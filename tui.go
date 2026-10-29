package main

import (
	"regexp"
	"strings"
)

var contextFooter = regexp.MustCompile(`(?:^|\s)(?:100|[0-9]{1,2})%\s+context left(?:\s|$)`)
var shortcutFooter = regexp.MustCompile(`(?:^|[^\p{L}\p{N}\s]|\s{2,})\s*\? for shortcuts(?:\s|$|[·│|])`)
var shellPrompt = regexp.MustCompile(`(?i)(?:[\w.-]+@[\w.-]+.*[$#]|PS\s+.*>|[a-z]:\\.*>|^[$#]|^[❯➜]).*$`)

type tuiEvidence struct {
	Composer         bool `json:"composer"`
	Context          bool `json:"context_footer"`
	Header           bool `json:"codex_header"`
	Shortcuts        bool `json:"shortcuts_footer"`
	ShellAfterFooter bool `json:"shell_after_footer"`
	HintPresent      bool `json:"hint_present"`
	HintOffset       int  `json:"hint_lines_from_bottom,omitempty"`
}

func (e tuiEvidence) matches() bool {
	return e.Composer && !e.ShellAfterFooter && (e.Context || e.Shortcuts)
}

// Only the visible viewport is supplied, never the terminal's document history.
// A banner or mention of Codex is insufficient: require a bottom composer and
// Codex-specific footer. This is UI recognition, not process authentication.
func recognizeCodex(text string) tuiEvidence {
	lines := strings.Split(strings.ReplaceAll(text, "\r", ""), "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	e := tuiEvidence{Header: strings.Contains(text, "OpenAI Codex")}
	start := max(0, len(lines)-12)
	footerIndex := -1
	for i := start; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		line = strings.TrimLeft(line, "│┃╎ ")
		if i >= len(lines)-5 && strings.HasPrefix(line, "›") {
			e.Composer = true
		}
		if i >= len(lines)-3 {
			e.Context = e.Context || contextFooter.MatchString(strings.ToLower(line))
			e.Shortcuts = e.Shortcuts || shortcutFooter.MatchString(line)
			if strings.Contains(line, "? for shortcuts") {
				e.HintPresent = true
				e.HintOffset = len(lines) - 1 - i
			}
			if e.Context || e.Shortcuts {
				if footerIndex < 0 {
					footerIndex = i
				}
			}
		}
		if footerIndex >= 0 && i > footerIndex && shellPrompt.MatchString(line) {
			e.ShellAfterFooter = true
		}
	}
	return e
}
