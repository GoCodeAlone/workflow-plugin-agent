package tools

import "testing"

func TestDetectPlaceholder_AngleBracket(t *testing.T) {
	ok, reason := DetectPlaceholder("<improved yaml>")
	if !ok {
		t.Error("expected placeholder detected for <improved yaml>")
	}
	if reason == "" {
		t.Error("expected non-empty reason")
	}
}

func TestDetectPlaceholder_DollarBrace(t *testing.T) {
	ok, _ := DetectPlaceholder("${CONFIG}")
	if !ok {
		t.Error("expected placeholder detected for ${CONFIG}")
	}
}

func TestDetectPlaceholder_TODO(t *testing.T) {
	ok, _ := DetectPlaceholder("TODO: fill this in")
	if !ok {
		t.Error("expected placeholder detected for TODO")
	}
}

func TestDetectPlaceholder_TooShort(t *testing.T) {
	ok, reason := DetectPlaceholder("hi")
	if !ok {
		t.Error("expected placeholder detected for short content")
	}
	if reason != "content too short (< 10 chars)" {
		t.Errorf("unexpected reason: %q", reason)
	}
}

func TestDetectPlaceholder_RealYAML(t *testing.T) {
	yaml := `version: "1.0"
endpoints:
  - path: /api/health
    method: GET
`
	ok, _ := DetectPlaceholder(yaml)
	if ok {
		t.Error("expected no placeholder detected for real YAML content")
	}
}

func TestDetectPlaceholder_EmptyString(t *testing.T) {
	ok, _ := DetectPlaceholder("")
	if !ok {
		t.Error("expected placeholder detected for empty string")
	}
}
