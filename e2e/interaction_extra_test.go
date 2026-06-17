package e2e

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/PolymuxOrg/midas/tools"
)

func TestSetInputFiles(t *testing.T) {
	page := newPage(t)
	gotoPath(t, page, "/upload.html")

	dir := t.TempDir()
	file := filepath.Join(dir, "hello.txt")
	if err := os.WriteFile(file, []byte("contents"), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	if err := page.Locator("#file").SetInputFiles(testCtx(t), file); err != nil {
		t.Fatalf("set input files: %v", err)
	}
	got := evalString(t, page, "document.getElementById('uploaded').textContent")
	if got != "uploaded:hello.txt" {
		t.Errorf("uploaded text = %q, want 'uploaded:hello.txt'", got)
	}
}

func TestSetInputFilesMultiple(t *testing.T) {
	page := newPage(t)
	h := requireHarness(t)
	h.server.SetContent(t, "/upload-multi.html", `<!DOCTYPE html>
<title>Upload multi</title>
<input id="file" type="file" multiple>
<div id="count"></div>
<script>
  document.getElementById('file').addEventListener('change', e => {
    document.getElementById('count').textContent = String(e.target.files.length);
  });
</script>`)
	gotoPath(t, page, "/upload-multi.html")

	dir := t.TempDir()
	var files []string
	for _, name := range []string{"a.txt", "b.txt"} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(name), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		files = append(files, p)
	}

	if err := page.Locator("#file").SetInputFiles(testCtx(t), files...); err != nil {
		t.Fatalf("set input files: %v", err)
	}
	if got := evalString(t, page, "document.getElementById('count').textContent"); got != "2" {
		t.Errorf("file count = %q, want 2", got)
	}
}

func TestDragAndDropTool(t *testing.T) {
	page := newPage(t)
	h := requireHarness(t)
	// Pointer-based draggable: dragging #src onto #dst records the drop.
	h.server.SetContent(t, "/dnd.html", `<!DOCTYPE html>
<title>DnD</title>
<style>#src,#dst{width:120px;height:120px;display:inline-block}#src{background:#8cf}#dst{background:#fc8}</style>
<div id="src">source</div>
<div id="dst">target</div>
<div id="result">none</div>
<script>
  const dst = document.getElementById('dst');
  let down = false;
  document.getElementById('src').addEventListener('pointerdown', () => down = true);
  dst.addEventListener('pointerup', () => { if (down) document.getElementById('result').textContent = 'dropped'; });
  dst.addEventListener('pointerenter', () => {}); // ensure events flow
  window.addEventListener('pointerup', () => down = false);
</script>`)
	gotoPath(t, page, "/dnd.html")

	svc := tools.NewService(h.bctx)
	if _, err := svc.Execute(testCtx(t), "drag_and_drop", map[string]any{
		"from_selector": "#src",
		"to_selector":   "#dst",
		"steps":         8,
	}); err != nil {
		t.Fatalf("drag_and_drop tool: %v", err)
	}
	if got := evalString(t, page, "document.getElementById('result').textContent"); got != "dropped" {
		t.Errorf("drag result = %q, want 'dropped'", got)
	}
}

func TestSelectOptionMultiple(t *testing.T) {
	page := newPage(t)
	gotoPath(t, page, "/select.html")

	if err := page.Locator("#multi").SelectOption(testCtx(t), "red", "blue"); err != nil {
		t.Fatalf("select multiple: %v", err)
	}
	var selected []string
	if err := page.Evaluate(testCtx(t),
		`Array.from(document.getElementById('multi').selectedOptions).map(o => o.value)`,
		&selected); err != nil {
		t.Fatalf("read selected: %v", err)
	}
	if len(selected) != 2 || !contains(selected, "red") || !contains(selected, "blue") {
		t.Errorf("selected = %v, want [red blue]", selected)
	}
}

func TestAddInitScriptRunsOnNewDocument(t *testing.T) {
	page := newPage(t)

	if err := page.AddInitScript(testCtx(t), "window.__injected = 'present';"); err != nil {
		t.Fatalf("add init script: %v", err)
	}
	// The init script must run before page scripts on the next navigation.
	gotoPath(t, page, "/empty.html")
	if got := evalString(t, page, "window.__injected || ''"); got != "present" {
		t.Errorf("window.__injected = %q, want 'present' (init script did not run)", got)
	}
}
