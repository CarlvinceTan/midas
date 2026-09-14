import assert from "node:assert/strict";
import { test } from "node:test";
import { initTheme as initPiTheme } from "@earendil-works/pi-coding-agent";
import type { TuiMouseEvent } from "@earendil-works/pi-tui";
import { CONTENT_END, CONTENT_START, DECORATION, stripAnsi } from "../../lib/ansi.ts";
import type { QuestionView } from "../../state/transcript.ts";
import { initTheme, theme } from "../../theme/theme.ts";
import { QuestionDialog } from "./question-dialog.ts";

initPiTheme(undefined, false);
initTheme(undefined);

function request(questions: QuestionView["questions"]): QuestionView {
  return { id: "q1", questions };
}

const mouse = (x: number, y: number): TuiMouseEvent =>
  ({ type: "click", button: "left", x, y }) as unknown as TuiMouseEvent;

/** Rendered row carrying the inverse-video block cursor, or -1 when hidden. */
const cursorRow = (lines: string[]): number => lines.findIndex((line) => line.includes("\x1b[7m"));

/** Text between a row's content markers; empty for a border or spacer row. */
const content = (line: string): string => {
  const start = line.indexOf(CONTENT_START);
  const end = line.indexOf(CONTENT_END);
  return start < 0 || end < 0 ? "" : line.slice(start + CONTENT_START.length, end);
};

test("the question counter sits in the top border, not the body", () => {
  const dialog = new QuestionDialog(
    request([
      { question: "Pick one", options: [{ label: "A" }, { label: "B" }] },
      { question: "Pick two", options: [{ label: "C" }] },
    ]),
    () => {},
    () => {},
  );
  const plain = dialog.render(60).map(stripAnsi);
  assert.ok(plain[0]!.includes("Question 1/2"), `counter missing from the border: ${plain[0]}`);
  assert.ok(!plain.slice(1, -1).join("\n").includes("Question 1/2"), `counter should not be in the body: ${plain.join("\n")}`);

  // Answering the first prompt advances the border counter.
  dialog.handleInput("\r");
  assert.ok(dialog.render(60).map(stripAnsi)[0]!.includes("Question 2/2"));
});

test("the question is the first body content, with a blank line above and below", () => {
  const dialog = new QuestionDialog(
    request([{ header: "Choose wisely", question: "What next?", options: [{ label: "A" }] }]),
    () => {},
    () => {},
  );
  const raw = dialog.render(60);
  const plain = raw.map(stripAnsi);
  assert.ok(!plain.slice(1, -1).join("\n").includes("Choose wisely"), "the header should not be rendered");
  const questionAt = raw.findIndex((line) => line.includes("What next?"));
  assert.ok(questionAt >= 2, `question row missing: ${plain.join("\n")}`);
  assert.equal(content(raw[questionAt - 1]!), "", "expected a blank line above the question");
  assert.equal(content(raw[questionAt + 1]!), "", "expected a blank line below the question");
  const question = raw[questionAt]!;
  assert.ok(question.includes(theme().fg("text", "What next?")), `question is not primary text: ${JSON.stringify(question)}`);
});

test("the question lines up with the counter in the top border", () => {
  const dialog = new QuestionDialog(
    request([{ question: "Why?", options: [{ label: "A" }] }]),
    () => {},
    () => {},
  );
  const raw = dialog.render(50);
  const visible = (line: string): string => stripAnsi(line).replace(/\x1b\][^\x07]*\x07/g, "");
  const top = visible(raw[0]!);
  const question = visible(raw.find((line) => line.includes("Why?"))!);
  assert.equal(question.indexOf("Why?"), top.indexOf("Question"), `question not aligned with the counter: ${question}`);
});

