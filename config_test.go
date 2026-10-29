package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfig(t *testing.T) {
	b, e := os.ReadFile("config.example.json")
	if e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		name, old, new string
		valid          bool
	}{
		{"valid", "", "", true}, {"unknown", "\"display\"", "\"typo\"", false}, {"badPort", "\"port\": 22", "\"port\": -1", false},
		{"legacyTitle", `"mode": "codex_tui"`, `"title_regex": "^Codex-clipbridge$"`, true},
		{"matchAll", `"mode": "codex_tui"`, `"mode":"title", "title_regex":".*"`, false},
		{"regex", `"mode": "codex_tui"`, `"mode":"title", "title_regex":"["`, false},
		{"missingTitle", `"mode": "codex_tui"`, `"mode":"title"`, false},
		{"defaultAuto", `"mode": "codex_tui",`, ``, true},
		{"unknownMode", `"codex_tui"`, `"any_terminal"`, false},
		{"conflictingModes", `"mode": "codex_tui"`, `"mode":"codex_tui", "title_regex":"^old$"`, false},
		{"limit", "20971520", "268435457", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := string(b)
			if tc.old != "" {
				if !strings.Contains(s, tc.old) {
					t.Fatal("fixture replacement not found")
				}
				s = strings.Replace(s, tc.old, tc.new, 1)
			}
			p := filepath.Join(t.TempDir(), "c.json")
			if e = os.WriteFile(p, []byte(s), 0600); e != nil {
				t.Fatal(e)
			}
			_, e := loadConfig(p)
			if (e == nil) != tc.valid {
				t.Fatal(e)
			}
		})
	}
}
func TestShellQuote(t *testing.T) {
	if quote("a'b") != "'a'\"'\"'b'" {
		t.Fatal("unsafe quoting")
	}
}
