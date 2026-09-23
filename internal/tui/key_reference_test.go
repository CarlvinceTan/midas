package tui

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

type keyReference struct {
	Candidates []KeyID             `json:"candidates"`
	Cases      []keyReferenceCase  `json:"cases"`
	Registry   []registryReference `json:"registry"`
}

type keyReferenceCase struct {
	Name        string  `json:"name"`
	Data        string  `json:"data"`
	Kitty       bool    `json:"kitty"`
	Environment string  `json:"environment"`
	Release     bool    `json:"release"`
	Matches     []KeyID `json:"matches"`
}

type registryReference struct {
	Label     string               `json:"label"`
	First     []KeyID              `json:"first"`
	Second    []KeyID              `json:"second"`
	Third     []KeyID              `json:"third"`
	Conflicts []KeybindingConflict `json:"conflicts"`
	User      KeybindingsConfig    `json:"user"`
	Resolved  KeybindingsConfig    `json:"resolved"`
	MatchesX  []KeybindingID       `json:"matchesX"`
}

func loadKeyReference(t *testing.T) keyReference {
	t.Helper()
	data, err := os.ReadFile("testdata/key-reference.json")
	if err != nil {
		t.Fatal(err)
	}
	var reference keyReference
	if err := json.Unmarshal(data, &reference); err != nil {
		t.Fatal(err)
	}
	if len(reference.Cases)+len(reference.Candidates) == 0 {
		t.Fatal("the reference fixture contains no cases")
	}
	return reference
}

func TestKeyMatchingMatchesReference(t *testing.T) {
	reference := loadKeyReference(t)
	for _, test := range reference.Cases {
		t.Run(test.Name, func(t *testing.T) {
			t.Setenv("WT_SESSION", "")
			t.Setenv("SSH_CONNECTION", "")
			t.Setenv("SSH_CLIENT", "")
			t.Setenv("SSH_TTY", "")
			if test.Environment == "windows" || test.Environment == "windows-ssh" {
				t.Setenv("WT_SESSION", "1")
			}
			if test.Environment == "windows-ssh" {
				t.Setenv("SSH_CONNECTION", "remote")
			}
			SetKittyProtocolActive(test.Kitty)
			matches := make([]KeyID, 0)
			for _, candidate := range reference.Candidates {
				if MatchesKey(test.Data, candidate) {
					matches = append(matches, candidate)
				}
			}
			if !reflect.DeepEqual(matches, test.Matches) {
				t.Errorf("matches = %#v, reference = %#v", matches, test.Matches)
			}
			if release := IsKeyRelease(test.Data); release != test.Release {
				t.Errorf("IsKeyRelease = %v, reference = %v", release, test.Release)
			}
		})
	}
	SetKittyProtocolActive(false)
}

func TestKeybindingsManagerMatchesReference(t *testing.T) {
	reference := loadKeyReference(t)
	definitions := []KeybindingDefinition{
		keybinding("first", "first", "a"),
		keybinding("second", "second", "b", "b"),
		keybinding("third", "third"),
	}
	manager := NewKeybindingsManager(definitions, KeybindingsConfig{
		"first":   {},
		"second":  {"x", "x"},
		"third":   {"x"},
		"unknown": {"z"},
	})
	actual := []registryReference{registrySnapshot("initial", manager)}
	manager.SetUserBindings(KeybindingsConfig{"first": {"ctrl+c", "ctrl+c"}})
	actual = append(actual, registrySnapshot("updated", manager))
	if !reflect.DeepEqual(actual, reference.Registry) {
		got, _ := json.MarshalIndent(actual, "", "  ")
		want, _ := json.MarshalIndent(reference.Registry, "", "  ")
		t.Fatalf("registry mismatch\ngot:  %s\nwant: %s", got, want)
	}
}

func registrySnapshot(label string, manager *KeybindingsManager) registryReference {
	matches := make([]KeybindingID, 0)
	for _, id := range []KeybindingID{"first", "second", "third"} {
		if manager.Matches("x", id) {
			matches = append(matches, id)
		}
	}
	return registryReference{
		Label: label, First: manager.Keys("first"), Second: manager.Keys("second"), Third: manager.Keys("third"),
		Conflicts: manager.Conflicts(), User: manager.UserBindings(), Resolved: manager.ResolvedBindings(), MatchesX: matches,
	}
}