test("the border counter tracks multi-question prompts without a header", () => {
  const dialog = new QuestionDialog(
    request([
      { header: "Remove UX", question: "How should remove appear?", options: [{ label: "A" }] },
      { header: "Running tasks", question: "Allow removing a running task?", options: [{ label: "B" }] },
    ]),
    () => {},
    () => {},
  );
  const border = () => dialog.render(60).map(stripAnsi)[0]!;
  assert.ok(border().includes("Question 1/2"), border());
  assert.ok(!dialog.render(60).map(stripAnsi).join("\n").includes("Remove UX"), "headers should not be rendered");
  dialog.handleInput("\r");
  assert.ok(border().includes("Question 2/2"), border());
  assert.ok(!dialog.render(60).map(stripAnsi).join("\n").includes("Running tasks"), "headers should not be rendered");
});

test("content bounds exclude the border and padding from selection", () => {
  const dialog = new QuestionDialog(
    request([{ question: "What next?", options: [{ label: "Alpha" }] }]),
    () => {},
    () => {},
  );
  const lines = dialog.render(40);
  // Border rows are decoration with empty content bounds, so they never select.
  for (const edge of [lines[0]!, lines.at(-1)!]) {
    assert.ok(edge.includes(DECORATION), "border row is not marked as decoration");
    assert.ok(edge.includes(CONTENT_START + CONTENT_END), "border row content bounds are not empty");
  }
  // Body rows bound only their text; the gutter, fill and border stay outside.
  for (const line of lines.slice(1, -1)) {
    const start = line.indexOf(CONTENT_START);
    const end = line.indexOf(CONTENT_END);
    assert.ok(start > 0 && end > start, `body row lacks content bounds: ${JSON.stringify(stripAnsi(line))}`);
    assert.equal(stripAnsi(line.slice(0, start)).trim(), "│", "gutter is inside the selection");
    assert.equal(stripAnsi(line.slice(end + CONTENT_END.length)).trim(), "│", "fill/border is inside the selection");
  }
});

test("a blank line separates the question from the options, with panel gutters", () => {
  const dialog = new QuestionDialog(
    request([{ question: "What next?", options: [{ label: "Alpha" }, { label: "Beta" }] }]),
    () => {},
    () => {},
  );
  const raw = dialog.render(40);
  const plain = raw.map(stripAnsi);
  const questionAt = raw.findIndex((line) => line.includes("What next?"));
  assert.ok(questionAt >= 1, `question row missing: ${plain.join("\n")}`);
  assert.equal(content(raw[questionAt + 1]!), "", "expected a blank line below the question");
  assert.ok(plain[questionAt + 2]!.includes("Alpha"), `option not below the blank line: ${plain.join("\n")}`);
  for (const line of plain.slice(1, -1)) {
    assert.ok(line.startsWith("│ "), `missing left gutter: ${line}`);
    assert.ok(line.endsWith(" │"), `missing right gutter: ${line}`);
  }
});

test("option descriptions sit under their option, with a blank row before the bottom border", () => {
  const dialog = new QuestionDialog(
    request([
      {
        question: "How?",
        options: [
          { label: "A", description: "the first way" },
          { label: "B", description: "the second way" },
        ],
      },
    ]),
    () => {},
    () => {},
  );
  const raw = dialog.render(50);
  const plain = raw.map(stripAnsi);
  const aAt = plain.findIndex((line) => line.includes("[x] A"));
  assert.ok(plain[aAt + 1]!.includes("the first way"), "the A description sits directly under A");
  const bAt = plain.findIndex((line) => line.includes("[ ] B"));
  assert.ok(plain[bAt + 1]!.includes("the second way"), "the B description sits directly under B");
  const otherAt = plain.findIndex((line) => line.includes("Other"));
  assert.equal(content(raw[otherAt + 1]!), "", "a blank row separates the last option from the bottom border");
  assert.ok(plain[otherAt + 2]!.includes("╰"), "the bottom border follows the blank row");
});

