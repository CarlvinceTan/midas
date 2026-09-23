// Package vault is the Midas secret store: one KeePass-compatible database that
// holds account passwords, TOTP seeds, and recovery codes, and hands them to an
// agent only once the user has unlocked it for the session.
//
// The file format is KeePass's on purpose: the database stays readable by the
// tools the user already trusts, nothing about it is Midas-specific, and there is
// no server or daemon holding the secrets.
package vault

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
	"github.com/tobischo/gokeepasslib/v3"
	w "github.com/tobischo/gokeepasslib/v3/wrappers"
)

// Entry is one stored secret, flattened for the agent. Absent fields are empty
// rather than guessed.
type Entry struct {
	Title       string `json:"title"`
	Username    string `json:"username,omitempty"`
	URL         string `json:"url,omitempty"`
	Notes       string `json:"notes,omitempty"`
	Group       string `json:"group,omitempty"`
	HasPassword bool   `json:"hasPassword,omitempty"`
	// HasTOTP reports a TOTP seed without revealing it.
	HasTOTP bool `json:"hasTOTP,omitempty"`
	// HasRecoveryCodes reports stored recovery codes without listing them.
	HasRecoveryCodes bool `json:"hasRecoveryCodes,omitempty"`
}

// Attribute names inside a KeePass entry. KeePassXC reads and writes the same
// names, which is what makes the database portable.
const (
	attrTOTP     = "otp"
	attrTOTPURL  = "otpauth"
	attrRecovery = "recovery.codes"
)

// Vault is an unlocked database. A locked vault cannot be read: the caller must
// supply the password again.
type Vault struct {
	path     string
	password string
	db       *gokeepasslib.Database
}

// Exists reports whether a database is present at path.
func Exists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// DefaultPath is where the vault lives: beside the agent's own configuration,
// because that is where the user expects to find it.
func DefaultPath(configDir string) string {
	if strings.TrimSpace(configDir) == "" {
		configDir = "."
	}
	return filepath.Join(configDir, "vault.kdbx")
}

// Create makes a new, empty database and returns it unlocked. It refuses to
// overwrite an existing file: creating a vault must never destroy one.
func Create(path, password string) (*Vault, error) {
	if strings.TrimSpace(password) == "" {
		return nil, errors.New("vault: a password is required")
	}
	if Exists(path) {
		return nil, fmt.Errorf("vault: %s already exists", path)
	}
	db := gokeepasslib.NewDatabase()
	db.Credentials = gokeepasslib.NewPasswordCredentials(password)
	db.Content = gokeepasslib.NewContent()
	db.Content.Root = &gokeepasslib.RootData{Groups: []gokeepasslib.Group{gokeepasslib.NewGroup()}}
	db.Content.Root.Groups[0].Name = "Midas"
	vault := &Vault{path: path, password: password, db: db}
	if err := vault.Save(); err != nil {
		return nil, err
	}
	return vault, nil
}

// Open unlocks a database, or reports a wrong password.
func Open(path, password string) (*Vault, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("vault: open %s: %w", path, err)
	}
	defer file.Close()
	db := gokeepasslib.NewDatabase()
	db.Credentials = gokeepasslib.NewPasswordCredentials(password)
	if err := gokeepasslib.NewDecoder(file).Decode(db); err != nil {
		return nil, fmt.Errorf("vault: unlock %s: %w", path, err)
	}
	if err := db.UnlockProtectedEntries(); err != nil {
		return nil, fmt.Errorf("vault: read protected fields: %w", err)
	}
	return &Vault{path: path, password: password, db: db}, nil
}

// Path is the database file.
func (v *Vault) Path() string { return v.path }

// Save writes the database atomically, so an interrupted write cannot leave a
// half-written vault behind.
func (v *Vault) Save() error {
	for index := range v.group().Entries {
		protect(&v.group().Entries[index])
	}
	if err := v.db.LockProtectedEntries(); err != nil {
		return err
	}
	defer func() { _ = v.db.UnlockProtectedEntries() }()
	directory := filepath.Dir(v.path)
	// A user may name a vault in a directory that does not exist yet, so the
	// directory is created rather than failing the write with a confusing error.
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("vault: create %s: %w", directory, err)
	}
	encoded := filepath.Join(directory, ".vault-"+filepath.Base(v.path))
	file, err := os.OpenFile(encoded, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("vault: write %s: %w", v.path, err)
	}
	if err := gokeepasslib.NewEncoder(file).Encode(v.db); err != nil {
		file.Close()
		os.Remove(encoded)
		return err
	}
	if err := file.Close(); err != nil {
		os.Remove(encoded)
		return err
	}
	if err := os.Chmod(encoded, 0o600); err != nil {
		os.Remove(encoded)
		return err
	}
	return os.Rename(encoded, v.path)
}

