package orchestrator

import (
	"testing"
)

// TestNewBrowserManager verifies that NewBrowserManager initializes correctly
// without starting a browser process.
func TestNewBrowserManager(t *testing.T) {
	bm := NewBrowserManager(true)
	if bm == nil {
		t.Fatal("expected non-nil BrowserManager")
		return
	}
	if bm.browser != nil {
		t.Error("expected browser to be nil before first GetPage call (lazy init)")
	}
	if bm.pages == nil {
		t.Error("expected pages map to be initialized")
	}
	if !bm.headless {
		t.Error("expected headless=true")
	}
}

// TestBrowserManagerIsAvailable verifies the IsAvailable check runs without panicking.
// The actual return value depends on whether Chrome is installed on the test machine.
func TestBrowserManagerIsAvailable(t *testing.T) {
	bm := NewBrowserManager(true)
	// Just verify it doesn't panic — the return value is environment-dependent.
	available := bm.IsAvailable()
	t.Logf("browser available: %v", available)
}

// TestBrowserManagerShutdownNoop verifies that Shutdown on an un-started
// BrowserManager is a no-op and does not return an error.
func TestBrowserManagerShutdownNoop(t *testing.T) {
	bm := NewBrowserManager(true)
	if err := bm.Shutdown(); err != nil {
		t.Errorf("Shutdown on un-started manager should return nil, got: %v", err)
	}
}

// TestBrowserManagerReleasePageNoop verifies that releasing a page for an agent
// that never had one is a safe no-op.
func TestBrowserManagerReleasePageNoop(t *testing.T) {
	bm := NewBrowserManager(true)
	// Should not panic
	bm.ReleasePage("agent-that-never-existed")
}

// TestBrowserManagerGetPageNoBrowser verifies that GetPage returns an error when
// no browser binary is available. This test is only meaningful in environments
// without Chrome/Chromium installed.
func TestBrowserManagerGetPageNoBrowser(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser test in short mode")
	}

	bm := NewBrowserManager(true)
	if bm.IsAvailable() {
		t.Skip("browser is available; skipping no-browser error test")
	}

	_, err := bm.GetPage("test-agent")
	if err == nil {
		t.Error("expected error when no browser is available, got nil")
	}
	t.Logf("got expected error: %v", err)
}
