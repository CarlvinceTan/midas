package vault

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
)

// TestVaultRoundTripsSecretsThroughAKeePassFile covers the foundation the Vault
// MCP is built on: a real kdbx file, protected fields, and values that survive a
// close and reopen.
func TestVaultRoundTripsSecretsThroughAKeePassFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.kdbx")
	created, err := Create(path, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !Exists(path) {
		t.Fatal("creating a vault wrote no file")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("vault mode = %v", info.Mode().Perm())
	}
	// A second create must not clobber an existing vault.
	if _, err := Create(path, "another"); err == nil {
		t.Fatal("creating over an existing vault was allowed")
	}

	secret := "hunter2"
	seed := "JBSWY3DPEHPK3PXP"
	if _, err := created.Put(WriteOptions{
		Title: "GitHub", Username: "carl", URL: "https://github.com",
		Password: &secret, TOTPSeed: seed,
		RecoveryCodes: []string{"aaaa-bbbb", "cccc-dddd"},
	}); err != nil {
		t.Fatal(err)
	}

	// The file is a KeePass database, not a JSON blob: reopening needs the
	// password, and the wrong one must fail.
	if _, err := Open(path, "wrong"); err == nil {
		t.Fatal("the wrong password unlocked the vault")
	}
	reopened, err := Open(path, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	password, err := reopened.Password("GitHub")
	if err != nil || password != secret {
		t.Fatalf("password = %q, %v", password, err)
	}
	entry, _, ok := reopened.Find("github") // case-insensitive lookup
	if !ok {
		t.Fatal("the entry was not found")
	}
	if entry.Username != "carl" || entry.URL != "https://github.com" {
		t.Fatalf("entry = %#v", entry)
	}
	if !entry.HasPassword || !entry.HasTOTP || !entry.HasRecoveryCodes {
		t.Fatalf("entry flags = %#v", entry)
	}

	codes, err := reopened.RecoveryCodes("GitHub")
	if err != nil || len(codes) != 2 || codes[0] != "aaaa-bbbb" {
		t.Fatalf("recovery codes = %#v, %v", codes, err)
	}
	// The listing never carries a secret.
	for _, listed := range reopened.Entries() {
		if strings.Contains(listed.Notes, seed) || strings.Contains(listed.Title, seed) {
			t.Fatalf("listing leaked a secret: %#v", listed)
		}
	}
}

func TestTOTPCodesMatchTheStandardAlgorithm(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.kdbx")
	vault, err := Create(path, "pw")
	if err != nil {
		t.Fatal(err)
	}
	// A seed without an entry, and one stored as a full otpauth URL: both are
	// shapes real authenticators produce.
	if _, err := vault.Put(WriteOptions{Title: "Plain", TOTPSeed: "JBSWY3DPEHPK3PXP"}); err != nil {
		t.Fatal(err)
	}
	uri := "otpauth://totp/Midas:me?secret=JBSWY3DPEHPK3PXP&issuer=Midas"
	stored, err := vault.Put(WriteOptions{Title: "Uri", TOTPSeed: uri})
	if err != nil {
		t.Fatal(err)
	}
	_ = stored
	now := time.Unix(1_700_000_000, 0)
	code, remaining, err := vault.TOTP("Plain", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(code) != 6 {
		t.Fatalf("code = %q", code)
	}
	if remaining <= 0 || remaining > 30*time.Second {
		t.Fatalf("remaining = %v", remaining)
	}
	// The same secret produces the same code wherever it came from.
	if _, err := vault.Put(WriteOptions{Title: "PlainURI", TOTPSeed: uri}); err != nil {
		t.Fatal(err)
	}
	fromURI, _, err := vault.TOTP("PlainURI", now)
	if err != nil {
		t.Fatal(err)
	}
	if fromURI != code {
		t.Fatalf("codes differ: %q vs %q", fromURI, code)
	}
	// And it is a real TOTP: the standard validator accepts it.
	expected, err := totp.GenerateCode("JBSWY3DPEHPK3PXP", now)
	if err != nil {
		t.Fatal(err)
	}
	if expected != code {
		t.Fatalf("code = %q, want %q", code, expected)
	}
	if _, _, err := vault.TOTP("Missing", now); err == nil {
		t.Fatal("a missing entry produced a code")
	}
}

func TestVaultPasswordChangeAndDelete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.kdbx")
	vault, err := Create(path, "first")
	if err != nil {
		t.Fatal(err)
	}
	secret := "s3cret"
	if _, err := vault.Put(WriteOptions{Title: "Mail", Password: &secret}); err != nil {
		t.Fatal(err)
	}
	if err := vault.ChangePassword("wrong", "second"); err == nil {
		t.Fatal("the wrong current password was accepted")
	}
	if err := vault.ChangePassword("first", "second"); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path, "first"); err == nil {
		t.Fatal("the old password still opens the vault")
	}
	reopened, err := Open(path, "second")
	if err != nil {
		t.Fatal(err)
	}
	if password, err := reopened.Password("Mail"); err != nil || password != secret {
		t.Fatalf("password after re-encryption = %q, %v", password, err)
	}
	removed, err := reopened.Delete("Mail")
	if err != nil || !removed {
		t.Fatalf("delete = %v, %v", removed, err)
	}
	if entries := reopened.Entries(); len(entries) != 0 {
		t.Fatalf("entries after delete = %#v", entries)
	}
	if removed, _ := reopened.Delete("Mail"); removed {
		t.Fatal("deleting a missing entry reported success")
	}
	// Updating an existing entry keeps the fields the caller did not mention.
	seed := "JBSWY3DPEHPK3PXP"
	if _, err := reopened.Put(WriteOptions{Title: "Mail", Password: &secret, TOTPSeed: seed}); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Put(WriteOptions{Title: "Mail", Username: "me"}); err != nil {
		t.Fatal(err)
	}
	entry, _, ok := reopened.Find("Mail")
	if !ok || entry.Username != "me" || !entry.HasPassword || !entry.HasTOTP {
		t.Fatalf("entry after update = %#v", entry)
	}
	// Destroying the file is explicit and separate from deleting an entry.
	if err := reopened.Destroy(); err != nil {
		t.Fatal(err)
	}
	if Exists(path) {
		t.Fatal("the vault file survived destruction")
	}
}
