import { getKeybindings } from "../keybindings.js";
import { decodePrintableKey, matchesKey } from "../keys.js";
import { KillRing } from "../kill-ring.js";
import { CURSOR_MARKER, } from "../tui.js";
import { UndoStack } from "../undo-stack.js";
import { cjkBreakRegex, getGraphemeSegmenter, getWordSegmenter, isWhitespaceChar, sliceByColumn, visibleWidth, } from "../utils.js";
import { findWordBackward, findWordForward } from "../word-navigation.js";
import { SelectList } from "./select-list.js";
const graphemeSegmenter = getGraphemeSegmenter();
const wordSegmenter = getWordSegmenter();
/** Regex matching paste markers like `[paste #1 +123 lines]` or `[paste #2 1234 chars]`. */
const PASTE_MARKER_REGEX = /\[paste #(\d+)( (\+\d+ lines|\d+ chars))?\]/g;
/** Non-global version for single-segment testing. */
const PASTE_MARKER_SINGLE = /^\[paste #(\d+)( (\+\d+ lines|\d+ chars))?\]$/;
/** Atomic image chips, e.g. `[Image: screenshot.png]`. */
const IMAGE_MARKER_REGEX = /\[Image: [^\]\n]*\]/g;
const IMAGE_MARKER_SINGLE = /^\[Image: [^\]\n]*\]$/;
/** Atomic generic file chips, e.g. `[File: report.pdf]`. */
const FILE_MARKER_REGEX = /\[File: [^\]\n]*\]/g;
const FILE_MARKER_SINGLE = /^\[File: [^\]\n]*\]$/;
/** A `[File: ...]` marker ending exactly at the cursor (used for typed input). */
const FILE_MARKER_SUFFIX = /\[File: [^\]\n]*\]$/;
/** Paths that become image chips when pasted/typed. */
const IMAGE_PATH_REGEX = /(?:^|[\s'"(])((?:\/|~\/)[^\n]*?\.(?:png|jpe?g|gif|webp|bmp|heic|heif|tiff?))['")]?$/i;
/** Check if a segment is an atomic marker (paste chunk, image chip or file chip). */
function isPasteMarker(segment) {
    return ((segment.length >= 10 && PASTE_MARKER_SINGLE.test(segment)) ||
        IMAGE_MARKER_SINGLE.test(segment) ||
        FILE_MARKER_SINGLE.test(segment));
}
/** Render atomic markers in yellow so chips stand out from editable text. */
function styleMarkers(text, bangColor, validImages, validPasteIds, validFiles) {
    const styled = styleBashPrefix(text, bangColor);
    if (!styled.includes("[paste #") && !styled.includes("[Image: ") && !styled.includes("[File: "))
        return styled;
    return styled
        .replace(PASTE_MARKER_REGEX, (m) => {
            const id = Number.parseInt(m.slice(8), 10);
            return validPasteIds && validPasteIds.has(id) ? `\x1b[33m${m}\x1b[39m` : m;
        })
        .replace(IMAGE_MARKER_REGEX, (m) => (validImages && validImages.has(m) ? `\x1b[33m${m}\x1b[39m` : m))
        .replace(FILE_MARKER_REGEX, (m) => (validFiles && validFiles.has(m) ? `\x1b[33m${m}\x1b[39m` : m));
}
/**
 * Color a leading `!`/`!!` shell prefix with the editor's border color, so the
 * bang matches the bash-mode border. The cursor marker may sit before it.
 */
function styleBashPrefix(text, color) {
    if (!color)
        return text;
    const match = /^(!{1,2}) /.exec(text);
    if (!match)
        return text;
    return color(match[1]) + text.slice(match[1].length);
}
/** Basename of a path without pulling in node:path. */
function baseName(path) {
    const parts = path.split("/");
    return parts[parts.length - 1] || path;
}
/**
 * Display name for an image chip. macOS screenshot names carry a narrow
 * no-break space (U+202F) before am/pm, whose rendered width disagrees with the
 * measured width and shifts following glyphs. Show a normal space instead; the
 * real path (kept separately) is unchanged.
 */
const UNICODE_SPACE_REGEX = /[\u00A0\u1680\u2000-\u200A\u202F\u205F\u3000]/g;
function displayImageName(name) {
    return name.replace(UNICODE_SPACE_REGEX, " ");
}
/** Control characters (including newlines) are never safe in a chip label. */
const CONTROL_CHAR_REGEX = /[\u0000-\u001f\u007f]/g;
/**
 * Sanitize a filename into a chip label that can never break the `[File: ...]`
 * marker syntax or leak a path separator. Paths are kept only in the payload,
 * never in the visible text.
 */
function safeFileName(name) {
    const cleaned = displayImageName(String(name ?? ""))
        .replace(/\]/g, " ")
        .replace(CONTROL_CHAR_REGEX, " ")
        .trim();
    return cleaned || "file";
}
/**
 * A segmenter that wraps Intl.Segmenter and merges graphemes that fall
 * within paste markers into single atomic segments.  This makes cursor
 * movement, deletion, word-wrap, etc. treat paste markers as single units.
 *
 * Only markers whose numeric ID exists in `validIds` and whose exact text is
 * present in `validImages`/`validFiles` are merged.
 */
function segmentWithMarkers(text, baseSegmenter, validIds, validImages, validFiles) {
    const hasPaste = validIds.size > 0 && text.includes("[paste #");
    const hasImage = (validImages ? validImages.size > 0 : false) && text.includes("[Image: ");
    const hasFile = (validFiles ? validFiles.size > 0 : false) && text.includes("[File: ");
    // Fast path: no markers in the text.
    if (!hasPaste && !hasImage && !hasFile) {
        return baseSegmenter.segment(text);
    }
    // Find all marker spans (paste markers must have a valid ID; image and file
    // chips must be registered in their respective valid sets).
    const markers = [];
    if (hasPaste) {
        for (const m of text.matchAll(PASTE_MARKER_REGEX)) {
            const id = Number.parseInt(m[1], 10);
            if (!validIds.has(id))
                continue;
            markers.push({ start: m.index, end: m.index + m[0].length });
        }
    }
    if (hasImage) {
        for (const m of text.matchAll(IMAGE_MARKER_REGEX)) {
            if (validImages && !validImages.has(m[0]))
                continue;
            markers.push({ start: m.index, end: m.index + m[0].length });
        }
    }
    if (hasFile) {
        for (const m of text.matchAll(FILE_MARKER_REGEX)) {
            if (validFiles && !validFiles.has(m[0]))
                continue;
            markers.push({ start: m.index, end: m.index + m[0].length });
        }
    }
    if (markers.length === 0) {
        return baseSegmenter.segment(text);
    }
    markers.sort((a, b) => a.start - b.start);
    // Build merged segment list.
    const baseSegments = baseSegmenter.segment(text);
    const result = [];
    let markerIdx = 0;
    for (const seg of baseSegments) {
        // Skip past markers that are entirely before this segment.
        while (markerIdx < markers.length && markers[markerIdx].end <= seg.index) {
            markerIdx++;
        }
        const marker = markerIdx < markers.length ? markers[markerIdx] : null;
        if (marker && seg.index >= marker.start && seg.index < marker.end) {
            // This segment falls inside a marker.
            // If this is the first segment of the marker, emit a merged segment.
            if (seg.index === marker.start) {
                const markerText = text.slice(marker.start, marker.end);
                result.push({
                    segment: markerText,
                    index: marker.start,
                    input: text,
                });
            }
            // Otherwise skip (already merged into the first segment).
        }
        else {
            result.push(seg);
        }
    }
    return result;
}
/**
 * Split a line into word-wrapped chunks.
 * Wraps at word boundaries when possible, falling back to character-level
 * wrapping for words longer than the available width.
 *
 * @param line - The text line to wrap
 * @param maxWidth - Maximum visible width per chunk
 * @param preSegmented - Optional pre-segmented graphemes (e.g. with paste-marker awareness).
 *                       When omitted the default Intl.Segmenter is used.
 * @returns Array of chunks with text and position information
 */
export function wordWrapLine(line, maxWidth, preSegmented) {
    if (!line || maxWidth <= 0) {
        return [{ text: "", startIndex: 0, endIndex: 0 }];
    }
    const lineWidth = visibleWidth(line);
    if (lineWidth <= maxWidth) {
        return [{ text: line, startIndex: 0, endIndex: line.length }];
    }
    const chunks = [];
    const segments = preSegmented ?? [...graphemeSegmenter.segment(line)];
    let currentWidth = 0;
    let chunkStart = 0;
    // Wrap opportunity: the position after the last whitespace before a non-whitespace
    // grapheme, i.e. where a line break is allowed.
    let wrapOppIndex = -1;
    let wrapOppWidth = 0;
    for (let i = 0; i < segments.length; i++) {
        const seg = segments[i];
        const grapheme = seg.segment;
        const gWidth = visibleWidth(grapheme);
        const charIndex = seg.index;
        const isWs = !isPasteMarker(grapheme) && isWhitespaceChar(grapheme);
        // Overflow check before advancing.
        if (currentWidth + gWidth > maxWidth) {
            if (wrapOppIndex >= 0 && currentWidth - wrapOppWidth + gWidth <= maxWidth) {
                // Backtrack to last wrap opportunity (the remaining content
                // plus the current grapheme still fits within maxWidth).
                chunks.push({ text: line.slice(chunkStart, wrapOppIndex), startIndex: chunkStart, endIndex: wrapOppIndex });
                chunkStart = wrapOppIndex;
                currentWidth -= wrapOppWidth;
            }
            else if (chunkStart < charIndex) {
                // No viable wrap opportunity: force-break at current position.
                // This also handles the case where backtracking to a word
                // boundary wouldn't help because the remaining content plus
                // the current grapheme (e.g. a wide character) still exceeds
                // maxWidth.
                chunks.push({ text: line.slice(chunkStart, charIndex), startIndex: chunkStart, endIndex: charIndex });
                chunkStart = charIndex;
                currentWidth = 0;
            }
            wrapOppIndex = -1;
        }
        if (gWidth > maxWidth) {
            // Single atomic segment wider than maxWidth (e.g. paste marker
            // in a narrow terminal). Re-wrap it at grapheme granularity.
            // The segment remains logically atomic for cursor
            // movement / editing — the split is purely visual for word-wrap layout.
            const subChunks = wordWrapLine(grapheme, maxWidth);
            for (let j = 0; j < subChunks.length - 1; j++) {
                const sc = subChunks[j];
                chunks.push({ text: sc.text, startIndex: charIndex + sc.startIndex, endIndex: charIndex + sc.endIndex });
            }
            const last = subChunks[subChunks.length - 1];
            chunkStart = charIndex + last.startIndex;
            currentWidth = visibleWidth(last.text);
            wrapOppIndex = -1;
            continue;
        }
        // Advance.
        currentWidth += gWidth;
        // Record wrap opportunity: whitespace followed by non-whitespace
        // (multiple spaces join; the break point is after the last space),
        // or at a boundary where either side is CJK (CJK allows breaking
        // between any adjacent characters).
        const next = segments[i + 1];
        if (isWs && next && (isPasteMarker(next.segment) || !isWhitespaceChar(next.segment))) {
            wrapOppIndex = next.index;
            wrapOppWidth = currentWidth;
        }
        else if (!isWs && next && !isWhitespaceChar(next.segment)) {
            const isCjk = !isPasteMarker(grapheme) && cjkBreakRegex.test(grapheme);
            const nextIsCjk = !isPasteMarker(next.segment) && cjkBreakRegex.test(next.segment);
            if (isCjk || nextIsCjk) {
                wrapOppIndex = next.index;
                wrapOppWidth = currentWidth;
            }
        }
    }
    // Push final chunk.
    chunks.push({ text: line.slice(chunkStart), startIndex: chunkStart, endIndex: line.length });
    return chunks;
}
const SLASH_COMMAND_SELECT_LIST_LAYOUT = {
    minPrimaryColumnWidth: 12,
    maxPrimaryColumnWidth: 32,
};
const ATTACHMENT_AUTOCOMPLETE_DEBOUNCE_MS = 20;
const DEFAULT_AUTOCOMPLETE_TRIGGER_CHARACTERS = ["@", "#"];
function escapeCharacterClass(value) {
    return value.replace(/[\\^$.*+?()[\]{}|-]/g, "\\$&");
}
function buildTriggerPattern(triggerCharacters) {
    return new RegExp(`(?:^|[\\s])[${triggerCharacters.map(escapeCharacterClass).join("")}][^\\s]*$`);
}
function buildDebouncePattern(triggerCharacters) {
    const escapedWithoutAt = triggerCharacters.filter((character) => character !== "@").map(escapeCharacterClass);
    return new RegExp(`(?:^|[ \\t])(?:@(?:"[^"]*|[^\\s]*)|[${escapedWithoutAt.join("")}][^\\s]*)$`);
}
function createScrollBorder(direction, hiddenLineCount, width) {
    const availableWidth = Math.max(0, width);
    const label = ` ${direction} ${hiddenLineCount} more `;
    const labelWidth = visibleWidth(label);
    if (labelWidth + 2 <= availableWidth) {
        const leftWidth = Math.floor((availableWidth - labelWidth) / 2);
        return "─".repeat(leftWidth) + label + "─".repeat(availableWidth - leftWidth - labelWidth);
    }
    const indicator = `─── ${direction} ${hiddenLineCount} more `;
    const remaining = availableWidth - visibleWidth(indicator);
    if (remaining >= 0)
        return indicator + "─".repeat(remaining);
    const ellipsis = "...".slice(0, availableWidth);
    const indicatorWidth = availableWidth - visibleWidth(ellipsis);
    return sliceByColumn(indicator, 0, indicatorWidth, true) + ellipsis;
}
export class Editor {
    state = {
        lines: [""],
        cursorLine: 0,
        cursorCol: 0,
    };
    /** Focusable interface - set by TUI when focus changes */
    focused = false;
    /** When false, render without the block cursor (history browsing). */
    cursorVisible = true;
    tui;
    theme;
    paddingX = 0;
    // Store last render geometry for cursor navigation and mouse hit-testing.
    lastWidth = 80;
    renderedVisibleLineCount = 1;
    renderedAutocompleteHeight = 0;
    // Vertical scrolling support
    scrollOffset = 0;
    // Border color (can be changed dynamically)
    borderColor;
    // Autocomplete support
    autocompleteProvider;
    autocompleteTriggerCharacters = [...DEFAULT_AUTOCOMPLETE_TRIGGER_CHARACTERS];
    autocompleteTriggerPattern = buildTriggerPattern(this.autocompleteTriggerCharacters);
    autocompleteDebouncePattern = buildDebouncePattern(this.autocompleteTriggerCharacters);
    autocompleteList;
    autocompleteState = null;
    autocompletePrefix = "";
    autocompleteMaxVisible = 5;
    autocompleteAbort;
    autocompleteDebounceTimer;
    autocompleteRequestTask = Promise.resolve();
    autocompleteStartToken = 0;
    autocompleteRequestId = 0;
    // Paste tracking for large pastes
    pastes = new Map();
    pasteCounter = 0;
    // Image chips: marker text -> source file path
    imageAttachments = new Map();
    // Session-lifetime memory of every chip created or restored (marker -> source
    // path). Unlike imageAttachments this survives setText()/submit, so a marker
    // copied out of the transcript can be re-attached when pasted back.
    knownImageMarkers = new Map();
    // Generic file chips: visible marker -> { path, id, name }. The marker is
    // the only thing in the visible text; path and identity stay in the payload.
    // Markers are unique even for same-basename files, so each chip owns its own
    // payload and deleting one never touches another.
    fileAttachments = new Map();
    // Session-lifetime memory of file chips (marker -> payload); survives
    // setText()/submit so a pasted marker can be re-attached.
    knownFileMarkers = new Map();
    // Optional host-supplied parser for generic file paths pasted into the
    // editor. Returning undefined leaves ordinary paste behavior untouched.
    filePasteHandler;
    // Monotonic source of unique chip identities (distinguishes same-basename
    // files from different directories).
    fileAttachmentCounter = 0;
    // Bracketed paste mode buffering
    pasteBuffer = "";
    isInPaste = false;
    // Prompt history for up/down navigation
    history = [];
    historyIndex = -1; // -1 = not browsing, 0 = most recent, 1 = older, etc.
    historyDraft = null;
    // Kill ring for Emacs-style kill/yank operations
    killRing = new KillRing();
    lastAction = null;
    // Character jump mode
    jumpMode = null;
    // Preferred visual column for vertical cursor movement (sticky column)
    preferredVisualCol = null;
    // When the cursor is snapped to the start of an atomic segment, e.g. a
    // paste marker, cursorCol no longer reflects where the cursor would have
    // landed. This field stores the pre-snap cursorCol so that the next
    // vertical move can resolve it to a visual column on whatever VL it belongs
    // to.
    snappedFromCursorCol = null;
    // Undo support
    undoStack = new UndoStack();
    onSubmit;
    onChange;
    disableSubmit = false;
    constructor(tui, theme, options = {}) {
        this.tui = tui;
        this.theme = theme;
        this.borderColor = theme.borderColor;
        const paddingX = options.paddingX ?? 0;
        this.paddingX = Number.isFinite(paddingX) ? Math.max(0, Math.floor(paddingX)) : 0;
        const maxVisible = options.autocompleteMaxVisible ?? 5;
        this.autocompleteMaxVisible = Number.isFinite(maxVisible) ? Math.max(3, Math.min(20, Math.floor(maxVisible))) : 5;
    }
    /** Set of currently valid paste IDs, for marker-aware segmentation. */
    validPasteIds() {
        return new Set(this.pastes.keys());
    }
    /** Image-chip markers created by pasting (typed `[Image: ...]` text is plain). */
    validImageMarkers() {
        return new Set(this.imageAttachments.keys());
    }
    /** Generic file-chip markers (typed `[File: ...]` text is plain). */
    validFileMarkers() {
        return new Set(this.fileAttachments.keys());
    }
    /** Segment text with paste-marker awareness, only merging valid markers. */
    segment(text, mode) {
        return segmentWithMarkers(text, mode === "word" ? wordSegmenter : graphemeSegmenter, this.validPasteIds(), this.validImageMarkers(), this.validFileMarkers());
    }
    getPaddingX() {
        return this.paddingX;
    }
    setPaddingX(padding) {
        const newPadding = Number.isFinite(padding) ? Math.max(0, Math.floor(padding)) : 0;
        if (this.paddingX !== newPadding) {
            this.paddingX = newPadding;
            this.tui.requestRender();
        }
    }
    getAutocompleteMaxVisible() {
        return this.autocompleteMaxVisible;
    }
    setAutocompleteMaxVisible(maxVisible) {
        const newMaxVisible = Number.isFinite(maxVisible) ? Math.max(3, Math.min(20, Math.floor(maxVisible))) : 5;
        if (this.autocompleteMaxVisible !== newMaxVisible) {
            this.autocompleteMaxVisible = newMaxVisible;
            this.tui.requestRender();
        }
    }
    setAutocompleteProvider(provider) {
        this.cancelAutocomplete();
        this.autocompleteProvider = provider;
        this.setAutocompleteTriggerCharacters(provider.triggerCharacters ?? []);
    }
    /**
     * Add a prompt to history for up/down arrow navigation.
     * Called after successful submission.
     */
    addToHistory(text) {
        const trimmed = text.trim();
        if (!trimmed)
            return;
        // Don't add consecutive duplicates
        if (this.history.length > 0 && this.history[0] === trimmed)
            return;
        this.history.unshift(trimmed);
        // Limit history size
        if (this.history.length > 100) {
            this.history.pop();
        }
    }
    isEditorEmpty() {
        return this.state.lines.length === 1 && this.state.lines[0] === "";
    }
    isOnFirstVisualLine() {
        const visualLines = this.buildVisualLineMap(this.lastWidth);
        const currentVisualLine = this.findCurrentVisualLine(visualLines);
        return currentVisualLine === 0;
    }
    isOnLastVisualLine() {
        const visualLines = this.buildVisualLineMap(this.lastWidth);
        const currentVisualLine = this.findCurrentVisualLine(visualLines);
        return currentVisualLine === visualLines.length - 1;
    }
    navigateHistory(direction) {
        this.lastAction = null;
        if (this.history.length === 0)
            return;
        const newIndex = this.historyIndex - direction; // Up(-1) increases index, Down(1) decreases
        if (newIndex < -1 || newIndex >= this.history.length)
            return;
        // Capture state when first entering history browsing mode
        if (this.historyIndex === -1 && newIndex >= 0) {
            this.pushUndoSnapshot();
            this.historyDraft = structuredClone(this.state);
        }
        this.historyIndex = newIndex;
        if (this.historyIndex === -1) {
            const draft = this.historyDraft;
            this.historyDraft = null;
            if (draft) {
                this.state = draft;
                this.preferredVisualCol = null;
                this.snappedFromCursorCol = null;
                this.scrollOffset = 0;
                if (this.onChange)
                    this.onChange(this.getText());
            }
            else {
                this.setTextInternal("");
            }
        }
        else {
            this.setTextInternal(this.history[this.historyIndex] || "", direction === -1 ? "start" : "end");
        }
    }
    exitHistoryBrowsing() {
        this.historyIndex = -1;
        this.historyDraft = null;
    }
    /** Internal setText that doesn't reset history state - used by navigateHistory */
    setTextInternal(text, cursorPlacement = "end") {
        const lines = text.split("\n");
        this.state.lines = lines.length === 0 ? [""] : lines;
        this.state.cursorLine = cursorPlacement === "start" ? 0 : this.state.lines.length - 1;
        this.setCursorCol(cursorPlacement === "start" ? 0 : this.state.lines[this.state.cursorLine]?.length || 0);
        // Reset scroll - render() will adjust to show cursor
        this.scrollOffset = 0;
        if (this.onChange) {
            this.onChange(this.getText());
        }
    }
    invalidate() {
        // No cached state to invalidate currently
    }
    renderTopBorder(width, hiddenLineCount) {
        const border = hiddenLineCount > 0 ? createScrollBorder("↑", hiddenLineCount, width) : "─".repeat(width);
        return this.borderColor(border);
    }
    renderBottomBorder(width, hiddenLineCount) {
        const border = hiddenLineCount > 0 ? createScrollBorder("↓", hiddenLineCount, width) : "─".repeat(width);
        return this.borderColor(border);
    }
    render(width) {
        const maxPadding = Math.max(0, Math.floor((width - 1) / 2));
        const paddingX = Math.min(this.paddingX, maxPadding);
        const contentWidth = Math.max(1, width - paddingX * 2);
        // Layout width: with padding the cursor can overflow into it,
        // without padding we reserve 1 column for the cursor.
        const layoutWidth = Math.max(1, contentWidth - (paddingX ? 0 : 1));
        // Store for cursor navigation (must match wrapping width)
        this.lastWidth = layoutWidth;
        // Layout the text
        const layoutLines = this.layoutText(layoutWidth);
        // Calculate max visible lines: 30% of terminal height, minimum 5 lines
        const terminalRows = this.tui.terminal.rows;
        const maxVisibleLines = Math.max(5, Math.floor(terminalRows * 0.3));
        // Find the cursor line index in layoutLines
        let cursorLineIndex = layoutLines.findIndex((line) => line.hasCursor);
        if (cursorLineIndex === -1)
            cursorLineIndex = 0;
        // Adjust scroll offset to keep cursor visible
        if (cursorLineIndex < this.scrollOffset) {
            this.scrollOffset = cursorLineIndex;
        }
        else if (cursorLineIndex >= this.scrollOffset + maxVisibleLines) {
            this.scrollOffset = cursorLineIndex - maxVisibleLines + 1;
        }
        // Clamp scroll offset to valid range
        const maxScrollOffset = Math.max(0, layoutLines.length - maxVisibleLines);
        this.scrollOffset = Math.max(0, Math.min(this.scrollOffset, maxScrollOffset));
        // Get visible lines slice
        const visibleLines = layoutLines.slice(this.scrollOffset, this.scrollOffset + maxVisibleLines);
        this.renderedVisibleLineCount = visibleLines.length;
        const result = [];
        const leftPadding = " ".repeat(paddingX);
        const rightPadding = leftPadding;
        // Render top border (with scroll indicator if scrolled down)
        result.push(this.renderTopBorder(width, this.scrollOffset));
        // Render each visible layout line
        // Emit hardware cursor marker when focused so TUI can position the
        // hardware cursor for IME candidate-window placement even while
        // autocomplete (e.g. slash-command menu) is visible.
        const emitCursorMarker = this.focused;
        for (const layoutLine of visibleLines) {
            const indent = layoutLine.indent || 0;
            const indentText = " ".repeat(indent);
            // `cursorPos` is relative to the line text, so shift it past the
            // hanging indent used by shell-mode continuation lines.
            const cursorPos = (layoutLine.cursorPos ?? 0) + indent;
            let displayText = indentText + layoutLine.text;
            let lineVisibleWidth = visibleWidth(displayText);
            let cursorInPadding = false;
            // Add cursor if this line has it
            if (this.cursorVisible !== false && layoutLine.hasCursor && layoutLine.cursorPos !== undefined) {
                const before = displayText.slice(0, cursorPos);
                const after = displayText.slice(cursorPos);
                // Hardware cursor marker (zero-width, emitted before fake cursor for IME positioning)
                const marker = emitCursorMarker ? CURSOR_MARKER : "";
                if (after.length > 0) {
                    // Cursor is on a character (grapheme) - replace it with highlighted version
                    // Get the first grapheme from 'after'
                    const afterGraphemes = [...this.segment(after, "grapheme")];
                    const firstGrapheme = afterGraphemes[0]?.segment || "";
                    const restAfter = after.slice(firstGrapheme.length);
                    const cursor = `\x1b[7m${firstGrapheme}\x1b[0m`;
                    displayText = before + marker + cursor + restAfter;
                    // lineVisibleWidth stays the same - we're replacing, not adding
                }
                else {
                    // Cursor is at the end - add highlighted space
                    const cursor = "\x1b[7m \x1b[0m";
                    displayText = before + marker + cursor;
                    lineVisibleWidth = lineVisibleWidth + 1;
                    // If cursor overflows content width into the padding, flag it
                    if (lineVisibleWidth > contentWidth && paddingX > 0) {
                        cursorInPadding = true;
                    }
                }
            }
            // Calculate padding based on actual visible width
            const padding = " ".repeat(Math.max(0, contentWidth - lineVisibleWidth));
            const lineRightPadding = cursorInPadding ? rightPadding.slice(1) : rightPadding;
            // Render the line (no side borders, just horizontal lines above and below)
            result.push(`${leftPadding}${styleMarkers(displayText, this.borderColor, this.validImageMarkers(), this.validPasteIds(), this.validFileMarkers())}${padding}${lineRightPadding}`);
        }
        // Render bottom border (with scroll indicator if more content below)
        const linesBelow = layoutLines.length - (this.scrollOffset + visibleLines.length);
        result.push(this.renderBottomBorder(width, linesBelow));
        // Add autocomplete list if active
        this.renderedAutocompleteHeight = 0;
        if (this.autocompleteState && this.autocompleteList) {
            const autocompleteResult = this.autocompleteList.render(contentWidth);
            this.renderedAutocompleteHeight = autocompleteResult.length;
            for (const line of autocompleteResult) {
                const lineWidth = visibleWidth(line);
                const linePadding = " ".repeat(Math.max(0, contentWidth - lineWidth));
                result.push(`${leftPadding}${line}${linePadding}${rightPadding}`);
            }
        }
        return result;
    }
    handleMouse(event) {
        const autocompleteStartRow = this.renderedVisibleLineCount + 2;
        if (this.autocompleteState &&
            this.autocompleteList &&
            event.y >= autocompleteStartRow &&
            event.y < autocompleteStartRow + this.renderedAutocompleteHeight) {
            const maxPadding = Math.max(0, Math.floor((event.width - 1) / 2));
            const paddingX = Math.min(this.paddingX, maxPadding);
            const contentWidth = Math.max(1, event.width - paddingX * 2);
            const result = this.autocompleteList.handleMouse?.({
                ...event,
                x: event.x - paddingX,
                y: event.y - autocompleteStartRow,
                width: contentWidth,
                height: this.renderedAutocompleteHeight,
            });
            return result ? { ...result, focus: true } : undefined;
        }
        // Leave press/drag/release unhandled so the renderer's screen-level text
        // selection can run over the editor rows (drag to select, release to copy).
        // The renderer synthesizes a click when press and release land on the same
        // cell without movement, which is the gesture that positions the cursor.
        if (event.type !== "click" || event.button !== "left")
            return undefined;
        if (event.y <= 0 || event.y > this.renderedVisibleLineCount)
            return { handled: true, focus: true };
        const visualLines = this.buildVisualLineMap(this.lastWidth);
        const visualLineIndex = this.scrollOffset + event.y - 1;
        const visualLine = visualLines[visualLineIndex];
        if (!visualLine)
            return { handled: true, focus: true };
        const logicalLine = this.state.lines[visualLine.logicalLine] ?? "";
        const chunkEnd = visualLine.startCol + visualLine.length;
        const chunk = logicalLine.slice(visualLine.startCol, chunkEnd);
        const maxPadding = Math.max(0, Math.floor((event.width - 1) / 2));
        const paddingX = Math.min(this.paddingX, maxPadding);
        const targetColumn = Math.max(0, event.x - paddingX);
        let visibleColumn = 0;
        let targetIndex = chunk.length;
        let lastGraphemeIndex = 0;
        for (const grapheme of this.segment(chunk, "grapheme")) {
            const nextColumn = visibleColumn + visibleWidth(grapheme.segment);
            lastGraphemeIndex = grapheme.index;
            if (targetColumn < nextColumn) {
                targetIndex = grapheme.index;
                break;
            }
            visibleColumn = nextColumn;
        }
        const isLastSegment = visualLineIndex === visualLines.length - 1 ||
            visualLines[visualLineIndex + 1]?.logicalLine !== visualLine.logicalLine;
        if (!isLastSegment && targetIndex === chunk.length && chunk.length > 0)
            targetIndex = lastGraphemeIndex;
        this.state.cursorLine = visualLine.logicalLine;
        this.setCursorCol(visualLine.startCol + targetIndex);
        this.lastAction = null;
        this.exitHistoryBrowsing();
        if (this.autocompleteState)
            this.updateAutocomplete();
        return { handled: true, focus: true };
    }
    handleInput(data) {
        const kb = getKeybindings();
        // Handle character jump mode (awaiting next character to jump to)
        if (this.jumpMode !== null) {
            // Cancel if the hotkey is pressed again
            if (kb.matches(data, "tui.editor.jumpForward") || kb.matches(data, "tui.editor.jumpBackward")) {
                this.jumpMode = null;
                return;
            }
            const printable = decodePrintableKey(data) ?? (data.charCodeAt(0) >= 32 ? data : undefined);
            if (printable !== undefined) {
                // Printable character - perform the jump
                const direction = this.jumpMode;
                this.jumpMode = null;
                this.jumpToChar(printable, direction);
                return;
            }
            // Control character - cancel and fall through to normal handling
            this.jumpMode = null;
        }
        // Handle bracketed paste mode
        if (data.includes("\x1b[200~")) {
            this.isInPaste = true;
            this.pasteBuffer = "";
            data = data.replace("\x1b[200~", "");
        }
        if (this.isInPaste) {
            this.pasteBuffer += data;
            const endIndex = this.pasteBuffer.indexOf("\x1b[201~");
            if (endIndex !== -1) {
                const pasteContent = this.pasteBuffer.substring(0, endIndex);
                if (pasteContent.length > 0) {
                    this.handlePaste(pasteContent);
                }
                this.isInPaste = false;
                const remaining = this.pasteBuffer.substring(endIndex + 6);
                this.pasteBuffer = "";
                if (remaining.length > 0) {
                    this.handleInput(remaining);
                }
                return;
            }
            return;
        }
        // Ctrl+C - let parent handle (exit/clear)
        if (kb.matches(data, "tui.input.copy")) {
            return;
        }
        // Undo
        if (kb.matches(data, "tui.editor.undo")) {
            this.undo();
            return;
        }
        // Handle autocomplete mode
        if (this.autocompleteState && this.autocompleteList) {
            if (kb.matches(data, "tui.select.cancel")) {
                this.cancelAutocomplete();
                return;
            }
            if (kb.matches(data, "tui.select.up") || kb.matches(data, "tui.select.down")) {
                this.autocompleteList.handleInput(data);
                return;
            }
            if (kb.matches(data, "tui.input.tab")) {
                const selected = this.autocompleteList.getSelectedItem();
                if (selected && this.autocompleteProvider) {
                    this.pushUndoSnapshot();
                    this.lastAction = null;
                    const result = this.autocompleteProvider.applyCompletion(this.state.lines, this.state.cursorLine, this.state.cursorCol, selected, this.autocompletePrefix);
                    this.state.lines = result.lines;
                    this.state.cursorLine = result.cursorLine;
                    this.setCursorCol(result.cursorCol);
                    this.cancelAutocomplete();
                    if (this.onChange)
                        this.onChange(this.getText());
                }
                return;
            }
            if (kb.matches(data, "tui.select.confirm")) {
                const selected = this.autocompleteList.getSelectedItem();
                if (selected && this.autocompleteProvider) {
                    this.pushUndoSnapshot();
                    this.lastAction = null;
                    const result = this.autocompleteProvider.applyCompletion(this.state.lines, this.state.cursorLine, this.state.cursorCol, selected, this.autocompletePrefix);
                    this.state.lines = result.lines;
                    this.state.cursorLine = result.cursorLine;
                    this.setCursorCol(result.cursorCol);
                    if (this.autocompletePrefix.startsWith("/")) {
                        this.cancelAutocomplete();
                        // Fall through to submit
                    }
                    else {
                        this.cancelAutocomplete();
                        if (this.onChange)
                            this.onChange(this.getText());
                        return;
                    }
                }
            }
        }
        // Tab - trigger completion
        if (kb.matches(data, "tui.input.tab") && !this.autocompleteState) {
            this.handleTabCompletion();
            return;
        }
        // Deletion actions
        if (kb.matches(data, "tui.editor.deleteToLineEnd")) {
            this.deleteToEndOfLine();
            return;
        }
        if (kb.matches(data, "tui.editor.deleteToLineStart")) {
            this.deleteToStartOfLine();
            return;
        }
        if (kb.matches(data, "tui.editor.deleteWordBackward")) {
            this.deleteWordBackwards();
            return;
        }
        if (kb.matches(data, "tui.editor.deleteWordForward")) {
            this.deleteWordForward();
            return;
        }
        if (kb.matches(data, "tui.editor.deleteCharBackward") || matchesKey(data, "shift+backspace")) {
            this.handleBackspace();
            return;
        }
        if (kb.matches(data, "tui.editor.deleteCharForward") || matchesKey(data, "shift+delete")) {
            this.handleForwardDelete();
            return;
        }
        // Kill ring actions
        if (kb.matches(data, "tui.editor.yank")) {
            this.yank();
            return;
        }
        if (kb.matches(data, "tui.editor.yankPop")) {
            this.yankPop();
            return;
        }
        // Dedicated history actions always browse entries instead of moving the cursor.
        if (kb.matches(data, "tui.editor.historyPrevious")) {
            this.cancelAutocomplete();
            this.navigateHistory(-1);
            return;
        }
        if (kb.matches(data, "tui.editor.historyNext")) {
            this.cancelAutocomplete();
            this.navigateHistory(1);
            return;
        }
        // Cursor movement actions
        if (kb.matches(data, "tui.editor.cursorLineStart")) {
            this.moveToLineStart();
            return;
        }
        if (kb.matches(data, "tui.editor.cursorLineEnd")) {
            this.moveToLineEnd();
            return;
        }
        if (kb.matches(data, "tui.editor.cursorWordLeft")) {
            this.moveWordBackwards();
            return;
        }
        if (kb.matches(data, "tui.editor.cursorWordRight")) {
            this.moveWordForwards();
            return;
        }
        // New line
        if (kb.matches(data, "tui.input.newLine") ||
            (data.charCodeAt(0) === 10 && data.length > 1) ||
            data === "\x1b\r" ||
            data === "\x1b[13;2~" ||
            (data.length > 1 && data.includes("\x1b") && data.includes("\r")) ||
            (data === "\n" && data.length === 1)) {
            if (this.shouldSubmitOnBackslashEnter(data, kb)) {
                this.handleBackspace();
                this.submitValue();
                return;
            }
            this.addNewLine();
            return;
        }
        // Submit (Enter)
        if (kb.matches(data, "tui.input.submit")) {
            if (this.disableSubmit)
                return;
            // Workaround for terminals without Shift+Enter support:
            // If char before cursor is \, delete it and insert newline instead of submitting.
            const currentLine = this.state.lines[this.state.cursorLine] || "";
            if (this.state.cursorCol > 0 && currentLine[this.state.cursorCol - 1] === "\\") {
                this.handleBackspace();
                this.addNewLine();
                return;
            }
            this.submitValue();
            return;
        }
        // Arrow key navigation (with history support)
        if (kb.matches(data, "tui.editor.cursorUp")) {
            if (this.isOnFirstVisualLine() &&
                (this.isEditorEmpty() || this.historyIndex > -1 || this.state.cursorCol === 0)) {
                this.navigateHistory(-1);
            }
            else if (this.isOnFirstVisualLine()) {
                // Already at top - jump to start of line
                this.moveToLineStart();
            }
            else {
                this.moveCursor(-1, 0);
            }
            return;
        }
        if (kb.matches(data, "tui.editor.cursorDown")) {
            if (this.historyIndex > -1 && this.isOnLastVisualLine()) {
                this.navigateHistory(1);
            }
            else if (this.isOnLastVisualLine()) {
                // Already at bottom - jump to end of line
                this.moveToLineEnd();
            }
            else {
                this.moveCursor(1, 0);
            }
            return;
        }
        if (kb.matches(data, "tui.editor.cursorRight")) {
            this.moveCursor(0, 1);
            return;
        }
        if (kb.matches(data, "tui.editor.cursorLeft")) {
            this.moveCursor(0, -1);
            return;
        }
        // Page up/down - scroll by page and move cursor
        if (kb.matches(data, "tui.editor.pageUp")) {
            this.pageScroll(-1);
            return;
        }
        if (kb.matches(data, "tui.editor.pageDown")) {
            this.pageScroll(1);
            return;
        }
        // Character jump mode triggers
        if (kb.matches(data, "tui.editor.jumpForward")) {
            this.jumpMode = "forward";
            return;
        }
        if (kb.matches(data, "tui.editor.jumpBackward")) {
            this.jumpMode = "backward";
            return;
        }
        // Shift+Space - insert regular space
        if (matchesKey(data, "shift+space")) {
            this.insertCharacter(" ");
            return;
        }
        const printable = decodePrintableKey(data);
        if (printable !== undefined) {
            this.insertCharacter(printable);
            return;
        }
        // Regular characters
        if (data.charCodeAt(0) >= 32) {
            this.insertCharacter(data);
        }
    }
    /**
     * Hanging indent (columns) for shell-mode continuation lines. When the input
     * starts with a `!`/`!!` command prefix, later visual lines hang under the
     * command text instead of under the bang; `0` means ordinary wrapping.
     */
    hangingIndent() {
        const first = this.state.lines[0] || "";
        const match = /^(!{1,2}) /.exec(first);
        return match ? match[1].length + 1 : 0;
    }
    /**
     * Split one logical line into visual chunks, flagging shell-mode
     * continuation chunks with the hanging indent so rendering and the visual
     * line map stay in sync (and cursor navigation keeps working).
     */
    visualChunks(lineIndex, width) {
        const line = this.state.lines[lineIndex] || "";
        const indent = this.hangingIndent();
        if (line.length === 0) {
            // A continuation line still reserves the indent so the cursor rests
            // at the command column.
            return [{ text: "", startIndex: 0, endIndex: 0, indent: lineIndex > 0 ? indent : 0 }];
        }
        if (indent === 0) {
            if (visibleWidth(line) <= width) {
                return [{ text: line, startIndex: 0, endIndex: line.length, indent: 0 }];
            }
            return wordWrapLine(line, width, [...this.segment(line, "grapheme")]).map((chunk) => ({ ...chunk, indent: 0 }));
        }
        const restWidth = Math.max(1, width - indent);
        if (lineIndex > 0) {
            return wordWrapLine(line, restWidth, [...this.segment(line, "grapheme")]).map((chunk) => ({ ...chunk, indent }));
        }
        // First logical line: the opening chunk may use the full width, the
        // wrapped remainder hangs under the command text.
        const head = wordWrapLine(line, width, [...this.segment(line, "grapheme")]);
        if (head.length <= 1) {
            return head.map((chunk) => ({ ...chunk, indent: 0 }));
        }
        const chunks = [{ ...head[0], indent: 0 }];
        const offset = head[0].endIndex;
        const remainder = line.slice(offset);
        if (remainder.length > 0) {
            for (const chunk of wordWrapLine(remainder, restWidth, [...this.segment(remainder, "grapheme")])) {
                chunks.push({ text: chunk.text, startIndex: offset + chunk.startIndex, endIndex: offset + chunk.endIndex, indent });
            }
        }
        return chunks;
    }
    layoutText(contentWidth) {
        const layoutLines = [];
        if (this.state.lines.length === 0 || (this.state.lines.length === 1 && this.state.lines[0] === "")) {
            // Empty editor
            layoutLines.push({
                text: "",
                hasCursor: true,
                cursorPos: 0,
                indent: 0,
            });
            return layoutLines;
        }
        // Process each logical line
        for (let i = 0; i < this.state.lines.length; i++) {
            const chunks = this.visualChunks(i, contentWidth);
            const isCurrentLine = i === this.state.cursorLine;
            for (let chunkIndex = 0; chunkIndex < chunks.length; chunkIndex++) {
                const chunk = chunks[chunkIndex];
                if (!chunk)
                    continue;
                const cursorPos = this.state.cursorCol;
                const isLastChunk = chunkIndex === chunks.length - 1;
                const indent = chunk.indent || 0;
                // Determine if cursor is in this chunk
                // For word-wrapped chunks, we need to handle the case where
                // cursor might be in trimmed whitespace at end of chunk
                let hasCursorInChunk = false;
                let adjustedCursorPos = 0;
                if (isCurrentLine) {
                    if (isLastChunk) {
                        // Last chunk: cursor belongs here if >= startIndex
                        hasCursorInChunk = cursorPos >= chunk.startIndex;
                        adjustedCursorPos = cursorPos - chunk.startIndex;
                    }
                    else {
                        // Non-last chunk: cursor belongs here if in range [startIndex, endIndex)
                        // But we need to handle the visual position in the trimmed text
                        hasCursorInChunk = cursorPos >= chunk.startIndex && cursorPos < chunk.endIndex;
                        if (hasCursorInChunk) {
                            adjustedCursorPos = cursorPos - chunk.startIndex;
                            // Clamp to text length (in case cursor was in trimmed whitespace)
                            if (adjustedCursorPos > chunk.text.length) {
                                adjustedCursorPos = chunk.text.length;
                            }
                        }
                    }
                }
                if (hasCursorInChunk) {
                    layoutLines.push({
                        text: chunk.text,
                        hasCursor: true,
                        cursorPos: adjustedCursorPos,
                        indent,
                    });
                }
                else {
                    layoutLines.push({
                        text: chunk.text,
                        hasCursor: false,
                        indent,
                    });
                }
            }
        }
        return layoutLines;
    }
    getText() {
        return this.state.lines.join("\n");
    }
    expandPasteMarkers(text) {
        let result = text;
        for (const [pasteId, pasteContent] of this.pastes) {
            const markerRegex = new RegExp(`\\[paste #${pasteId}( (\\+\\d+ lines|\\d+ chars))?\\]`, "g");
            result = result.replace(markerRegex, () => pasteContent);
        }
        return result;
    }
    /**
     * Get text with paste markers expanded to their actual content.
     * Use this when you need the full content (e.g., for external editor).
     */
    getExpandedText() {
        return this.expandPasteMarkers(this.state.lines.join("\n"));
    }
    getLines() {
        return [...this.state.lines];
    }
    getCursor() {
        return { line: this.state.cursorLine, col: this.state.cursorCol };
    }
    setText(text) {
        this.cancelAutocomplete();
        this.lastAction = null;
        this.exitHistoryBrowsing();
        const normalized = this.normalizeText(text);
        // Push undo snapshot if content differs (makes programmatic changes undoable)
        if (this.getText() !== normalized) {
            this.pushUndoSnapshot();
        }
        this.pastes.clear();
        this.pasteCounter = 0;
        // A wholesale text replacement drops any chips the old text carried.
        this.imageAttachments.clear();
        this.fileAttachments.clear();
        this.setTextInternal(normalized);
    }
    /**
     * Insert text at the current cursor position.
     * Used for programmatic insertion (e.g., clipboard image markers).
     * This is atomic for undo - single undo restores entire pre-insert state.
     */
    insertTextAtCursor(text) {
        if (!text)
            return;
        this.cancelAutocomplete();
        this.pushUndoSnapshot();
        this.lastAction = null;
        this.exitHistoryBrowsing();
        this.insertTextAtCursorInternal(text);
    }
    /**
     * Insert an atomic `[Image: name]` chip at the cursor, remembering its
     * source path so the host can attach the file on submit.
     */
    insertImageAttachment(path, displayName) {
        if (!path)
            return;
        const name = displayImageName(displayName || baseName(path));
        const marker = `[Image: ${name}]`;
        this.imageAttachments.set(marker, path);
        this.knownImageMarkers.set(marker, path);
        // Always leave a plain space after the chip so following text does not
        // glue onto it. The space is ordinary buffer text, not part of the chip.
        const currentLine = this.state.lines[this.state.cursorLine] || "";
        const separator = this.state.cursorCol < currentLine.length && currentLine[this.state.cursorCol] === " " ? "" : " ";
        this.insertTextAtCursor(marker + separator);
    }
    /** Image chips currently present in the text, with their source paths. */
    getImageAttachments() {
        const text = this.getText();
        const result = [];
        for (const [marker, path] of this.imageAttachments) {
            if (text.includes(marker))
                result.push({ marker, path });
        }
        return result;
    }
    /** Re-register image chips (e.g. when restoring a saved draft) without re-inserting their text. */
    setImageAttachments(attachments) {
        this.imageAttachments.clear();
        for (const attachment of attachments ?? []) {
            if (attachment && attachment.marker) {
                this.imageAttachments.set(attachment.marker, attachment.path);
                this.knownImageMarkers.set(attachment.marker, attachment.path);
            }
        }
    }
    /**
     * Pick a visible file-chip marker that is not already present in the text,
     * so same-basename files from different directories each own a distinct
     * chip. Only the (sanitized) filename is visible; the path stays in the
     * payload.
     */
    uniqueFileMarker(name) {
        const text = this.getText();
        let candidate = `[File: ${name}]`;
        let suffix = 1;
        while (text.includes(candidate)) {
            suffix++;
            candidate = `[File: ${name} (${suffix})]`;
        }
        return candidate;
    }
    /**
     * Insert an atomic `[File: name]` chip at the cursor, remembering its source
     * path and a unique identity so the host can attach the file on submit.
     * Returns the visible marker that was inserted.
     */
    insertFileAttachment(path, options = {}) {
        if (!path)
            return;
        const name = safeFileName(options?.displayName || baseName(path));
        const marker = this.uniqueFileMarker(name);
        const id = options?.id || `file-${++this.fileAttachmentCounter}`;
        const attachment = { marker, path, id, name };
        this.fileAttachments.set(marker, attachment);
        this.knownFileMarkers.set(marker, attachment);
        // Always leave a plain space after the chip so following text does not
        // glue onto it. The space is ordinary buffer text, not part of the chip.
        const currentLine = this.state.lines[this.state.cursorLine] || "";
        const separator = this.state.cursorCol < currentLine.length && currentLine[this.state.cursorCol] === " " ? "" : " ";
        this.insertTextAtCursor(marker + separator);
        return marker;
    }
    /** Generic file chips currently present in the text, with their payloads. */
    getFileAttachments() {
        const text = this.getText();
        const result = [];
        for (const attachment of this.fileAttachments.values()) {
            if (text.includes(attachment.marker)) {
                result.push({ ...attachment });
            }
        }
        return result;
    }
    /**
     * Re-register generic file chips (e.g. when restoring a saved draft) without
     * re-inserting their text. Legacy `{ marker, path }` entries are accepted;
     * missing identity/name fields are filled in.
     */
    setFileAttachments(attachments) {
        this.fileAttachments.clear();
        for (const attachment of attachments ?? []) {
            if (!attachment || !attachment.marker)
                continue;
            const name = safeFileName(attachment.name || (attachment.path ? baseName(attachment.path) : "file"));
            const id = attachment.id || `file-${++this.fileAttachmentCounter}`;
            const record = { marker: attachment.marker, path: attachment.path, id, name };
            this.fileAttachments.set(record.marker, record);
            this.knownFileMarkers.set(record.marker, record);
        }
    }
    /**
     * Install (or clear) the host application's parser for generic file paths
     * pasted into the editor. The handler receives the cleaned pasted text and
     * returns the attachments to create, or undefined to leave the paste as
     * ordinary text. This keeps filesystem/path policy in the app, not the
     * vendor editor.
     */
    setFilePasteHandler(handler) {
        this.filePasteHandler = typeof handler === "function" ? handler : undefined;
    }
    /**
     * Normalize the value returned by a file paste handler into an array of
     * `{ path, ... }` inputs, or undefined when the handler did not claim the
     * paste.
     */
    normalizeFilePasteInputs(inputs) {
        if (!inputs)
            return undefined;
        const list = Array.isArray(inputs) ? inputs : [inputs];
        const result = [];
        for (const input of list) {
            if (!input)
                continue;
            if (typeof input === "string") {
                if (input)
                    result.push({ path: input });
            }
            else if (input.path) {
                result.push(input);
            }
        }
        return result.length > 0 ? result : undefined;
    }
    /**
     * Only explicit insertion or paste may register a file chip. If a character
     * just typed by hand completed a `[File: name]` marker whose only earlier
     * occurrence was not in the buffer, drop any stale/session mapping so the
     * typed lookalike stays plain text.
     */
    suppressTypedFileMarker(previousText) {
        if (this.fileAttachments.size === 0)
            return;
        const line = this.state.lines[this.state.cursorLine] || "";
        const match = FILE_MARKER_SUFFIX.exec(line.slice(0, this.state.cursorCol));
        if (!match)
            return;
        const marker = match[0];
        if (previousText.includes(marker))
            return;
        this.fileAttachments.delete(marker);
    }
    /**
     * Collapse a just-typed/pasted image path in front of the cursor into an
     * atomic chip. Returns true when a chip was created.
     */
    maybeCollapseImagePath() {
        const line = this.state.lines[this.state.cursorLine] || "";
        const upto = line.slice(0, this.state.cursorCol);
        const match = upto.match(IMAGE_PATH_REGEX);
        if (!match)
            return false;
        const path = match[1];
        const start = this.state.cursorCol - path.length;
        const marker = `[Image: ${displayImageName(baseName(path))}]`;
        this.imageAttachments.set(marker, path);
        this.knownImageMarkers.set(marker, path);
        // Always separate the chip from following text with a plain space (see
        // insertImageAttachment); don't double up on an existing one.
        const separator = this.state.cursorCol < line.length && line[this.state.cursorCol] === " " ? "" : " ";
        this.state.lines[this.state.cursorLine] =
            line.slice(0, start) + marker + separator + line.slice(this.state.cursorCol);
        this.setCursorCol(start + marker.length + separator.length);
        return true;
    }
    /**
     * Normalize text for editor storage:
     * - Normalize line endings (\r\n and \r -> \n)
     * - Expand tabs to 4 spaces
     */
    normalizeText(text) {
        return text.replace(/\r\n/g, "\n").replace(/\r/g, "\n").replace(/\t/g, "    ");
    }
    /**
     * Internal text insertion at cursor. Handles single and multi-line text.
     * Does not push undo snapshots or trigger autocomplete - caller is responsible.
     * Normalizes line endings and calls onChange once at the end.
     */
    insertTextAtCursorInternal(text) {
        if (!text)
            return;
        // Normalize line endings and tabs
        const normalized = this.normalizeText(text);
        const insertedLines = normalized.split("\n");
        const currentLine = this.state.lines[this.state.cursorLine] || "";
        const beforeCursor = currentLine.slice(0, this.state.cursorCol);
        const afterCursor = currentLine.slice(this.state.cursorCol);
        if (insertedLines.length === 1) {
            // Single line - insert at cursor position
            this.state.lines[this.state.cursorLine] = beforeCursor + normalized + afterCursor;
            this.setCursorCol(this.state.cursorCol + normalized.length);
        }
        else {
            // Multi-line insertion
            this.state.lines = [
                // All lines before current line
                ...this.state.lines.slice(0, this.state.cursorLine),
                // The first inserted line merged with text before cursor
                beforeCursor + insertedLines[0],
                // All middle inserted lines
                ...insertedLines.slice(1, -1),
                // The last inserted line with text after cursor
                insertedLines[insertedLines.length - 1] + afterCursor,
                // All lines after current line
                ...this.state.lines.slice(this.state.cursorLine + 1),
            ];
            this.state.cursorLine += insertedLines.length - 1;
            this.setCursorCol((insertedLines[insertedLines.length - 1] || "").length);
        }
        if (this.onChange) {
            this.onChange(this.getText());
        }
    }
    // All the editor methods from before...
    insertCharacter(char, skipUndoCoalescing) {
        this.exitHistoryBrowsing();
        // Undo coalescing (fish-style):
        // - Consecutive word chars coalesce into one undo unit
        // - Space captures state before itself (so undo removes space+following word together)
        // - Each space is separately undoable
        // Skip coalescing when called from atomic operations (e.g., handlePaste)
        if (!skipUndoCoalescing) {
            if (isWhitespaceChar(char) || this.lastAction !== "type-word") {
                this.pushUndoSnapshot();
            }
            this.lastAction = "type-word";
        }
        const line = this.state.lines[this.state.cursorLine] || "";
        const before = line.slice(0, this.state.cursorCol);
        const after = line.slice(this.state.cursorCol);
        const textBeforeChar = this.getText();
        this.state.lines[this.state.cursorLine] = before + char + after;
        this.setCursorCol(this.state.cursorCol + char.length);
        // Typed `[File: ...]` text must stay plain even if a chip by that name
        // was remembered; only insertion/paste registers a file chip.
        this.suppressTypedFileMarker(textBeforeChar);
        // A completed image path typed/dragged into the input becomes an atomic chip.
        this.maybeCollapseImagePath();
        if (this.onChange) {
            this.onChange(this.getText());
        }
        // Check if we should trigger or update autocomplete
        if (!this.autocompleteState) {
            // Auto-trigger for "/" at the start of a line (slash commands)
            if (char === "/" && this.isAtStartOfMessage()) {
                this.tryTriggerAutocomplete();
            }
            // Auto-trigger for symbol-based completion like @, #, or provider triggers at token boundaries
            else if (this.autocompleteTriggerCharacters.includes(char)) {
                const currentLine = this.state.lines[this.state.cursorLine] || "";
                const textBeforeCursor = currentLine.slice(0, this.state.cursorCol);
                const charBeforeSymbol = textBeforeCursor[textBeforeCursor.length - 2];
                if (textBeforeCursor.length === 1 || charBeforeSymbol === " " || charBeforeSymbol === "\t") {
                    this.tryTriggerAutocomplete();
                }
            }
            // Also auto-trigger when typing letters in a slash command or symbol completion context
            else if (/[a-zA-Z0-9.\-_]/.test(char)) {
                const currentLine = this.state.lines[this.state.cursorLine] || "";
                const textBeforeCursor = currentLine.slice(0, this.state.cursorCol);
                // Check if we're in a slash command (with or without space for arguments)
                if (this.isInSlashCommandContext(textBeforeCursor)) {
                    this.tryTriggerAutocomplete();
                }
                // Check if we're in a symbol-based completion context like @, #, or provider triggers
                else if (this.autocompleteTriggerPattern.test(textBeforeCursor)) {
                    this.tryTriggerAutocomplete();
                }
            }
        }
        else {
            this.updateAutocomplete();
        }
    }
    handlePaste(pastedText) {
        this.cancelAutocomplete();
        this.exitHistoryBrowsing();
        this.lastAction = null;
        this.pushUndoSnapshot();
        // Some terminals (e.g. tmux popups with extended-keys-format=csi-u) re-encode
        // control bytes inside bracketed paste as CSI-u Ctrl+<letter> sequences
        // (ESC [ <codepoint> ; 5 u). Decode those back to their literal byte so the
        // per-char filter below preserves newlines instead of stripping ESC and
        // leaking the printable tail (e.g. "[106;5u") into the editor.
        const decodedText = pastedText.replace(/\x1b\[(\d+);5u/g, (match, code) => {
            const cp = Number(code);
            if (cp >= 97 && cp <= 122)
                return String.fromCharCode(cp - 96);
            if (cp >= 65 && cp <= 90)
                return String.fromCharCode(cp - 64);
            return match;
        });
        // Clean the pasted text: normalize line endings, expand tabs
        const cleanText = this.normalizeText(decodedText);
        // Filter out non-printable characters except newlines
        let filteredText = cleanText
            .split("")
            .filter((char) => char === "\n" || char.charCodeAt(0) >= 32)
            .join("");
        // Re-attach chips copied out of the transcript: a pasted `[Image: name]`
        // marker only becomes a live chip when we still remember the source path
        // it was created from. Markers with no memory (or text typed by hand)
        // stay plain, so pasted text is still inserted verbatim below.
        for (const match of filteredText.matchAll(IMAGE_MARKER_REGEX)) {
            const marker = match[0];
            if (this.imageAttachments.has(marker))
                continue;
            const knownPath = this.knownImageMarkers.get(marker);
            if (knownPath)
                this.imageAttachments.set(marker, knownPath);
        }
        // The same re-attachment applies to generic file chips. Only exact
        // markers we created/restored are revived; unknown literals stay plain.
        for (const match of filteredText.matchAll(FILE_MARKER_REGEX)) {
            const marker = match[0];
            if (this.fileAttachments.has(marker))
                continue;
            const known = this.knownFileMarkers.get(marker);
            if (known)
                this.fileAttachments.set(marker, known);
        }
        // A pasted image path (e.g. a screenshot dragged into the terminal)
        // becomes an atomic chip and is attached when the prompt is sent.
        const singleLine = filteredText.trim();
        if (singleLine && !singleLine.includes("\n")) {
            const imageMatch = singleLine.match(IMAGE_PATH_REGEX);
            if (imageMatch && imageMatch[1]) {
                this.insertImageAttachment(imageMatch[1]);
                return;
            }
        }
        // Let the host application claim generic file paths. The handler owns
        // path/filesystem policy; when it returns nothing the paste keeps its
        // ordinary (single-line, multiline or large-paste) behavior.
        if (this.filePasteHandler) {
            const fileInputs = this.normalizeFilePasteInputs(this.filePasteHandler(filteredText));
            if (fileInputs) {
                for (const input of fileInputs) {
                    this.insertFileAttachment(input.path, input);
                }
                return;
            }
        }
        // If pasting a file path (starts with /, ~, or .) and the character before
        // the cursor is a word character, prepend a space for better readability
        if (/^[/~.]/.test(filteredText)) {
            const currentLine = this.state.lines[this.state.cursorLine] || "";
            const charBeforeCursor = this.state.cursorCol > 0 ? currentLine[this.state.cursorCol - 1] : "";
            if (charBeforeCursor && /\w/.test(charBeforeCursor)) {
                filteredText = ` ${filteredText}`;
            }
        }
        // Split into lines to check for large paste
        const pastedLines = filteredText.split("\n");
        // Check if this is a large paste (> 10 lines or > 1000 characters)
        const totalChars = filteredText.length;
        if (pastedLines.length > 10 || totalChars > 1000) {
            // Store the paste and insert a marker
            this.pasteCounter++;
            const pasteId = this.pasteCounter;
            this.pastes.set(pasteId, filteredText);
            // Insert marker like "[paste #1 +123 lines]" or "[paste #1 1234 chars]"
            const marker = pastedLines.length > 10
                ? `[paste #${pasteId} +${pastedLines.length} lines]`
                : `[paste #${pasteId} ${totalChars} chars]`;
            // Trailing space is separate from the chip (see insertImageAttachment).
            this.insertTextAtCursorInternal(marker + " ");
            return;
        }
        if (pastedLines.length === 1) {
            // Single line - insert atomically (do not trigger autocomplete during paste)
            this.insertTextAtCursorInternal(filteredText);
            return;
        }
        // Multi-line paste - use direct state manipulation
        this.insertTextAtCursorInternal(filteredText);
    }
    addNewLine() {
        this.cancelAutocomplete();
        this.exitHistoryBrowsing();
        this.lastAction = null;
        this.pushUndoSnapshot();
        const currentLine = this.state.lines[this.state.cursorLine] || "";
        const before = currentLine.slice(0, this.state.cursorCol);
        const after = currentLine.slice(this.state.cursorCol);
        // Split current line
        this.state.lines[this.state.cursorLine] = before;
        this.state.lines.splice(this.state.cursorLine + 1, 0, after);
        // Move cursor to start of new line
        this.state.cursorLine++;
        this.setCursorCol(0);
        if (this.onChange) {
            this.onChange(this.getText());
        }
    }
    shouldSubmitOnBackslashEnter(data, kb) {
        if (this.disableSubmit)
            return false;
        if (!matchesKey(data, "enter"))
            return false;
        const submitKeys = kb.getKeys("tui.input.submit");
        const hasShiftEnter = submitKeys.includes("shift+enter") || submitKeys.includes("shift+return");
        if (!hasShiftEnter)
            return false;
        const currentLine = this.state.lines[this.state.cursorLine] || "";
        return this.state.cursorCol > 0 && currentLine[this.state.cursorCol - 1] === "\\";
    }
    submitValue() {
        this.cancelAutocomplete();
        const result = this.expandPasteMarkers(this.state.lines.join("\n")).trim();
        // Resolve chips before clearing state so the host can attach the
        // referenced files; the text alone only carries the visible labels.
        const imageAttachments = this.getImageAttachments();
        const fileAttachments = this.getFileAttachments();
        this.state = { lines: [""], cursorLine: 0, cursorCol: 0 };
        this.pastes.clear();
        this.pasteCounter = 0;
        this.exitHistoryBrowsing();
        this.scrollOffset = 0;
        this.undoStack.clear();
        this.lastAction = null;
        if (this.onChange)
            this.onChange("");
        if (this.onSubmit)
            this.onSubmit(result, imageAttachments, fileAttachments);
    }
    handleBackspace() {
        this.exitHistoryBrowsing();
        this.lastAction = null;
        if (this.state.cursorCol > 0) {
            this.pushUndoSnapshot();
            // Delete grapheme before cursor (handles emojis, combining characters, etc.)
            let line = this.state.lines[this.state.cursorLine] || "";
            const beforeCursor = line.slice(0, this.state.cursorCol);
            // Find the last grapheme in the text before cursor
            const graphemes = [...this.segment(beforeCursor, "grapheme")];
            const lastGrapheme = graphemes[graphemes.length - 1];
            const graphemeLength = lastGrapheme ? lastGrapheme.segment.length : 1;
            const isPastedSegmented = PASTE_MARKER_SINGLE.exec(lastGrapheme.segment);
            if (isPastedSegmented) {
                // This contains the id part e.g 4 from [paste #4 +123 lines]
                const targetId = Number(isPastedSegmented[1]);
                this.pastes.delete(targetId);
                this.pasteCounter--;
                // Shift registry entries down in ascending id order, independent
                // of marker order in the text ([paste #3] becomes [paste #2] when
                // [paste #1] is removed).
                const higherIds = [...this.pastes.keys()].filter((id) => id > targetId).sort((a, b) => a - b);
                for (const id of higherIds) {
                    this.pastes.set(id - 1, this.pastes.get(id));
                    this.pastes.delete(id);
                }
                // Renumber markers with ids greater than the removed one.
                this.state.lines = this.state.lines.map((line) => line.replace(PASTE_MARKER_REGEX, (fullMatch, idGroup, suffixGroup) => {
                    const x = Number(idGroup);
                    if (x <= targetId)
                        return fullMatch;
                    return `[paste #${x - 1}${suffixGroup}]`;
                }));
            }
            line = this.state.lines[this.state.cursorLine] || "";
            const before = line.slice(0, this.state.cursorCol - graphemeLength);
            const after = line.slice(this.state.cursorCol);
            this.state.lines[this.state.cursorLine] = before + after;
            this.setCursorCol(this.state.cursorCol - graphemeLength);
        }
        else if (this.state.cursorLine > 0) {
            this.pushUndoSnapshot();
            // Merge with previous line
            const currentLine = this.state.lines[this.state.cursorLine] || "";
            const previousLine = this.state.lines[this.state.cursorLine - 1] || "";
            this.state.lines[this.state.cursorLine - 1] = previousLine + currentLine;
            this.state.lines.splice(this.state.cursorLine, 1);
            this.state.cursorLine--;
            this.setCursorCol(previousLine.length);
        }
        if (this.onChange) {
            this.onChange(this.getText());
        }
        // Update or re-trigger autocomplete after backspace
        if (this.autocompleteState) {
            this.updateAutocomplete();
        }
        else {
            // If autocomplete was cancelled (no matches), re-trigger if we're in a completable context
            const currentLine = this.state.lines[this.state.cursorLine] || "";
            const textBeforeCursor = currentLine.slice(0, this.state.cursorCol);
            // Slash command context
            if (this.isInSlashCommandContext(textBeforeCursor)) {
                this.tryTriggerAutocomplete();
            }
            // Symbol-based completion context like @, #, or provider triggers
            else if (this.autocompleteTriggerPattern.test(textBeforeCursor)) {
                this.tryTriggerAutocomplete();
            }
        }
    }
    /**
     * Set cursor column and clear preferredVisualCol.
     * Use this for all non-vertical cursor movements to reset sticky column behavior.
     */
    setCursorCol(col) {
        this.state.cursorCol = col;
        this.preferredVisualCol = null;
        this.snappedFromCursorCol = null;
    }
    /**
     * Move cursor to a target visual line, applying sticky column logic.
     * Shared by moveCursor() and pageScroll().
     */
    moveToVisualLine(visualLines, currentVisualLine, targetVisualLine) {
        const currentVL = visualLines[currentVisualLine];
        const targetVL = visualLines[targetVisualLine];
        if (!(currentVL && targetVL))
            return;
        // When the cursor was snapped to a segment start, resolve the pre-snap
        // position against the VL it belongs to. This gives the correct visual
        // column even after a resize reshuffles VLs.
        let currentVisualCol;
        if (this.snappedFromCursorCol !== null) {
            const vlIndex = this.findVisualLineAt(visualLines, currentVL.logicalLine, this.snappedFromCursorCol);
            currentVisualCol = this.snappedFromCursorCol - visualLines[vlIndex].startCol;
        }
        else {
            currentVisualCol = this.state.cursorCol - currentVL.startCol;
        }
        // For non-last segments, clamp to length-1 to stay within the segment
        const isLastSourceSegment = currentVisualLine === visualLines.length - 1 ||
            visualLines[currentVisualLine + 1]?.logicalLine !== currentVL.logicalLine;
        const sourceMaxVisualCol = isLastSourceSegment ? currentVL.length : Math.max(0, currentVL.length - 1);
        const isLastTargetSegment = targetVisualLine === visualLines.length - 1 ||
            visualLines[targetVisualLine + 1]?.logicalLine !== targetVL.logicalLine;
        const targetMaxVisualCol = isLastTargetSegment ? targetVL.length : Math.max(0, targetVL.length - 1);
        const moveToVisualCol = this.computeVerticalMoveColumn(currentVisualCol, sourceMaxVisualCol, targetMaxVisualCol);
        // Set cursor position
        this.state.cursorLine = targetVL.logicalLine;
        const targetCol = targetVL.startCol + moveToVisualCol;
        const logicalLine = this.state.lines[targetVL.logicalLine] || "";
        this.state.cursorCol = Math.min(targetCol, logicalLine.length);
        // Snap cursor to atomic segment boundary (e.g. paste markers)
        // so the cursor never lands in the middle of a multi-grapheme unit.
        // Single-grapheme segments don't need snapping.
        const segments = [...this.segment(logicalLine, "grapheme")];
        for (const seg of segments) {
            if (seg.index > this.state.cursorCol)
                break;
            if (seg.segment.length <= 1)
                continue;
            if (this.state.cursorCol < seg.index + seg.segment.length) {
                const isContinuation = seg.index < targetVL.startCol;
                const isMovingDown = targetVisualLine > currentVisualLine;
                if (isContinuation && isMovingDown) {
                    // The segment started on a previous visual line, and we
                    // already visited it on the way down. Skip all remaining
                    // continuation VLs and land on the first VL past it.
                    const segEnd = seg.index + seg.segment.length;
                    let next = targetVisualLine + 1;
                    while (next < visualLines.length &&
                        visualLines[next].logicalLine === targetVL.logicalLine &&
                        visualLines[next].startCol < segEnd) {
                        next++;
                    }
                    if (next < visualLines.length) {
                        this.moveToVisualLine(visualLines, currentVisualLine, next);
                        return;
                    }
                }
                // Snap to the start of the segment so it gets highlighted.
                // Store the pre-snap position so the next vertical move can
                // resolve it to the correct visual column.
                this.snappedFromCursorCol = this.state.cursorCol;
                this.state.cursorCol = seg.index;
                return;
            }
        }
        // No snap occurred – we moved out of the atomic segment.
        this.snappedFromCursorCol = null;
    }
    /**
     * Compute the target visual column for vertical cursor movement.
     * Implements the sticky column decision table:
     *
     * | P | S | T | U | Scenario                                             | Set Preferred | Move To     |
     * |---|---|---|---| ---------------------------------------------------- |---------------|-------------|
     * | 0 | * | 0 | - | Start nav, target fits                               | null          | current     |
     * | 0 | * | 1 | - | Start nav, target shorter                            | current       | target end  |
     * | 1 | 0 | 0 | 0 | Clamped, target fits preferred                       | null          | preferred   |
     * | 1 | 0 | 0 | 1 | Clamped, target longer but still can't fit preferred | keep          | target end  |
     * | 1 | 0 | 1 | - | Clamped, target even shorter                         | keep          | target end  |
     * | 1 | 1 | 0 | - | Rewrapped, target fits current                       | null          | current     |
     * | 1 | 1 | 1 | - | Rewrapped, target shorter than current               | current       | target end  |
     *
     * Where:
     * - P = preferred col is set
     * - S = cursor in middle of source line (not clamped to end)
     * - T = target line shorter than current visual col
     * - U = target line shorter than preferred col
     */
    computeVerticalMoveColumn(currentVisualCol, sourceMaxVisualCol, targetMaxVisualCol) {
        const hasPreferred = this.preferredVisualCol !== null; // P
        const cursorInMiddle = currentVisualCol < sourceMaxVisualCol; // S
        const targetTooShort = targetMaxVisualCol < currentVisualCol; // T
        if (!hasPreferred || cursorInMiddle) {
            if (targetTooShort) {
                // Cases 2 and 7
                this.preferredVisualCol = currentVisualCol;
                return targetMaxVisualCol;
            }
            // Cases 1 and 6
            this.preferredVisualCol = null;
            return currentVisualCol;
        }
        const targetCantFitPreferred = targetMaxVisualCol < this.preferredVisualCol; // U
        if (targetTooShort || targetCantFitPreferred) {
            // Cases 4 and 5
            return targetMaxVisualCol;
        }
        // Case 3
        const result = this.preferredVisualCol;
        this.preferredVisualCol = null;
        return result;
    }
    moveToLineStart() {
        this.lastAction = null;
        this.setCursorCol(0);
    }
    moveToLineEnd() {
        this.lastAction = null;
        const currentLine = this.state.lines[this.state.cursorLine] || "";
        this.setCursorCol(currentLine.length);
    }
    deleteToStartOfLine() {
        this.exitHistoryBrowsing();
        const currentLine = this.state.lines[this.state.cursorLine] || "";
        if (this.state.cursorCol > 0) {
            this.pushUndoSnapshot();
            // Calculate text to be deleted and save to kill ring (backward deletion = prepend)
            const deletedText = currentLine.slice(0, this.state.cursorCol);
            this.killRing.push(deletedText, { prepend: true, accumulate: this.lastAction === "kill" });
            this.lastAction = "kill";
            // Delete from start of line up to cursor
            this.state.lines[this.state.cursorLine] = currentLine.slice(this.state.cursorCol);
            this.setCursorCol(0);
        }
        else if (this.state.cursorLine > 0) {
            this.pushUndoSnapshot();
            // At start of line - merge with previous line, treating newline as deleted text
            this.killRing.push("\n", { prepend: true, accumulate: this.lastAction === "kill" });
            this.lastAction = "kill";
            const previousLine = this.state.lines[this.state.cursorLine - 1] || "";
            this.state.lines[this.state.cursorLine - 1] = previousLine + currentLine;
            this.state.lines.splice(this.state.cursorLine, 1);
            this.state.cursorLine--;
            this.setCursorCol(previousLine.length);
        }
        if (this.onChange) {
            this.onChange(this.getText());
        }
    }
    deleteToEndOfLine() {
        this.exitHistoryBrowsing();
        const currentLine = this.state.lines[this.state.cursorLine] || "";
        if (this.state.cursorCol < currentLine.length) {
            this.pushUndoSnapshot();
            // Calculate text to be deleted and save to kill ring (forward deletion = append)
            const deletedText = currentLine.slice(this.state.cursorCol);
            this.killRing.push(deletedText, { prepend: false, accumulate: this.lastAction === "kill" });
            this.lastAction = "kill";
            // Delete from cursor to end of line
            this.state.lines[this.state.cursorLine] = currentLine.slice(0, this.state.cursorCol);
        }
        else if (this.state.cursorLine < this.state.lines.length - 1) {
            this.pushUndoSnapshot();
            // At end of line - merge with next line, treating newline as deleted text
            this.killRing.push("\n", { prepend: false, accumulate: this.lastAction === "kill" });
            this.lastAction = "kill";
            const nextLine = this.state.lines[this.state.cursorLine + 1] || "";
            this.state.lines[this.state.cursorLine] = currentLine + nextLine;
            this.state.lines.splice(this.state.cursorLine + 1, 1);
        }
        if (this.onChange) {
            this.onChange(this.getText());
        }
    }
    deleteWordBackwards() {
        this.exitHistoryBrowsing();
        const currentLine = this.state.lines[this.state.cursorLine] || "";
        // If at start of line, behave like backspace at column 0 (merge with previous line)
        if (this.state.cursorCol === 0) {
            if (this.state.cursorLine > 0) {
                this.pushUndoSnapshot();
                // Treat newline as deleted text (backward deletion = prepend)
                this.killRing.push("\n", { prepend: true, accumulate: this.lastAction === "kill" });
                this.lastAction = "kill";
                const previousLine = this.state.lines[this.state.cursorLine - 1] || "";
                this.state.lines[this.state.cursorLine - 1] = previousLine + currentLine;
                this.state.lines.splice(this.state.cursorLine, 1);
                this.state.cursorLine--;
                this.setCursorCol(previousLine.length);
            }
        }
        else {
            this.pushUndoSnapshot();
            // Save lastAction before cursor movement (moveWordBackwards resets it)
            const wasKill = this.lastAction === "kill";
            const oldCursorCol = this.state.cursorCol;
            this.moveWordBackwards();
            const deleteFrom = this.state.cursorCol;
            this.setCursorCol(oldCursorCol);
            const deletedText = currentLine.slice(deleteFrom, this.state.cursorCol);
            this.killRing.push(deletedText, { prepend: true, accumulate: wasKill });
            this.lastAction = "kill";
            this.state.lines[this.state.cursorLine] =
                currentLine.slice(0, deleteFrom) + currentLine.slice(this.state.cursorCol);
            this.setCursorCol(deleteFrom);
        }
        if (this.onChange) {
            this.onChange(this.getText());
        }
    }
    deleteWordForward() {
        this.exitHistoryBrowsing();
        const currentLine = this.state.lines[this.state.cursorLine] || "";
        // If at end of line, merge with next line (delete the newline)
        if (this.state.cursorCol >= currentLine.length) {
            if (this.state.cursorLine < this.state.lines.length - 1) {
                this.pushUndoSnapshot();
                // Treat newline as deleted text (forward deletion = append)
                this.killRing.push("\n", { prepend: false, accumulate: this.lastAction === "kill" });
                this.lastAction = "kill";
                const nextLine = this.state.lines[this.state.cursorLine + 1] || "";
                this.state.lines[this.state.cursorLine] = currentLine + nextLine;
                this.state.lines.splice(this.state.cursorLine + 1, 1);
            }
        }
        else {
            this.pushUndoSnapshot();
            // Save lastAction before cursor movement (moveWordForwards resets it)
            const wasKill = this.lastAction === "kill";
            const oldCursorCol = this.state.cursorCol;
            this.moveWordForwards();
            const deleteTo = this.state.cursorCol;
            this.setCursorCol(oldCursorCol);
            const deletedText = currentLine.slice(this.state.cursorCol, deleteTo);
            this.killRing.push(deletedText, { prepend: false, accumulate: wasKill });
            this.lastAction = "kill";
            this.state.lines[this.state.cursorLine] =
                currentLine.slice(0, this.state.cursorCol) + currentLine.slice(deleteTo);
        }
        if (this.onChange) {
            this.onChange(this.getText());
        }
    }
    handleForwardDelete() {
        this.exitHistoryBrowsing();
        this.lastAction = null;
        const currentLine = this.state.lines[this.state.cursorLine] || "";
        if (this.state.cursorCol < currentLine.length) {
            this.pushUndoSnapshot();
            // Delete grapheme at cursor position (handles emojis, combining characters, etc.)
            const afterCursor = currentLine.slice(this.state.cursorCol);
            // Find the first grapheme at cursor
            const graphemes = [...this.segment(afterCursor, "grapheme")];
            const firstGrapheme = graphemes[0];
            const graphemeLength = firstGrapheme ? firstGrapheme.segment.length : 1;
            const before = currentLine.slice(0, this.state.cursorCol);
            const after = currentLine.slice(this.state.cursorCol + graphemeLength);
            this.state.lines[this.state.cursorLine] = before + after;
        }
        else if (this.state.cursorLine < this.state.lines.length - 1) {
            this.pushUndoSnapshot();
            // At end of line - merge with next line
            const nextLine = this.state.lines[this.state.cursorLine + 1] || "";
            this.state.lines[this.state.cursorLine] = currentLine + nextLine;
            this.state.lines.splice(this.state.cursorLine + 1, 1);
        }
        if (this.onChange) {
            this.onChange(this.getText());
        }
        // Update or re-trigger autocomplete after forward delete
        if (this.autocompleteState) {
            this.updateAutocomplete();
        }
        else {
            const currentLine = this.state.lines[this.state.cursorLine] || "";
            const textBeforeCursor = currentLine.slice(0, this.state.cursorCol);
            // Slash command context
            if (this.isInSlashCommandContext(textBeforeCursor)) {
                this.tryTriggerAutocomplete();
            }
            // Symbol-based completion context like @, #, or provider triggers
            else if (this.autocompleteTriggerPattern.test(textBeforeCursor)) {
                this.tryTriggerAutocomplete();
            }
        }
    }
    /**
     * Build a mapping from visual lines to logical positions.
     * Returns an array where each element represents a visual line with:
     * - logicalLine: index into this.state.lines
     * - startCol: starting column in the logical line
     * - length: length of this visual line segment
     */
    buildVisualLineMap(width) {
        const visualLines = [];
        for (let i = 0; i < this.state.lines.length; i++) {
            for (const chunk of this.visualChunks(i, width)) {
                visualLines.push({
                    logicalLine: i,
                    startCol: chunk.startIndex,
                    length: chunk.endIndex - chunk.startIndex,
                });
            }
        }
        return visualLines;
    }
    /**
     * Find the visual line index that contains the given logical position.
     */
    findVisualLineAt(visualLines, line, col) {
        for (let i = 0; i < visualLines.length; i++) {
            const vl = visualLines[i];
            if (!vl || vl.logicalLine !== line)
                continue;
            const offset = col - vl.startCol;
            // Cursor is in this segment if it's within range. For the last
            // segment of a logical line, cursor can be at length (end position)
            const isLastSegmentOfLine = i === visualLines.length - 1 || visualLines[i + 1]?.logicalLine !== vl.logicalLine;
            if (offset >= 0 && (offset < vl.length || (isLastSegmentOfLine && offset === vl.length))) {
                return i;
            }
        }
        return visualLines.length - 1;
    }
    /**
     * Find the visual line index for the current cursor position.
     */
    findCurrentVisualLine(visualLines) {
        return this.findVisualLineAt(visualLines, this.state.cursorLine, this.state.cursorCol);
    }
    moveCursor(deltaLine, deltaCol) {
        this.lastAction = null;
        const visualLines = this.buildVisualLineMap(this.lastWidth);
        const currentVisualLine = this.findCurrentVisualLine(visualLines);
        if (deltaLine !== 0) {
            const targetVisualLine = currentVisualLine + deltaLine;
            if (targetVisualLine >= 0 && targetVisualLine < visualLines.length) {
                this.moveToVisualLine(visualLines, currentVisualLine, targetVisualLine);
            }
        }
        if (deltaCol !== 0) {
            const currentLine = this.state.lines[this.state.cursorLine] || "";
            if (deltaCol > 0) {
                // Moving right - move by one grapheme (handles emojis, combining characters, etc.)
                if (this.state.cursorCol < currentLine.length) {
                    const afterCursor = currentLine.slice(this.state.cursorCol);
                    const graphemes = [...this.segment(afterCursor, "grapheme")];
                    const firstGrapheme = graphemes[0];
                    this.setCursorCol(this.state.cursorCol + (firstGrapheme ? firstGrapheme.segment.length : 1));
                }
                else if (this.state.cursorLine < this.state.lines.length - 1) {
                    // Wrap to start of next logical line
                    this.state.cursorLine++;
                    this.setCursorCol(0);
                }
                else {
                    // At end of last line - can't move, but set preferredVisualCol for up/down navigation
                    const currentVL = visualLines[currentVisualLine];
                    if (currentVL) {
                        this.preferredVisualCol = this.state.cursorCol - currentVL.startCol;
                    }
                }
            }
            else {
                // Moving left - move by one grapheme (handles emojis, combining characters, etc.)
                if (this.state.cursorCol > 0) {
                    const beforeCursor = currentLine.slice(0, this.state.cursorCol);
                    const graphemes = [...this.segment(beforeCursor, "grapheme")];
                    const lastGrapheme = graphemes[graphemes.length - 1];
                    this.setCursorCol(this.state.cursorCol - (lastGrapheme ? lastGrapheme.segment.length : 1));
                }
                else if (this.state.cursorLine > 0) {
                    // Wrap to end of previous logical line
                    this.state.cursorLine--;
                    const prevLine = this.state.lines[this.state.cursorLine] || "";
                    this.setCursorCol(prevLine.length);
                }
            }
        }
        // Keep an open autocomplete picker in sync with the new cursor
        // position: cursor movement changes the text before the cursor, so a
        // picker computed for the old position is stale. Re-query so it
        // refreshes — or closes when the new position yields no suggestions —
        // mirroring insertCharacter()/handleBackspace(). Without this, arrowing
        // left from `/cmd ` back into the command name leaves the argument
        // picker showing against a `/cmd` prefix (and a Tab there would
        // concatenate the stale suggestion onto the partial command name).
        if (this.autocompleteState) {
            this.updateAutocomplete();
        }
    }
    /**
     * Scroll by a page (direction: -1 for up, 1 for down).
     * Moves cursor by the page size while keeping it in bounds.
     */
    pageScroll(direction) {
        this.lastAction = null;
        const terminalRows = this.tui.terminal.rows;
        const pageSize = Math.max(5, Math.floor(terminalRows * 0.3));
        const visualLines = this.buildVisualLineMap(this.lastWidth);
        const currentVisualLine = this.findCurrentVisualLine(visualLines);
        const targetVisualLine = Math.max(0, Math.min(visualLines.length - 1, currentVisualLine + direction * pageSize));
        this.moveToVisualLine(visualLines, currentVisualLine, targetVisualLine);
    }
    moveWordBackwards() {
        this.lastAction = null;
        const currentLine = this.state.lines[this.state.cursorLine] || "";
        // If at start of line, move to end of previous line
        if (this.state.cursorCol === 0) {
            if (this.state.cursorLine > 0) {
                this.state.cursorLine--;
                const prevLine = this.state.lines[this.state.cursorLine] || "";
                this.setCursorCol(prevLine.length);
            }
            return;
        }
        this.setCursorCol(findWordBackward(currentLine, this.state.cursorCol, {
            segment: (text) => this.segment(text, "word"),
            isAtomicSegment: isPasteMarker,
        }));
    }
    /**
     * Yank (paste) the most recent kill ring entry at cursor position.
     */
    yank() {
        if (this.killRing.length === 0)
            return;
        this.pushUndoSnapshot();
        const text = this.killRing.peek();
        this.insertYankedText(text);
        this.lastAction = "yank";
    }
    /**
     * Cycle through kill ring (only works immediately after yank or yank-pop).
     * Replaces the last yanked text with the previous entry in the ring.
     */
    yankPop() {
        // Only works if we just yanked and have more than one entry
        if (this.lastAction !== "yank" || this.killRing.length <= 1)
            return;
        this.pushUndoSnapshot();
        // Delete the previously yanked text (still at end of ring before rotation)
        this.deleteYankedText();
        // Rotate the ring: move end to front
        this.killRing.rotate();
        // Insert the new most recent entry (now at end after rotation)
        const text = this.killRing.peek();
        this.insertYankedText(text);
        this.lastAction = "yank";
    }
    /**
     * Insert text at cursor position (used by yank operations).
     */
    insertYankedText(text) {
        this.exitHistoryBrowsing();
        const lines = text.split("\n");
        if (lines.length === 1) {
            // Single line - insert at cursor
            const currentLine = this.state.lines[this.state.cursorLine] || "";
            const before = currentLine.slice(0, this.state.cursorCol);
            const after = currentLine.slice(this.state.cursorCol);
            this.state.lines[this.state.cursorLine] = before + text + after;
            this.setCursorCol(this.state.cursorCol + text.length);
        }
        else {
            // Multi-line insert
            const currentLine = this.state.lines[this.state.cursorLine] || "";
            const before = currentLine.slice(0, this.state.cursorCol);
            const after = currentLine.slice(this.state.cursorCol);
            // First line merges with text before cursor
            this.state.lines[this.state.cursorLine] = before + (lines[0] || "");
            // Insert middle lines
            for (let i = 1; i < lines.length - 1; i++) {
                this.state.lines.splice(this.state.cursorLine + i, 0, lines[i] || "");
            }
            // Last line merges with text after cursor
            const lastLineIndex = this.state.cursorLine + lines.length - 1;
            this.state.lines.splice(lastLineIndex, 0, (lines[lines.length - 1] || "") + after);
            // Update cursor position
            this.state.cursorLine = lastLineIndex;
            this.setCursorCol((lines[lines.length - 1] || "").length);
        }
        if (this.onChange) {
            this.onChange(this.getText());
        }
    }
    /**
     * Delete the previously yanked text (used by yank-pop).
     * The yanked text is derived from killRing[end] since it hasn't been rotated yet.
     */
    deleteYankedText() {
        const yankedText = this.killRing.peek();
        if (!yankedText)
            return;
        const yankLines = yankedText.split("\n");
        if (yankLines.length === 1) {
            // Single line - delete backward from cursor
            const currentLine = this.state.lines[this.state.cursorLine] || "";
            const deleteLen = yankedText.length;
            const before = currentLine.slice(0, this.state.cursorCol - deleteLen);
            const after = currentLine.slice(this.state.cursorCol);
            this.state.lines[this.state.cursorLine] = before + after;
            this.setCursorCol(this.state.cursorCol - deleteLen);
        }
        else {
            // Multi-line delete - cursor is at end of last yanked line
            const startLine = this.state.cursorLine - (yankLines.length - 1);
            const startCol = (this.state.lines[startLine] || "").length - (yankLines[0] || "").length;
            // Get text after cursor on current line
            const afterCursor = (this.state.lines[this.state.cursorLine] || "").slice(this.state.cursorCol);
            // Get text before yank start position
            const beforeYank = (this.state.lines[startLine] || "").slice(0, startCol);
            // Remove all lines from startLine to cursorLine and replace with merged line
            this.state.lines.splice(startLine, yankLines.length, beforeYank + afterCursor);
            // Update cursor
            this.state.cursorLine = startLine;
            this.setCursorCol(startCol);
        }
        if (this.onChange) {
            this.onChange(this.getText());
        }
    }
    pushUndoSnapshot() {
        this.undoStack.push({ state: this.state, pastes: this.pastes, pasteCounter: this.pasteCounter, fileAttachments: this.fileAttachments });
    }
    undo() {
        this.exitHistoryBrowsing();
        const snapshot = this.undoStack.pop();
        if (!snapshot)
            return;
        Object.assign(this.state, snapshot.state);
        this.pastes = snapshot.pastes;
        this.pasteCounter = snapshot.pasteCounter;
        // File attachment payloads are part of undoable state so a deleted chip
        // (and its path) comes back with the restored marker text.
        if (snapshot.fileAttachments) {
            this.fileAttachments = snapshot.fileAttachments;
        }
        this.lastAction = null;
        this.preferredVisualCol = null;
        if (this.onChange) {
            this.onChange(this.getText());
        }
    }
    /**
     * Jump to the first occurrence of a character in the specified direction.
     * Multi-line search. Case-sensitive. Skips the current cursor position.
     */
    jumpToChar(char, direction) {
        this.lastAction = null;
        const isForward = direction === "forward";
        const lines = this.state.lines;
        const end = isForward ? lines.length : -1;
        const step = isForward ? 1 : -1;
        for (let lineIdx = this.state.cursorLine; lineIdx !== end; lineIdx += step) {
            const line = lines[lineIdx] || "";
            const isCurrentLine = lineIdx === this.state.cursorLine;
            // Current line: start after/before cursor; other lines: search full line
            const searchFrom = isCurrentLine
                ? isForward
                    ? this.state.cursorCol + 1
                    : this.state.cursorCol - 1
                : undefined;
            const idx = isForward ? line.indexOf(char, searchFrom) : line.lastIndexOf(char, searchFrom);
            if (idx !== -1) {
                this.state.cursorLine = lineIdx;
                this.setCursorCol(idx);
                return;
            }
        }
        // No match found - cursor stays in place
    }
    moveWordForwards() {
        this.lastAction = null;
        const currentLine = this.state.lines[this.state.cursorLine] || "";
        // If at end of line, move to start of next line
        if (this.state.cursorCol >= currentLine.length) {
            if (this.state.cursorLine < this.state.lines.length - 1) {
                this.state.cursorLine++;
                this.setCursorCol(0);
            }
            return;
        }
        this.setCursorCol(findWordForward(currentLine, this.state.cursorCol, {
            segment: (text) => this.segment(text, "word"),
            isAtomicSegment: isPasteMarker,
        }));
    }
    // Slash menu only allowed on the first line of the editor
    isSlashMenuAllowed() {
        return this.state.cursorLine === 0;
    }
    // Helper method to check if cursor is at start of message (for slash command detection)
    isAtStartOfMessage() {
        if (!this.isSlashMenuAllowed())
            return false;
        const currentLine = this.state.lines[this.state.cursorLine] || "";
        const beforeCursor = currentLine.slice(0, this.state.cursorCol);
        return beforeCursor.trim() === "" || beforeCursor.trim() === "/";
    }
    isInSlashCommandContext(textBeforeCursor) {
        return this.isSlashMenuAllowed() && textBeforeCursor.trimStart().startsWith("/");
    }
    // Autocomplete methods
    /**
     * Find the best autocomplete item index for the given prefix.
     * Returns -1 if no match is found.
     *
     * Match priority:
     * 1. Exact match (prefix === item.value) -> always selected
     * 2. Prefix match -> first item whose value starts with prefix
     * 3. No match -> -1 (keep default highlight)
     *
     * Matching is case-sensitive and checks item.value only.
     */
    getBestAutocompleteMatchIndex(items, prefix) {
        if (!prefix)
            return -1;
        let firstPrefixIndex = -1;
        for (let i = 0; i < items.length; i++) {
            const value = items[i].value;
            if (value === prefix) {
                return i; // Exact match always wins
            }
            if (firstPrefixIndex === -1 && value.startsWith(prefix)) {
                firstPrefixIndex = i;
            }
        }
        return firstPrefixIndex;
    }
    createAutocompleteList(prefix, items) {
        const layout = prefix.startsWith("/") ? SLASH_COMMAND_SELECT_LIST_LAYOUT : undefined;
        const list = new SelectList(items, this.autocompleteMaxVisible, this.theme.selectList, layout);
        list.onSelect = (selected) => {
            if (!this.autocompleteProvider)
                return;
            this.pushUndoSnapshot();
            this.lastAction = null;
            const result = this.autocompleteProvider.applyCompletion(this.state.lines, this.state.cursorLine, this.state.cursorCol, selected, this.autocompletePrefix);
            this.state.lines = result.lines;
            this.state.cursorLine = result.cursorLine;
            this.setCursorCol(result.cursorCol);
            this.cancelAutocomplete();
            this.onChange?.(this.getText());
        };
        return list;
    }
    tryTriggerAutocomplete(explicitTab = false) {
        this.requestAutocomplete({ force: false, explicitTab });
    }
    handleTabCompletion() {
        if (!this.autocompleteProvider)
            return;
        const currentLine = this.state.lines[this.state.cursorLine] || "";
        const beforeCursor = currentLine.slice(0, this.state.cursorCol);
        if (this.isInSlashCommandContext(beforeCursor) && !beforeCursor.trimStart().includes(" ")) {
            this.handleSlashCommandCompletion();
        }
        else {
            this.forceFileAutocomplete(true);
        }
    }
    handleSlashCommandCompletion() {
        this.requestAutocomplete({ force: false, explicitTab: true });
    }
    forceFileAutocomplete(explicitTab = false) {
        this.requestAutocomplete({ force: true, explicitTab });
    }
    requestAutocomplete(options) {
        if (!this.autocompleteProvider)
            return;
        if (options.force) {
            const shouldTrigger = !this.autocompleteProvider.shouldTriggerFileCompletion ||
                this.autocompleteProvider.shouldTriggerFileCompletion(this.state.lines, this.state.cursorLine, this.state.cursorCol);
            if (!shouldTrigger) {
                return;
            }
        }
        this.cancelAutocompleteRequest();
        const startToken = ++this.autocompleteStartToken;
        const debounceMs = this.getAutocompleteDebounceMs(options);
        if (debounceMs > 0) {
            this.autocompleteDebounceTimer = setTimeout(() => {
                this.autocompleteDebounceTimer = undefined;
                void this.startAutocompleteRequest(startToken, options);
            }, debounceMs);
            return;
        }
        void this.startAutocompleteRequest(startToken, options);
    }
    async startAutocompleteRequest(startToken, options) {
        const previousTask = this.autocompleteRequestTask;
        this.autocompleteRequestTask = (async () => {
            await previousTask;
            if (startToken !== this.autocompleteStartToken || !this.autocompleteProvider) {
                return;
            }
            const controller = new AbortController();
            this.autocompleteAbort = controller;
            const requestId = ++this.autocompleteRequestId;
            const snapshotText = this.getText();
            const snapshotLine = this.state.cursorLine;
            const snapshotCol = this.state.cursorCol;
            await this.runAutocompleteRequest(requestId, controller, snapshotText, snapshotLine, snapshotCol, options);
        })();
        await this.autocompleteRequestTask;
    }
    setAutocompleteTriggerCharacters(triggerCharacters) {
        const next = [...DEFAULT_AUTOCOMPLETE_TRIGGER_CHARACTERS];
        for (const character of triggerCharacters) {
            if (character.length !== 1 || character === "/" || isWhitespaceChar(character) || next.includes(character)) {
                continue;
            }
            next.push(character);
        }
        this.autocompleteTriggerCharacters = next;
        this.autocompleteTriggerPattern = buildTriggerPattern(next);
        this.autocompleteDebouncePattern = buildDebouncePattern(next);
    }
    getAutocompleteDebounceMs(options) {
        if (options.explicitTab || options.force) {
            return 0;
        }
        const currentLine = this.state.lines[this.state.cursorLine] || "";
        const textBeforeCursor = currentLine.slice(0, this.state.cursorCol);
        return this.autocompleteDebouncePattern.test(textBeforeCursor) ? ATTACHMENT_AUTOCOMPLETE_DEBOUNCE_MS : 0;
    }
    async runAutocompleteRequest(requestId, controller, snapshotText, snapshotLine, snapshotCol, options) {
        if (!this.autocompleteProvider)
            return;
        const suggestions = await this.autocompleteProvider.getSuggestions(this.state.lines, this.state.cursorLine, this.state.cursorCol, { signal: controller.signal, force: options.force });
        if (!this.isAutocompleteRequestCurrent(requestId, controller, snapshotText, snapshotLine, snapshotCol)) {
            return;
        }
        this.autocompleteAbort = undefined;
        if (!suggestions || !Array.isArray(suggestions.items) || suggestions.items.length === 0) {
            this.cancelAutocomplete();
            this.tui.requestRender();
            return;
        }
        if (options.force && options.explicitTab && suggestions.items.length === 1) {
            const item = suggestions.items[0];
            this.pushUndoSnapshot();
            this.lastAction = null;
            const result = this.autocompleteProvider.applyCompletion(this.state.lines, this.state.cursorLine, this.state.cursorCol, item, suggestions.prefix);
            this.state.lines = result.lines;
            this.state.cursorLine = result.cursorLine;
            this.setCursorCol(result.cursorCol);
            if (this.onChange)
                this.onChange(this.getText());
            this.tui.requestRender();
            return;
        }
        this.applyAutocompleteSuggestions(suggestions, options.force ? "force" : "regular");
        this.tui.requestRender();
    }
    isAutocompleteRequestCurrent(requestId, controller, snapshotText, snapshotLine, snapshotCol) {
        return (!controller.signal.aborted &&
            requestId === this.autocompleteRequestId &&
            this.getText() === snapshotText &&
            this.state.cursorLine === snapshotLine &&
            this.state.cursorCol === snapshotCol);
    }
    applyAutocompleteSuggestions(suggestions, state) {
        this.autocompletePrefix = suggestions.prefix;
        this.autocompleteList = this.createAutocompleteList(suggestions.prefix, suggestions.items);
        const bestMatchIndex = this.getBestAutocompleteMatchIndex(suggestions.items, suggestions.prefix);
        if (bestMatchIndex >= 0) {
            this.autocompleteList.setSelectedIndex(bestMatchIndex);
        }
        this.autocompleteState = state;
    }
    cancelAutocompleteRequest() {
        this.autocompleteStartToken += 1;
        if (this.autocompleteDebounceTimer) {
            clearTimeout(this.autocompleteDebounceTimer);
            this.autocompleteDebounceTimer = undefined;
        }
        this.autocompleteAbort?.abort();
        this.autocompleteAbort = undefined;
    }
    clearAutocompleteUi() {
        this.autocompleteState = null;
        this.autocompleteList = undefined;
        this.autocompletePrefix = "";
    }
    cancelAutocomplete() {
        this.cancelAutocompleteRequest();
        this.clearAutocompleteUi();
    }
    isShowingAutocomplete() {
        return this.autocompleteState !== null;
    }
    updateAutocomplete() {
        if (!this.autocompleteState || !this.autocompleteProvider)
            return;
        this.requestAutocomplete({ force: this.autocompleteState === "force", explicitTab: false });
    }
}
//# sourceMappingURL=editor.js.map