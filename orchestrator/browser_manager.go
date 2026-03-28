package orchestrator

import (
	"fmt"
	"os/exec"
	"sync"

	"github.com/GoCodeAlone/modular"
	"github.com/GoCodeAlone/workflow/config"
	"github.com/GoCodeAlone/workflow/plugin"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

// BrowserManager manages a shared Rod browser instance and per-agent pages.
// The browser is lazily initialized on first use to avoid consuming resources
// unless browser tools are actually invoked.
type BrowserManager struct {
	mu       sync.Mutex
	browser  *rod.Browser
	headless bool
	pages    map[string]*rod.Page // keyed by agent ID
}

// NewBrowserManager creates a BrowserManager. The browser is not started until
// GetPage is first called.
func NewBrowserManager(headless bool) *BrowserManager {
	return &BrowserManager{
		headless: headless,
		pages:    make(map[string]*rod.Page),
	}
}

// IsAvailable checks whether a Chrome/Chromium binary is accessible.
func (bm *BrowserManager) IsAvailable() bool {
	// Rod's launcher can find the browser path
	if _, has := launcher.LookPath(); has {
		return true
	}
	// Also check common system locations
	for _, name := range []string{"google-chrome", "chromium", "chromium-browser", "chrome"} {
		if p, err := exec.LookPath(name); err == nil && p != "" {
			return true
		}
	}
	return false
}

// ensureBrowser starts the browser if it is not already running.
// Must be called with bm.mu held.
func (bm *BrowserManager) ensureBrowser() error {
	if bm.browser != nil {
		return nil
	}

	l := launcher.New().Headless(bm.headless)
	url, err := l.Launch()
	if err != nil {
		return fmt.Errorf("launch browser: %w", err)
	}

	bm.browser = rod.New().ControlURL(url)
	if err := bm.browser.Connect(); err != nil {
		bm.browser = nil
		return fmt.Errorf("connect to browser: %w", err)
	}
	return nil
}

// GetPage returns the page associated with agentID, creating one if needed.
// If the browser has not been started yet, it is started now.
func (bm *BrowserManager) GetPage(agentID string) (*rod.Page, error) {
	bm.mu.Lock()
	defer bm.mu.Unlock()

	if p, ok := bm.pages[agentID]; ok {
		return p, nil
	}

	if err := bm.ensureBrowser(); err != nil {
		return nil, err
	}

	page, err := bm.browser.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		return nil, fmt.Errorf("create page: %w", err)
	}

	bm.pages[agentID] = page
	return page, nil
}

// ReleasePage closes the page associated with agentID and removes it from the map.
func (bm *BrowserManager) ReleasePage(agentID string) {
	bm.mu.Lock()
	defer bm.mu.Unlock()

	if p, ok := bm.pages[agentID]; ok {
		_ = p.Close()
		delete(bm.pages, agentID)
	}
}

// Shutdown closes all agent pages and then the browser itself.
func (bm *BrowserManager) Shutdown() error {
	bm.mu.Lock()
	defer bm.mu.Unlock()

	for id, p := range bm.pages {
		_ = p.Close()
		delete(bm.pages, id)
	}

	if bm.browser != nil {
		err := bm.browser.Close()
		bm.browser = nil
		return err
	}
	return nil
}

// browserManagerHook creates a BrowserManager and registers it in the service registry.
func browserManagerHook() plugin.WiringHook {
	return plugin.WiringHook{
		Name:     "ratchet.browser_manager",
		Priority: 75,
		Hook: func(app modular.Application, _ *config.WorkflowConfig) error {
			mgr := NewBrowserManager(true)
			_ = app.RegisterService("ratchet-browser-manager", mgr)
			return nil
		},
	}
}
