# Pasted file attachments: evidence and limits

Status: **automated integration proof complete for the terminal/path paste route;
native Finder clipboard acceptance in a real terminal is still a manual check.**
This document does not claim that a Finder file copy has been verified end to end
on a user's machine.

## What a user can paste

Midas installs a file paste handler on the editor. It only claims a paste when the
*whole* paste is unambiguously path-only (the T44 `parsePastedFilePaths` parser,
which never touches the filesystem or the clipboard):

- one absolute path (`/Users/me/notes.md`), one per line, or several on a line;
- `~/...` paths;
- `file://` URLs (including percent-encoded spaces);
- quoted paths (`'/tmp/My File.txt'`), backslash-escaped paths, spaces and Unicode;
- macOS screenshot names with a narrow no-break space.

Anything containing prose, a relative path or a URL is left as ordinary text, so a
sentence that merely *mentions* a path never attaches that file. Image paths keep
their existing `[Image: name]` chip behavior (the editor checks images first).
Ordinary, multiline and large text pastes are untouched (large pastes still become
`[paste #N]` markers).

An accepted paste becomes an atomic `[File: <basename>]` chip, rendered in the same
yellow style as image chips in the editor and in the transcript card. Duplicate
basenames get distinct identities (`[File: report.pdf]`, `[File: report.pdf (2)]`)
and their own payload; deleting a chip removes only its payload, and undo restores
the chip with its payload. Only an explicit insertion or paste registers a chip:
typed lookalikes and unknown pasted markers stay plain text and never read a file.

## Submit: what the controller receives

On submit (`src/ui/app.ts`):

- **Images** keep the exact existing behavior: the chip/path resolves to a base64
  data URL `PromptAttachment` (`data:<mime>;base64,...`), byte-for-byte.
- **UTF-8 text/code/documents** are read once and appended to the prompt as an
  explicitly labelled, untrusted file-content section:

  ```
  ----- BEGIN UNTRUSTED FILE CONTENT: notes.md (text/markdown, 42 bytes) -----
  <exact contents>
  ----- END UNTRUSTED FILE CONTENT: notes.md -----
  ```

  The visible chip label stays in the text. The content itself is inserted
  verbatim — tabs, CRLF/LF, Unicode and a trailing newline all survive, and the
  prompt's whitespace normalizer only ever runs over the visible text, never over
  the section. The label tells the model the section is untrusted data, never
  instructions. Provider support for a non-image file *part* is not established,
  so no such claim is made and no transport was changed.

## Supported formats and limits (from `src/lib/attachments.ts`)

| Kind | Accepted | Per-file cap | Notes |
| --- | --- | --- | --- |
| Image | `png jpg jpeg gif webp bmp heic heif tif tiff` | 25 MiB | exact bytes as a base64 data URL |
| Text / code / document | any valid UTF-8 that is not a detected binary | 1 MiB | advisory MIME by extension; unlisted extensions are `text/plain` |
| Batch | up to 8 files, 25 MiB combined | — | rejected before any large file is read |

Rejected as `unsupported-binary`: NUL bytes, invalid UTF-8, and the binary magic
prefixes `%PDF-`, `%!PS`, ELF, PNG, JPEG, GIF, ZIP, gzip, Matroska, Ogg, FLAC and
RIFF. Directories (`directory`), missing paths (`missing`), unreadable files
(`unreadable`) and oversized files (`oversize`) are reported distinctly.

Every failure names the file and a next step, and the input is restored with its
chips intact instead of being dropped or sent as a bare label. Examples:

- `Couldn't attach doc.pdf: binary files (including PDF) are not supported. Convert it to UTF-8 text or remove its chip.`
- `Couldn't attach nope.txt: the file no longer exists. Remove its chip or fix the path.`
- `Couldn't attach subdir: that path is a directory, not a file.`

## Frozen queued submissions

A busy input queues a `QueuedPrompt` carrying the visible text, any image
attachments, the file chips, and the once-read `frozenFiles` payloads. Editing a
queued follow-up restores only the visible text and chip mappings (never the
inlined content), so re-submitting cannot append the same content twice. Reusing
the frozen payload per chip identity means a file that changed after it was
attached is never silently re-read.

The queue, chip identities and frozen payloads are persisted per session in
isolated config roots and rehydrated on resume. Persistence keeps every payload
that fits its cap and drops only the offending ones, so one oversized image can
never discard the frozen text beside it. A generic file chip whose payload is
missing or malformed after a restart — dropped at persistence, corrupted in the
state file, or absent from a legacy queue that saved no snapshot — is marked
explicitly and **never re-read from disk on the user's behalf**. Flush, queue
edit/resubmit and active queued steer all pause instead: the queued message and
its chip identity are retained, and the user is told to re-attach the file or
remove the chip. Explicitly reattaching a file (or resubmitting it as fresh
input) reads and sends its current contents, while a healthy snapshot round-trips
exactly and stays frozen. Image-only queues keep their existing behavior; a
generic file queue saved without a snapshot is not treated as valid.