test("option descriptions keep the same gap on the right as the left", () => {
  const dialog = new QuestionDialog(
    request([
      {
        question: "Why?",
        options: [{ label: "A", description: "one two three four five six seven eight nine ten eleven twelve" }],
      },
    ]),
    () => {},
    () => {},
  );
  const raw = dialog.render(50);
  // total 50 − two borders − two gutter columns.
  const contentWidth = 46;
  const description = raw.filter((line) => /\b(two|four|six|eight|ten|twelve)\b/.test(stripAnsi(line)));
  assert.ok(description.length >= 1, `description rows missing: ${raw.map(stripAnsi).join("\n")}`);
  for (const line of description) {
    const text = stripAnsi(content(line));
    assert.ok(
      contentWidth - text.length >= 5,
      `description should leave the label indent on the right: ${JSON.stringify(text)}`,
    );
  }
});

test("options show [x]/[ ] and Up/Down do not wrap", () => {
  const dialog = new QuestionDialog(
    request([{ question: "Why?", options: [{ label: "A" }, { label: "B" }] }]),
    () => {},
    () => {},
  );
  const screen = () => dialog.render(40).map(stripAnsi);
  assert.ok(screen().some((line) => line.includes("[x] A")), "the first option should start chosen");
  assert.ok(screen().some((line) => line.includes("[ ] B")), "the other options should be unchecked");

  dialog.handleInput("\x1b[A"); // up at the top
  assert.ok(screen().some((line) => line.includes("[x] A")), "Up at the top must not wrap to the bottom");

  dialog.handleInput("\x1b[B"); // down to B
  assert.ok(screen().some((line) => line.includes("[x] B")), "Down should move the choice");

  dialog.handleInput("\x1b[B"); // down to Other (the last entry)
  dialog.handleInput("\x1b[B"); // down at the bottom
  // Staying on Other shows its inline answer field, which carries the block cursor.
  assert.ok(cursorRow(dialog.render(40)) >= 0, "Down at the bottom must stay on Other");
});

test("focused Other is checked before custom text is entered", () => {
  const dialog = new QuestionDialog(
    request([{ question: "Why?", options: [{ label: "A" }] }]),
    () => {},
    () => {},
  );
  const screen = () => dialog.render(40).map(stripAnsi).join("\n");

  assert.ok(screen().includes("[ ] Other"), "unfocused empty Other should start unchecked");
  dialog.handleInput("\x1b[B");
  assert.ok(screen().includes("[x] Other"), "focused empty Other should look selected");

  dialog.handleInput("\x1b[A");
  assert.ok(screen().includes("[ ] Other"), "empty Other should uncheck after focus leaves it");
});

test("bracketed paste inserts multiline text into Other", () => {
  let answers: string[][] | undefined;
  const dialog = new QuestionDialog(
    request([{ question: "Why?", options: [{ label: "A" }] }]),
    (value) => { answers = value; },
    () => {},
  );

  dialog.handleInput("\x1b[B"); // focus Other
  dialog.handleInput("\x1b[200~alpha\r\nbeta\tgamma\x1b[201~");
  const screen = dialog.render(40).map(stripAnsi).join("\n");
  assert.ok(screen.includes("alpha"), `pasted first line missing: ${screen}`);
  assert.ok(screen.includes("beta    gamma"), `pasted newline or tab missing: ${screen}`);

  dialog.handleInput("\r");
  assert.deepEqual(answers, [["alpha\nbeta    gamma"]]);
});

test("Other accepts a bracketed paste split across input chunks", () => {
  let answers: string[][] | undefined;
  const dialog = new QuestionDialog(
    request([{ question: "Why?", options: [{ label: "A" }] }]),
    (value) => { answers = value; },
    () => {},
  );

  dialog.handleInput("\x1b[B");
  dialog.handleInput("\x1b[200~split ");
  dialog.handleInput("paste\x1b[201~");
  dialog.handleInput("\r");
  assert.deepEqual(answers, [["split paste"]]);
});

