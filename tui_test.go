package main

import (
	"strings"
	"testing"
)

func TestRecognizeCodex(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		want       bool
	}{
		{"idle", "A response\n\n› Ask anything\n\n  ? for shortcuts    98% context left\n", true},
		{"draft", "Response\n› please inspect this\n  [Image #1]\n92% context left\n", true},
		{"contextOnlyFooter", "result\n›\n  100% context left", true},
		{"startupCustomStatus", "OpenAI Codex (v0.1)\n› Ask anything\n? for shortcuts  custom status", true},
		{"shellBannerScrollback", "OpenAI Codex (v0.1)\n› Ask anything\n? for shortcuts 98% context left\n" + strings.Repeat("shell output\n", 15) + "user@debian:~$", false},
		{"mention", "OpenAI Codex supports 100% context left\nuser@debian:~$", false},
		{"shell", "user@debian:~$ ls\nfoo.txt\nuser@debian:~$", false},
		{"otherTUI", "Claude Code\n❯ Ask anything\n? for shortcuts", false},
		{"customStatusNoBanner", "A response\n› something\n? for shortcuts", true},
		{"twoLineFooter", "A response\n› something\n? for shortcuts\ngpt-5.4 high · workspace", true},
		{"agentsFooter", "A response\n› something\n3 for agents · ? for shortcuts\ngpt-5.4 high · workspace", true},
		{"spacedFooter", "› something\nPlan mode  ? for shortcuts\nstatus", true},
		{"contextAndStatus", "› something\n98% context left\ngpt-5.4 high", true},
		{"genericHelp", "› something\nThe documentation says ? for shortcuts", false},
		{"exitedCodex", "OpenAI Codex\n› something\n? for shortcuts\nuser@debian:~$", false},
		{"quotedBanner", "The documentation mentions › something\n? for shortcuts", false},
		{"oldFooterAboveShell", "› Ask anything\n? for shortcuts 98% context left\nuser@debian:~$\nuser@debian:~$\nuser@debian:~$", false},
		{"empty", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := recognizeCodex(tc.text)
			if e.matches() != tc.want {
				t.Fatalf("recognition=%+v want=%v", e, tc.want)
			}
		})
	}
}