// ChangePassword re-encrypts the database with a new password.
func (v *Vault) ChangePassword(current, next string) error {
	if current != v.password {
		return errors.New("vault: the current password is wrong")
	}
	if strings.TrimSpace(next) == "" {
		return errors.New("vault: a new password is required")
	}
	v.password = next
	v.db.Credentials = gokeepasslib.NewPasswordCredentials(next)
	return v.Save()
}

func (v *Vault) group() *gokeepasslib.Group {
	if v.db.Content == nil || v.db.Content.Root == nil || len(v.db.Content.Root.Groups) == 0 {
		v.db.Content = gokeepasslib.NewContent()
		v.db.Content.Root = &gokeepasslib.RootData{Groups: []gokeepasslib.Group{gokeepasslib.NewGroup()}}
		v.db.Content.Root.Groups[0].Name = "Midas"
	}
	return &v.db.Content.Root.Groups[0]
}

// Entries lists every stored secret, newest name order, without revealing values.
func (v *Vault) Entries() []Entry {
	entries := make([]Entry, 0, 8)
	for _, entry := range v.group().Entries {
		entries = append(entries, describe(v.group().Name, entry))
	}
	slices.SortStableFunc(entries, func(a, b Entry) int { return cmp.Compare(a.Title, b.Title) })
	return entries
}

func describe(group string, entry gokeepasslib.Entry) Entry {
	result := Entry{
		Title:    entry.GetTitle(),
		Username: entry.GetContent("UserName"),
		URL:      entry.GetContent("URL"),
		Notes:    entry.GetContent("Notes"),
		Group:    group,
	}
	for _, value := range entry.Values {
		switch strings.ToLower(value.Key) {
		case "password":
			result.HasPassword = value.Value.Content != ""
		case attrTOTP, attrTOTPURL:
			result.HasTOTP = result.HasTOTP || strings.TrimSpace(value.Value.Content) != ""
		case attrRecovery:
			result.HasRecoveryCodes = strings.TrimSpace(value.Value.Content) != ""
		}
	}
	return result
}

// Find returns the entry with a title, case-insensitively.
func (v *Vault) Find(title string) (Entry, gokeepasslib.Entry, bool) {
	wanted := strings.ToLower(strings.TrimSpace(title))
	for _, entry := range v.group().Entries {
		if strings.ToLower(entry.GetTitle()) == wanted {
			return describe(v.group().Name, entry), entry, true
		}
	}
	return Entry{}, gokeepasslib.Entry{}, false
}

// Password returns a stored password.
func (v *Vault) Password(title string) (string, error) {
	entry, stored, ok := v.Find(title)
	if !ok {
		return "", fmt.Errorf("vault: no entry named %q", title)
	}
	if !entry.HasPassword {
		return "", fmt.Errorf("vault: %q has no password stored", title)
	}
	return stored.GetPassword(), nil
}

// TOTP returns the current one-time code for an entry, and how long it stays
// valid, so a caller can wait for a fresh code instead of sending a stale one.
func (v *Vault) TOTP(title string, now time.Time) (string, time.Duration, error) {
	_, stored, ok := v.Find(title)
	if !ok {
		return "", 0, fmt.Errorf("vault: no entry named %q", title)
	}
	secret := strings.TrimSpace(stored.GetContent(attrTOTP))
	uri := strings.TrimSpace(stored.GetContent(attrTOTPURL))
	// Authenticators may use a non-default period or digit count, so an otpauth
	// record is honoured rather than reduced to its secret.
	period, digits, algorithm := uint(30), otp.DigitsSix, otp.AlgorithmSHA1
	algorithmName := "SHA1"
	if secret == "" && uri != "" {
		parsed, err := otp.NewKeyFromURL(uri)
		if err != nil {
			return "", 0, fmt.Errorf("vault: %q has an unreadable otpauth record: %w", title, err)
		}
		secret = parsed.Secret()
		if value := parsed.Period(); value > 0 {
			period = uint(value)
		}
		if value := parsed.Digits(); value != 0 {
			digits = value
		}
		if value := parsed.Algorithm(); value != 0 {
			algorithm = value
			algorithmName = algorithm.String()
		}
	}
	if secret == "" {
		return "", 0, fmt.Errorf("vault: %q has no TOTP seed stored", title)
	}
	code, err := totp.GenerateCodeCustom(strings.ToUpper(secret), now, totp.ValidateOpts{
		Period: period, Skew: 1, Digits: digits, Algorithm: algorithm,
	})
	if err != nil {
		return "", 0, fmt.Errorf("vault: generate a %s code for %q: %w", algorithmName, title, err)
	}
	remaining := time.Duration(int64(period)-now.Unix()%int64(period)) * time.Second
	return code, remaining, nil
}

