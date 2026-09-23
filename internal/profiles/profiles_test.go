package profiles

import (
	"strings"
	"testing"
)

func TestProfilesAreDeterministicAndReservedHelpersAreMarked(t *testing.T) {
	profiles := Profiles()
	want := []string{"advisor", "explore", "main", "summary", "title"}
	if len(profiles) != len(want) {
		t.Fatalf("profiles = %#v", profiles)
	}
	for index, name := range want {
		if profiles[index].Name != name {
			t.Fatalf("profile %d = %q", index, profiles[index].Name)
		}
	}
	profile, err := ResolveProfile("title")
	if err != nil || !profile.Reserved {
		t.Fatalf("title = %#v, %v", profile, err)
	}
	if _, err := ResolveProfile("missing"); err == nil {
		t.Fatal("missing profile resolved")
	}
	if _, err := ResolveProfile("orchestrator"); err == nil {
		t.Fatal("removed orchestrator profile still resolves")
	}
}

func TestUtilityAgentsAreReservedHelpers(t *testing.T) {
	for _, name := range []string{"title", "summary"} {
		profile, err := ResolveProfile(name)
		if err != nil {
			t.Fatal(err)
		}
		if !IsUtilityAgent(name) || !profile.Reserved {
			t.Fatalf("%s = %#v", name, profile)
		}
		if IsEntryAgent(name) {
			t.Fatalf("%s counted as an entry agent", name)
		}
		if profile.SystemPrompt == "" {
			t.Fatalf("%s has no helper instructions", name)
		}
	}
	for _, name := range []string{"main", "advisor", "explore"} {
		if IsUtilityAgent(name) {
			t.Fatalf("%s counted as a utility agent", name)
		}
	}
}

func TestWritingProfilesKeepScratchFilesOutOfTheWorkspace(t *testing.T) {
	for _, name := range []string{"main"} {
		profile, err := ResolveProfile(name)
		if err != nil {
			t.Fatal(err)
		}
		prompt := profile.SystemPrompt
		if !strings.Contains(prompt, "system temp directory") || !strings.Contains(prompt, "TMPDIR") {
			t.Fatalf("%s profile does not direct scratch files to the system temp directory:\n%s", name, prompt)
		}
	}
}
