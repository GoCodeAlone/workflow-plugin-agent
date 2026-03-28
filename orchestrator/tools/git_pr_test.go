package tools

import (
	"testing"
)

func TestParseGitRemote_SSH(t *testing.T) {
	// This test only validates the regex, not actual git commands
	matches := remoteURLRegex.FindStringSubmatch("git@github.com:GoCodeAlone/ratchet.git")
	if len(matches) < 3 {
		t.Fatal("expected match")
	}
	if matches[1] != "GoCodeAlone" || matches[2] != "ratchet" {
		t.Errorf("expected GoCodeAlone/ratchet, got %s/%s", matches[1], matches[2])
	}
}

func TestParseGitRemote_HTTPS(t *testing.T) {
	matches := remoteURLRegex.FindStringSubmatch("https://github.com/GoCodeAlone/ratchet.git")
	if len(matches) < 3 {
		t.Fatal("expected match")
	}
	if matches[1] != "GoCodeAlone" || matches[2] != "ratchet" {
		t.Errorf("expected GoCodeAlone/ratchet, got %s/%s", matches[1], matches[2])
	}
}

func TestParseGitRemote_NoGitSuffix(t *testing.T) {
	matches := remoteURLRegex.FindStringSubmatch("https://github.com/GoCodeAlone/ratchet")
	if len(matches) < 3 {
		t.Fatal("expected match")
	}
	if matches[1] != "GoCodeAlone" || matches[2] != "ratchet" {
		t.Errorf("expected GoCodeAlone/ratchet, got %s/%s", matches[1], matches[2])
	}
}
