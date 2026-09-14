import { execFile } from "node:child_process";
import { existsSync, mkdtempSync, rmSync, symlinkSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { gitAsync, type Task } from "./board.ts";
import type { ConflictResolver, OutputSink } from "./runner.ts";

/**
 * Integration validation must never run a task's checks in the human's checkout:
 * checks would then see unrelated work-in-progress (validating a tree that is
 * not the one committed) and could silently rewrite that WIP when the path was
 * already dirty, leaving `git status` unchanged. Instead the exact merge
 * candidate is materialised in a throwaway worktree, checks run there, and only
 * the validated content is applied back to the target branch afterwards.
 */

/**
 * Non-trimming git runner. `gitAsync` trims its stdout, which corrupts the
 * leading status column of `git status --porcelain -z` (an unstaged entry starts
 * with a space) and any NUL-separated path list. Lists that we parse are read
 * with this runner; scalar values still use `gitAsync`.
 */
function gitRaw(cwd: string, ...args: string[]): Promise<string> {
  return new Promise((resolve, reject) => {
    execFile("git", ["-C", cwd, ...args], { encoding: "utf8", maxBuffer: 8 * 1024 * 1024 }, (error, stdout) => {
      if (error) reject(error);
      else resolve(stdout);
    });
  });
}

async function snapshot(cwd: string): Promise<string> {
  await gitAsync(cwd, "add", "--all");
  return gitAsync(cwd, "write-tree");
}

/**
 * Every path that differs between two commits, including both sides of a
 * rename (rename detection is off so an old path deleted by the merge is seen
 * as a collision candidate too). NUL separation keeps paths with spaces intact.
 */
export async function changedPaths(cwd: string, from: string, to: string): Promise<Set<string>> {
  const raw = await gitRaw(cwd, "diff", "--name-only", "--no-renames", "-z", from, to);
  return new Set(raw.split("\0").filter(Boolean));
}

/**
 * Locally dirty paths from `git status --porcelain -z`: staged, unstaged,
 * renamed (both the source and destination) and untracked entries. Parsed
 * NUL-first so paths containing spaces are not quoted or split. Used to check
 * the merge against the human's in-progress work before Git is asked to touch it.
 */
export async function dirtyPaths(cwd: string): Promise<Set<string>> {
  const tokens = (await gitRaw(cwd, "status", "--porcelain", "-z")).split("\0");
  const paths = new Set<string>();
  for (let index = 0; index < tokens.length; index += 1) {
    const entry = tokens[index];
    if (!entry) continue;
    const status = entry.slice(0, 2);
    paths.add(entry.slice(3));
    // A rename/copy record is `XY NEW\0OLD\0`; guard the user's old path too.
    if (status[0] === "R" || status[0] === "C") {
      const original = tokens[index + 1];
      if (original) { paths.add(original); index += 1; }
    }
  }
  return paths;
}

/**
 * Root paths the merge would write that exist only as ignored/untracked files
 * in the human's checkout. `git status` omits ignored files, so apply would
 * silently replace them; treat them as a collision and defer instead.
 */
export async function ignoredCollisions(cwd: string, paths: Iterable<string>): Promise<Set<string>> {
  const collided = new Set<string>();
  for (const path of paths) {
    if (!existsSync(join(cwd, path))) continue;
    try {
      await gitAsync(cwd, "check-ignore", "--quiet", "--", path);
      collided.add(path);
    } catch { /* tracked or not ignored: no collision */ }
  }
  return collided;
}

/**
 * After checks pass in the throwaway worktree, rewrite the human's checkout so
 * its tracked content for the merge's paths matches the validated candidate and
 * advance the target branch to that exact commit. Only the changed paths are
 * touched: unrelated staged, unstaged and untracked files keep their bytes and
 * index entries. No stash/clean/reset-hard is ever used; a failure part-way
 * through restores just the paths already written.
 */
export async function applyValidatedCandidate(cwd: string, target: string, base: string, candidate: string): Promise<void> {
  const tokens = (await gitRaw(cwd, "diff", "--name-status", "--no-renames", "-z", base, candidate)).split("\0");
  const applied: Array<{ path: string; status: string }> = [];
  try {
    for (let index = 0; index < tokens.length; index += 1) {
      const status = tokens[index];
      if (!status) continue;
      const path = tokens[index + 1];
      if (path === undefined) break;
      index += 1;
      if (status[0] === "D") await gitAsync(cwd, "rm", "--quiet", "--", path);
      else await gitAsync(cwd, "checkout", candidate, "--", path);
      applied.push({ path, status });
    }
    // Advance the branch last, so a partial apply never leaves HEAD ahead of the
    // working tree. `update-ref` is atomic; a failure rolls the paths back.
    await gitAsync(cwd, "update-ref", `refs/heads/${target}`, candidate);
  } catch (error) {
    for (const { path, status } of applied.reverse()) {
      try {
        if (status[0] === "A") await gitAsync(cwd, "rm", "--quiet", "--", path);
        else await gitAsync(cwd, "checkout", base, "--", path);
      } catch { /* best-effort targeted rollback of the paths already applied */ }
    }
    throw error;
  }
}

export interface CandidateMerge {
  /** The validated merge commit; the target branch is advanced to exactly this. */
  commit: string;
  /** Tree of `commit`, the tree the checks actually validated. */
  tree: string;
}

export interface PrepareCandidateOptions {
  /** The human's checkout; the disposable worktree is created from its repository. */
  repoRoot: string;
  /** Target branch tip the candidate must merge onto. */
  base: string;
  /** The worker's validated result commit. */
  resultCommit: string;
  task: Task;
  output: OutputSink;
  /** Runs the task's checks (the shell output sink is the caller's). */
  runChecks: (cwd: string) => Promise<void>;
  resolveConflict: ConflictResolver;
}

/**
 * Dependencies are provided deliberately, not by accident. Package-manager
 * stores (`node_modules`) are the only root-only state reachable from the
 * disposable worktree, linked one level above it so Node's normal resolution
 * finds them while a check's cwd stays free of the human's checkout. Config and
 * build output are NOT provisioned, so a check that quietly relies on them
 * fails loudly instead of validating stale root state.
 */
function linkDependencies(repoRoot: string, holder: string): void {
  const source = join(repoRoot, "node_modules");
  const target = join(holder, "node_modules");
  if (!existsSync(source) || existsSync(target)) return;
  try { symlinkSync(source, target, "dir"); } catch { /* best effort; a missing dep store fails the checks */ }
}

/** Materialise the merge of `resultCommit` onto `base` in a throwaway worktree. */
async function mergeResult(worktree: string, resultCommit: string, task: Task, output: OutputSink, resolveConflict: ConflictResolver): Promise<void> {
  try {
    await gitAsync(worktree, "merge", "--no-edit", "--no-ff", resultCommit);
    return;
  } catch (error) {
    const conflictedRaw = (await gitAsync(worktree, "diff", "--name-only", "--diff-filter=U")).trim();
    if (!conflictedRaw) {
      await gitAsync(worktree, "merge", "--abort").catch(() => undefined);
      throw error;
    }
    const conflicted = conflictedRaw.split("\n").map((path) => path.trim()).filter(Boolean);
    try {
      await resolveConflict(task, worktree, output);
      // Stage only the conflicted paths; nothing else in the throwaway worktree.
      await gitAsync(worktree, "add", "--", ...conflicted);
      if (await gitAsync(worktree, "diff", "--name-only", "--diff-filter=U")) throw new Error("unresolved conflicts remain");
      // `git diff --check` rejects leftover `<<<<<<<` / `>>>>>>>` markers.
      try { await gitAsync(worktree, "diff", "--cached", "--check"); }
      catch { throw new Error("merge left conflict markers"); }
      await gitAsync(worktree, "commit", "--no-edit");
    } catch (resolveError) {
      await gitAsync(worktree, "merge", "--abort").catch(() => undefined);
      throw new Error(`Merge conflict unresolved: ${resolveError instanceof Error ? resolveError.message : String(resolveError)}`);
    }
  }
}

/**
 * Validate the exact candidate tree in a disposable worktree and return it only
 * if the checks neither changed its content nor moved its HEAD. The worktree is
 * always removed; nothing it contains can leak into the human's checkout.
 */
export async function prepareCandidate(options: PrepareCandidateOptions): Promise<CandidateMerge> {
  const { repoRoot, base, resultCommit, task, output, runChecks, resolveConflict } = options;
  const holder = mkdtempSync(join(tmpdir(), "midas-integration-"));
  const worktree = join(holder, "worktree");
  try {
    linkDependencies(repoRoot, holder);
    await gitAsync(repoRoot, "worktree", "add", "--detach", worktree, base);
    await mergeResult(worktree, resultCommit, task, output, resolveConflict);
    const commit = await gitAsync(worktree, "rev-parse", "HEAD");
    const tree = await gitAsync(worktree, "rev-parse", "HEAD^{tree}");
    // Provenance: the candidate must be the merge of exactly the two validated
    // inputs, not something the worktree drifted into.
    const parents = (await gitAsync(worktree, "rev-list", "--parents", "-n", "1", "HEAD")).split(/\s+/).slice(1);
    if (parents.length >= 2) {
      if (parents[0] !== base || !parents.includes(resultCommit)) throw new Error("Integration candidate is not a merge of the validated result");
    } else if (commit !== base) {
      throw new Error("Integration candidate is not a merge of the validated result");
    }
    const before = await snapshot(worktree);
    await runChecks(worktree);
    if (await snapshot(worktree) !== before) throw new Error("Integration checks modified the candidate tree; refusing to merge unvalidated changes");
    if (await gitAsync(worktree, "rev-parse", "HEAD") !== commit) throw new Error("Integration checks moved the candidate HEAD; refusing to merge unvalidated changes");
    return { commit, tree };
  } finally {
    await gitAsync(repoRoot, "worktree", "remove", "--force", worktree).catch(() => undefined);
    rmSync(holder, { recursive: true, force: true });
  }
}