## Evidence matrix

Automated proof lives in `src/ui/file-attachments.test.ts` (24 tests), plus the
merged T44/T45 suites and the app/component suites. All of these run against a
fake controller, fake terminal and synthetic temp files under an isolated
`HOME`/`XDG_*`/`MIDAS_CONFIG_DIR`/`PI_CONFIG_DIR` sandbox. No model, opencode
server, board, clipboard or real user file is touched.

| # | Invariant | Result | Evidence |
| --- | --- | --- | --- |
| 1 | Path-only paste becomes a removable file chip; prose does not | pass | `file-attachments.test.ts` paste tests; `attachments.test.ts` parser tests |
| 2 | Multi-path paste yields one chip each | pass | `file-attachments.test.ts`, `file-chip-paste.test.ts` |
| 3 | Idle submit sends the exact file text (tabs, CRLF, Unicode, trailing newline) once | pass | `file-attachments.test.ts` |
| 4 | Content bypasses prompt whitespace normalization | pass | `file-attachments.test.ts` |
| 5 | Same-basename files keep distinct identities and payloads | pass | `file-chip-paste.test.ts`, `file-attachments.test.ts` |
| 6 | Deleting a chip excludes only its payload; undo restores payload | pass | `file-chip-paste.test.ts`, `file-attachments.test.ts` |
| 7 | Typed lookalikes / unknown markers / prose paths never read a file | pass | `file-chip-paste.test.ts`, `file-attachments.test.ts` |
| 8 | Images keep exact data URLs and add no file section | pass | `editor.test.ts`, `image-chip-paste.test.ts`, `file-attachments.test.ts` |
| 9 | Busy input queues; queued flush sends the frozen content, not a changed file | pass | `file-attachments.test.ts` |
| 10 | Editing a queued prompt neither duplicates nor re-reads | pass | `file-attachments.test.ts` |
| 11 | Reorder/requeue keeps the frozen payload at the slot | pass | `file-attachments.test.ts` |
| 12 | Active steer sends the exact content (typed and queued) | pass | `file-attachments.test.ts` |
| 13 | Drafts restore file chips and still submit content | pass | `drafts.test.ts`, `file-attachments.test.ts` |
| 14 | History recall revives a submitted chip instead of an inert label | pass | `file-attachments.test.ts` |
| 15 | Queue/chips/frozen payloads round-trip in isolated config roots | pass | `session-state.test.ts`, `file-attachments.test.ts` |
| 16 | Batch file/byte limits and specific errors keep the input recoverable | pass | `attachments.test.ts`, `file-attachments.test.ts` |
| 17 | Transcript renders `[File: …]` with the image-chip yellow style | pass | `components/user-prompt.test.ts` |
| 18 | `/goal` arguments/agent/busy and optional metadata safety are unchanged | pass | `goal-lifecycle.test.ts`, `goal-command.test.ts`, `task-metadata.test.ts` |
| 19 | A restart that loses a frozen payload pauses flush/edit/steer; only explicit reattachment reads the disk | pass | `file-attachments.test.ts`, `session-state.test.ts` |
| 20 | Per-file persistence keeps the valid payloads in a mixed batch; dropped/malformed/legacy chips are marked | pass | `session-state.test.ts`, `file-attachments.test.ts` |

Typecheck (`npm run typecheck`) passes with these changes.

## Not yet proven: native Finder clipboard

The supported **terminal** paste route is what is tested: a terminal sends text,
and macOS Finder's file copy lands in that stream as a path or `file://` URL, both
of which the parser already accepts. No explicit-paste-only native adapter was
added, because the path/`file://` text route already covers it and this task
forbids polling, reading or replacing the user's clipboard.

Still to be checked manually, on a real terminal with a real Finder copy (this is
**not** claimed by the unit tests above):

1. In iTerm2/Terminal, ⌘C a file in Finder, focus Midas and ⌘V.
2. Confirm a yellow `[File: <name>]` chip appears (not raw text), then Enter.
3. Confirm the reply shows the model used the file's contents.
4. Repeat for a PDF (expect the specific unsupported-binary error, input intact)
   and for two same-named files from different folders (expect distinct chips).
5. If a terminal instead pastes only a display name or some other non-path form,
   record that terminal and add an explicit-paste-only adapter in the optional
   `src/lib/clipboard-files.ts` module, mocked in tests, rather than touching the
   live clipboard.

Until step 1–5 are performed, native Finder acceptance remains unverified.