test("a multi-choice question must be filled before Right advances", () => {
  const dialog = new QuestionDialog(
    request([
      { question: "Pick", multiple: true, options: [{ label: "A" }, { label: "B" }] },
      { question: "Next", options: [{ label: "C" }] },
    ]),
    () => {},
    () => {},
  );
  const screen = () => dialog.render(40).map(stripAnsi).join("\n");
  dialog.handleInput("\x1b[C"); // right with nothing ticked
  assert.ok(screen().includes("Question 1/2"), "Right must be blocked until the question is answered");
  dialog.handleInput(" "); // tick A
  assert.ok(screen().includes("[x] A"), "Space should tick the option");
  dialog.handleInput("\x1b[C");
  assert.ok(screen().includes("Question 2/2"), "Right should advance once the question is answered");
});

test("Left/Right move between questions without wrapping or losing answers", () => {
  let answers: string[][] | undefined;
  const dialog = new QuestionDialog(
    request([
      { header: "First", question: "One", options: [{ label: "A" }, { label: "B" }] },
      { header: "Second", question: "Two", options: [{ label: "C" }, { label: "D" }] },
    ]),
    (value) => { answers = value; },
    () => {},
  );
  const screen = () => dialog.render(40).map(stripAnsi).join("\n");
  dialog.handleInput("\x1b[D"); // left at the first question
  assert.ok(screen().includes("Question 1/2"), "Left at the first question must not wrap to the last");

  dialog.handleInput("\x1b[B"); // choose B
  dialog.handleInput("\x1b[C"); // right to the second question
  assert.ok(screen().includes("Question 2/2"), "Right should advance after a single-choice answer");
  dialog.handleInput("\x1b[C"); // right at the last question
  assert.ok(screen().includes("Question 2/2"), "Right at the last question must not wrap to the first");

  dialog.handleInput("\x1b[D"); // back to the first question
  assert.ok(screen().includes("Question 1/2"), "Left should step back");
  assert.ok(screen().includes("[x] B"), "the first question's answer should be preserved");

  dialog.handleInput("\x1b[C"); // forward again
  dialog.handleInput("\r"); // submit on the last question
  assert.deepEqual(answers, [["B"], ["C"]]);
});

/** Select the "Other" entry and type `text` into its free-text answer. */
function typeAnswer(dialog: QuestionDialog, text: string, width = 40): void {
  dialog.render(width);
  dialog.handleInput("\x1b[B"); // down to the last entry ("Other")
  for (const char of text) dialog.handleInput(char);
  dialog.render(width);
}

test("the Other answer wraps across rows instead of truncating", () => {
  const dialog = new QuestionDialog(
    request([{ question: "Why?", options: [{ label: "A" }] }]),
    () => {},
    () => {},
  );
  typeAnswer(dialog, "alpha beta gamma delta epsilon zeta eta theta iota kappa lambda mu", 40);
  const plain = dialog.render(40).map(stripAnsi);
  const body = plain.slice(1, -1).join("\n");
  assert.ok(body.includes("mu"), `answer tail was truncated: ${body}`);
  const first = plain.find((line) => line.includes("alpha"));
  const last = plain.find((line) => line.includes("mu"));
  assert.ok(first && last, `answer words missing: ${body}`);
  assert.notEqual(first, last, "a long answer should span more than one row");
  // Every answer row keeps the panel's right gutter.
  for (const line of plain.slice(1, -1)) {
    if (line.includes("alpha") || line.includes("lambda mu")) assert.ok(line.endsWith(" │"), `lost right gutter: ${line}`);
  }
});

test("the Other answer wraps whole words instead of splitting one", () => {
  const dialog = new QuestionDialog(
    request([{ question: "Why?", options: [{ label: "A" }] }]),
    () => {},
    () => {},
  );
  const wordA = "a".repeat(20);
  const wordB = "b".repeat(20);
  typeAnswer(dialog, `${wordA} ${wordB}`, 40);
  const body = dialog.render(40).map(stripAnsi).join("\n");
  assert.ok(body.includes(wordB), `the second word was split across rows: ${body}`);
});

test("the typed Other answer lines up under the option label", () => {
  const dialog = new QuestionDialog(
    request([{ question: "Why?", options: [{ label: "A" }] }]),
    () => {},
    () => {},
  );
  typeAnswer(dialog, "hello world", 40);
  const lines = dialog.render(40).map(stripAnsi);
  const other = lines.find((line) => line.includes("Other"))!;
  const answer = lines.find((line) => line.includes("hello"))!;
  assert.equal(answer.indexOf("hello"), other.indexOf("Other"), `answer not under the label: ${answer}`);
});