// RecoveryCodes returns the stored recovery codes for an entry.
func (v *Vault) RecoveryCodes(title string) ([]string, error) {
	_, stored, ok := v.Find(title)
	if !ok {
		return nil, fmt.Errorf("vault: no entry named %q", title)
	}
	raw := strings.TrimSpace(stored.GetContent(attrRecovery))
	if raw == "" {
		return nil, fmt.Errorf("vault: %q has no recovery codes stored", title)
	}
	codes := []string{}
	for _, line := range strings.Split(raw, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			codes = append(codes, trimmed)
		}
	}
	return codes, nil
}

// WriteOptions are the fields a caller can store. Empty fields are left as they
// are, so adding a TOTP seed to an existing entry does not clear its password.
type WriteOptions struct {
	Title         string
	Username      string
	URL           string
	Notes         string
	Password      *string
	TOTPSeed      string
	RecoveryCodes []string
}

// Put creates or updates an entry and saves the database.
func (v *Vault) Put(options WriteOptions) (Entry, error) {
	title := strings.TrimSpace(options.Title)
	if title == "" {
		return Entry{}, errors.New("vault: an entry needs a title")
	}
	group := v.group()
	var target *gokeepasslib.Entry
	for index := range group.Entries {
		if strings.EqualFold(group.Entries[index].GetTitle(), title) {
			target = &group.Entries[index]
			break
		}
	}
	if target == nil {
		entry := gokeepasslib.NewEntry()
		group.Entries = append(group.Entries, entry)
		target = &group.Entries[len(group.Entries)-1]
	}
	setContent(target, "Title", title)
	// Only the fields the caller supplied are replaced: a documentation comment on
	// WriteOptions promises that adding a TOTP seed leaves the password and the
	// account details alone, and overwriting them with empty strings would erase
	// them from an existing entry.
	if value := strings.TrimSpace(options.Username); value != "" {
		setContent(target, "UserName", value)
	}
	if value := strings.TrimSpace(options.URL); value != "" {
		setContent(target, "URL", value)
	}
	if value := strings.TrimSpace(options.Notes); value != "" {
		setContent(target, "Notes", value)
	}
	if options.Password != nil {
		setContent(target, "Password", *options.Password)
	}
	if seed := strings.TrimSpace(options.TOTPSeed); seed != "" {
		// A caller may hand over a bare seed or a full otpauth URL, which is what
		// an authenticator exports. Store it in the field that matches, so
		// KeePassXC shows it correctly either way.
		if strings.HasPrefix(strings.ToLower(seed), "otpauth://") {
			setContent(target, attrTOTPURL, seed)
			setContent(target, attrTOTP, "")
		} else {
			setContent(target, attrTOTP, strings.ToUpper(seed))
			setContent(target, attrTOTPURL, "")
		}
	}
	if len(options.RecoveryCodes) > 0 {
		setContent(target, attrRecovery, strings.Join(options.RecoveryCodes, "\n"))
	}
	if err := v.Save(); err != nil {
		return Entry{}, err
	}
	described, _, _ := v.Find(title)
	return described, nil
}

// Delete removes an entry, reporting whether it existed. Deleting a vault file is
// a separate, explicit act: an agent that can call Delete must not be able to
// destroy the whole store by accident.
func (v *Vault) Delete(title string) (bool, error) {
	group := v.group()
	for index := range group.Entries {
		if strings.EqualFold(group.Entries[index].GetTitle(), strings.TrimSpace(title)) {
			group.Entries = append(group.Entries[:index], group.Entries[index+1:]...)
			return true, v.Save()
		}
	}
	return false, nil
}

// Destroy removes the database file itself. The caller is expected to have asked
// the user: this is not reversible.
func (v *Vault) Destroy() error {
	if err := os.Remove(v.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// setContent writes a KeePass field, leaving protected fields protected.
func setContent(entry *gokeepasslib.Entry, key, value string) {
	for index := range entry.Values {
		if strings.EqualFold(entry.Values[index].Key, key) {
			entry.Values[index].Value.Content = value
			return
		}
	}
	entry.Values = append(entry.Values, gokeepasslib.ValueData{Key: key, Value: gokeepasslib.V{Content: value}})
}

// Protect marks the fields that must be encrypted inside the database.
func protect(entry *gokeepasslib.Entry) {
	for index := range entry.Values {
		switch strings.ToLower(entry.Values[index].Key) {
		case "password", attrTOTP, attrTOTPURL, attrRecovery:
			entry.Values[index].Value.Protected = w.NewBoolWrapper(true)
		}
	}
}
