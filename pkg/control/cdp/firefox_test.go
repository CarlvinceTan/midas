package cdp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFirefoxProfilesAreReadFromProfilesIni(t *testing.T) {
	home := t.TempDir()
	base := filepath.Join(home, "Library", "Application Support", "Firefox")
	profile := filepath.Join(base, "Profiles", "abc.default")
	if err := os.MkdirAll(profile, 0o700); err != nil {
		t.Fatal(err)
	}
	ini := `[Profile0]
Name=default
IsRelative=1
Path=Profiles/abc.default
Default=1
`
	if err := os.WriteFile(filepath.Join(base, "profiles.ini"), []byte(ini), 0o600); err != nil {
		t.Fatal(err)
	}
	profiles, err := FirefoxProfiles(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 1 || profiles[0] != profile {
		t.Fatalf("profiles = %#v", profiles)
	}
	// A machine without Firefox reports nothing rather than failing.
	if profiles, err := FirefoxProfiles(t.TempDir()); err != nil || len(profiles) != 0 {
		t.Fatalf("empty = %#v, %v", profiles, err)
	}
}

func TestFirefoxEndpointPreferencesAreManagedNotAppended(t *testing.T) {
	profile := t.TempDir()
	userJS := filepath.Join(profile, "user.js")
	// A user's own preferences must survive untouched.
	if err := os.WriteFile(userJS, []byte("user_pref(\"toolkit.telemetry.enabled\", false);\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := EnableFirefoxEndpoint(profile, 9333); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(userJS)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "user_pref(\"toolkit.telemetry.enabled\", false);") {
		t.Fatalf("an existing preference was lost:\n%s", text)
	}
	for _, want := range []string{
		`user_pref("devtools.debugger.remote-enabled", true);`,
		`user_pref("devtools.debugger.remote-port", 9333);`,
		`user_pref("devtools.debugger.prompt-connection", false);`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("user.js missing %q:\n%s", want, text)
		}
	}
	// Enabling twice must not duplicate the block.
	if err := EnableFirefoxEndpoint(profile, 9333); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(userJS)
	if strings.Count(string(data), "devtools.debugger.remote-enabled") != 1 {
		t.Fatalf("the block was duplicated:\n%s", data)
	}
	// Changing the port updates the same block.
	if err := EnableFirefoxEndpoint(profile, 9444); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(userJS)
	if strings.Contains(string(data), "9333") || !strings.Contains(string(data), "9444") {
		t.Fatalf("port not updated:\n%s", data)
	}
	// Disabling removes only Midas's block.
	if err := DisableFirefoxEndpoint(profile); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(userJS)
	text = string(data)
	if strings.Contains(text, "devtools.debugger") {
		t.Fatalf("the block survived disabling:\n%s", text)
	}
	if !strings.Contains(text, "toolkit.telemetry.enabled") {
		t.Fatalf("disabling removed the user's own preference:\n%s", text)
	}
	// With nothing left of ours and no user content, the file goes away.
	empty := t.TempDir()
	if err := EnableFirefoxEndpoint(empty, 0); err != nil {
		t.Fatal(err)
	}
	if err := DisableFirefoxEndpoint(empty); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(empty, "user.js")); !os.IsNotExist(err) {
		t.Fatalf("a file we created was left behind: %v", err)
	}
	if err := EnableFirefoxEndpoint(filepath.Join(t.TempDir(), "nope"), 0); err == nil {
		t.Fatal("a directory that is not a profile was accepted")
	}
}