test("a wrapped answer line starts with a word, not a dangling space", () => {
  const dialog = new QuestionDialog(
    request([{ question: "Why?", options: [{ label: "A" }] }]),
    () => {},
    () => {},
  );
  // The first word exactly fills the wrap width (36 content cols − 5 label
  // indent − 1 reserved cursor col), so the following space cannot attach and
  // must not lead the continuation line.
  const filler = "a".repeat(30);
  const next = "bbbbb";
  typeAnswer(dialog, `${filler} ${next}`, 40);
  const line = dialog.render(40).find((value) => stripAnsi(value).includes(next));
  assert.ok(line, `continuation line missing: ${dialog.render(40).map(stripAnsi).join("\n")}`);
  assert.ok(
    stripAnsi(content(line)).startsWith(`     ${next}`),
    `continuation line should begin with the word under the Other label: ${JSON.stringify(stripAnsi(content(line)))}`,
  );
});

test("the Other answer scrolls and moves the cursor with Up/Down", () => {
  const dialog = new QuestionDialog(
    request([{ question: "Why?", options: [{ label: "A" }] }]),
    () => {},
    () => {},
  );
  typeAnswer(dialog, "alpha beta gamma delta epsilon zeta eta theta iota kappa lambda mu", 40);
  const rows = dialog.render(40);
  const bottom = cursorRow(rows);
  assert.ok(bottom > 0, "cursor should be visible at the end of the answer");

  dialog.handleInput("\x1b[A"); // up
  assert.equal(cursorRow(dialog.render(40)), bottom - 1, "Up moves the cursor one visual line up");

  // Up at the top of the answer leaves the field and selects the previous option.
  for (let i = 0; i < 10; i++) dialog.handleInput("\x1b[A");
  const off = dialog.render(40);
  assert.equal(cursorRow(off), -1, "cursor leaves the answer at the top");
  assert.ok(stripAnsi(off.find((line) => line.includes("A"))!).includes("[x]"), "selection moved to the option above");

  // Down returns to the answer.
  dialog.handleInput("\x1b[B");
  assert.ok(cursorRow(dialog.render(40)) >= 0, "Down returns to the answer");
});

test("clicking the Other answer moves the block cursor to that spot", () => {
  const dialog = new QuestionDialog(
    request([{ question: "Why?", options: [{ label: "A" }] }]),
    () => {},
    () => {},
  );
  typeAnswer(dialog, "hello", 40);
  const lines = dialog.render(40);
  const answerY = lines.findIndex((line) => stripAnsi(line).includes("hello"));
  assert.ok(answerY > 0, `answer row missing: ${lines.map(stripAnsi).join("\n")}`);

  // The answer text is indented past the option padding + `[x] ` checkbox to sit
  // under the option label, one column in from the border (x = 7 at this width).
  // Click between "h" and "e".
  assert.deepEqual(dialog.handleMouse(mouse(8, answerY)), { handled: true, render: true, focus: true });
  dialog.handleInput("X");
  const updated = dialog.render(40).map(stripAnsi).join("\n");
  assert.ok(updated.includes("hXello"), `cursor did not land mid-answer: ${updated}`);
});

test("clicking an option row selects it", () => {
  let answers: string[][] | undefined;
  const dialog = new QuestionDialog(
    request([{ question: "Why?", options: [{ label: "Alpha" }, { label: "Beta" }] }]),
    (value) => { answers = value; },
    () => {},
  );
  const lines = dialog.render(40);
  const alphaY = lines.findIndex((line) => stripAnsi(line).includes("Alpha"));
  assert.ok(alphaY > 0, "option row missing");
  dialog.handleMouse(mouse(3, alphaY));
  dialog.handleInput("\r");
  assert.deepEqual(answers, [["Alpha"]]);
});
