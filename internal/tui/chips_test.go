package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParsePastedPathsAcceptsOnlyPathPastes(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		input string
		want  []string
		ok    bool
	}{
		{name: "single absolute", input: "/tmp/notes.md", want: []string{"/tmp/notes.md"}, ok: true},
		{name: "two lines", input: "/tmp/one.txt\n/tmp/two.txt", want: []string{"/tmp/one.txt", "/tmp/two.txt"}, ok: true},
		{name: "crlf", input: "/tmp/one.txt\r\n/tmp/two.txt\r\n", want: []string{"/tmp/one.txt", "/tmp/two.txt"}, ok: true},
		{name: "tilde", input: "~/notes.md", want: []string{filepath.Join(home, "notes.md")}, ok: true},
		{name: "quoted spaces", input: `'/tmp/My File.txt'`, want: []string{"/tmp/My File.txt"}, ok: true},
		{name: "two quoted", input: `'/tmp/My File.txt' "/tmp/other file.md"`, want: []string{"/tmp/My File.txt", "/tmp/other file.md"}, ok: true},
		{name: "bare path with spaces and extension", input: "/tmp/Screenshot 2026.png", want: []string{"/tmp/Screenshot 2026.png"}, ok: true},
		{name: "machine facing marker is not a path", input: "[Image: copied shot.png]", ok: false},
		{name: "prose", input: "please look at /tmp/report.txt", ok: false},
		{name: "relative", input: "notes.md", ok: false},
		{name: "url", input: "https://example.com/x.png", ok: false},
		{name: "unterminated quote", input: "'/tmp/one.txt", ok: false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, ok := ParsePastedPaths(testCase.input, "/work")
			if ok != testCase.ok {
				t.Fatalf("ok = %v, want %v (got %v)", ok, testCase.ok, got)
			}
			if !testCase.ok {
				return
			}
			if len(got) != len(testCase.want) {
				t.Fatalf("paths = %v, want %v", got, testCase.want)
			}
			for index := range got {
				if got[index] != testCase.want[index] {
					t.Fatalf("paths = %v, want %v", got, testCase.want)
				}
			}
		})
	}
}

func TestInsertAttachmentChipsLabelsImagesAndFiles(t *testing.T) {
	marker, paths, ok := InsertAttachmentChips("/tmp/My Shot.png\n/tmp/notes.md", "/work")
	if !ok {
		t.Fatal("a path-only paste should become chips")
	}
	if marker != "[Image: My Shot.png] [File: notes.md] " {
		t.Fatalf("marker = %q", marker)
	}
	if paths["[Image: My Shot.png]"] != "/tmp/My Shot.png" || paths["[File: notes.md]"] != "/tmp/notes.md" {
		t.Fatalf("paths = %v", paths)
	}
}

func TestInsertAttachmentChipsNormalizesScreenshotSpaces(t *testing.T) {
	// macOS screenshot names carry a narrow no-break space before am/pm.
	marker, _, ok := InsertAttachmentChips("/tmp/Screenshot 2026-09-21 at 12.21.16\u202fpm.png", "/work")
	if !ok {
		t.Fatal("a screenshot path should become a chip")
	}
	if marker != "[Image: Screenshot 2026-09-21 at 12.21.16 pm.png] " {
		t.Fatalf("marker = %q", marker)
	}
}

func TestInsertAttachmentChipsRefusesDuplicateBasenames(t *testing.T) {
	if _, _, ok := InsertAttachmentChips("/tmp/a/notes.md\n/tmp/b/notes.md", "/work"); ok {
		t.Fatal("ambiguous basenames should stay plain text")
	}
}

func TestStyleAttachmentMarkersPaintsOnlyChips(t *testing.T) {
	theme := NewOneDarkTheme(ColorTrue)
	styled := StyleAttachmentMarkers(theme, "see [Image: shot.png] and [File: notes.md] now", "")
	plainPrefix := theme.FG("toolTitle", "[Image: shot.png]")
	if !strings.Contains(styled, plainPrefix) {
		t.Fatalf("image chip not styled: %q", styled)
	}
	if !strings.Contains(styled, theme.FG("toolTitle", "[File: notes.md]")) {
		t.Fatalf("file chip not styled: %q", styled)
	}
	if strings.Contains(styled, theme.FG("toolTitle", "see")) {
		t.Fatalf("prose should not be styled: %q", styled)
	}
}

func TestExpandAttachmentPathsKeepsVisibleMarkers(t *testing.T) {
	paths := map[string]string{"[Image: shot.png]": "/tmp/shot.png"}
	expanded := ExpandAttachmentPaths("look at [Image: shot.png] and [Image: typed.png]", paths)
	if expanded != "look at /tmp/shot.png and [Image: typed.png]" {
		t.Fatalf("expanded = %q", expanded)
	}
}

func TestPromptCardPaintsAttachmentChipsYellow(t *testing.T) {
	theme := CurrentTheme()
	card := strings.Join(renderPromptCard("look at [Image: shot.png] and [File: notes.md]", 60, nil), "\n")
	if !strings.Contains(card, theme.FG("toolTitle", "[Image: shot.png]")+theme.FGPrefix("userMessageText")) {
		t.Fatalf("image chip not painted in the prompt card: %q", card)
	}
	if !strings.Contains(card, theme.FG("toolTitle", "[File: notes.md]")+theme.FGPrefix("userMessageText")) {
		t.Fatalf("file chip not painted in the prompt card: %q", card)
	}
	// The text after a chip keeps the prompt colour rather than falling back to
	// the terminal default.
	if !strings.Contains(stripANSI(card), "look at [Image: shot.png] and [File: notes.md]") {
		t.Fatalf("prompt text changed:\n%s", stripANSI(card))
	}
}
