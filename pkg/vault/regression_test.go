package vault

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	wrappers "github.com/tobischo/gokeepasslib/v3/wrappers"
)

// TestPutKeepsFieldsItWasNotGiven pins WriteOptions' promise: adding a TOTP seed
// to an existing entry leaves its account details and password alone. The fields
// used to be overwritten with empty strings.
func TestPutKeepsFieldsItWasNotGiven(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.kdbx")
	vault, err := Create(path, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	secret := "hunter2"
	if _, err := vault.Put(WriteOptions{
		Title: "GitHub", Username: "carl", URL: "https://github.com",
		Notes: "personal account", Password: &secret,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := vault.Put(WriteOptions{Title: "GitHub", TOTPSeed: "JBSWY3DPEHPK3PXP"}); err != nil {
		t.Fatal(err)
	}
	entry, _, ok := vault.Find("GitHub")
	if !ok {
		t.Fatal("entry disappeared")
	}
	if entry.Username != "carl" || entry.URL != "https://github.com" {
		t.Fatalf("account details were cleared: %#v", entry)
	}
	password, err := vault.Password("GitHub")
	if err != nil || password != secret {
		t.Fatalf("password after adding a seed = %q, %v", password, err)
	}
	if !entry.HasTOTP {
		t.Fatalf("seed was not stored: %#v", entry)
	}
	// A caller that does pass a field still replaces it.
	if _, err := vault.Put(WriteOptions{Title: "GitHub", Username: "carl2"}); err != nil {
		t.Fatal(err)
	}
	entry, _, _ = vault.Find("GitHub")
	if entry.Username != "carl2" {
		t.Fatalf("username was not updated: %#v", entry)
	}
}

// TestCreateMakesTheDirectoryItWasGiven: a user may name a vault in a directory
// that does not exist yet.
func TestCreateMakesTheDirectoryItWasGiven(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new", "nested", "vault.kdbx")
	vault, err := Create(path, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := vault.Put(WriteOptions{Title: "Entry", Password: pointer("secret")}); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := reopened.Find("Entry"); !ok {
		t.Fatal("the entry did not survive the round trip")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("vault mode = %o", mode)
	}
}

func pointer(value string) *string { return &value }

// TestTOTPHonoursTheAuthenticatorSettings: an otpauth URL may name a period and a
// digit count, and the generated code has to match, or every code is wrong.
func TestTOTPHonoursTheAuthenticatorSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.kdbx")
	vault, err := Create(path, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	// RFC 6238 test vector: SHA1, 8 digits, period 30.
	if _, err := vault.Put(WriteOptions{
		Title:    "EightDigits",
		TOTPSeed: "otpauth://totp/Example:carl?secret=GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ&digits=8&period=30",
	}); err != nil {
		t.Fatal(err)
	}
	code, remaining, err := vault.TOTP("EightDigits", time.Unix(59, 0))
	if err != nil {
		t.Fatal(err)
	}
	if code != "94287082" {
		t.Fatalf("code = %q, want 94287082", code)
	}
	if remaining <= 0 || remaining > 30*time.Second {
		t.Fatalf("remaining = %v", remaining)
	}
	// The period is honoured when it is not the default.
	if _, err := vault.Put(WriteOptions{
		Title:    "SixtySeconds",
		TOTPSeed: "otpauth://totp/Slow?secret=GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ&period=60",
	}); err != nil {
		t.Fatal(err)
	}
	_, remaining, err = vault.TOTP("SixtySeconds", time.Unix(50, 0))
	if err != nil {
		t.Fatal(err)
	}
	if remaining != 10*time.Second {
		t.Fatalf("remaining with a 60s period = %v, want 10s", remaining)
	}
}

// TestTOTPURLIsStoredProtected: an otpauth URL contains the shared secret, so it
// must be encrypted inside the database like the other secret fields.
func TestTOTPURLIsStoredProtected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.kdbx")
	vault, err := Create(path, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	uri := "otpauth://totp/Example:carl?secret=GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	if _, err := vault.Put(WriteOptions{Title: "URI", TOTPSeed: uri}); err != nil {
		t.Fatal(err)
	}
	_, stored, ok := vault.Find("URI")
	if !ok {
		t.Fatal("entry missing")
	}
	for _, value := range stored.Values {
		if strings.EqualFold(value.Key, attrTOTPURL) {
			if !reflect.DeepEqual(value.Value.Protected, wrappers.NewBoolWrapper(true)) {
				t.Fatalf("otpauth field is not protected: %#v", value)
			}
			return
		}
	}
	t.Fatalf("otpauth field not found in %#v", stored.Values)
}
