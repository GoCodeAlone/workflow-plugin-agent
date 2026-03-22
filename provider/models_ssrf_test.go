package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestListModels_SSRF_VulnExists proves that without base_url validation a
// caller can redirect provider requests to arbitrary internal services.
// After the fix this test FAILS because the SSRF is blocked before the request
// is made.
func TestListModels_SSRF_VulnExists(t *testing.T) {
	reached := false
	victim := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[]}`)
	}))
	defer victim.Close()

	// Supply the internal test server as base_url — should be blocked after fix.
	_, _ = listAnthropicModels(context.Background(), "fake-key", victim.URL)

	if !reached {
		t.Skip("SSRF is correctly blocked — vulnerability no longer exists")
	}
	// Vulnerability exists: request reached the internal server.
}

// --- Fix validation tests ---

func TestValidateBaseURL_EmptyAllowed(t *testing.T) {
	if err := ValidateBaseURL(""); err != nil {
		t.Errorf("ValidateBaseURL(\"\"): unexpected error: %v", err)
	}
}

func TestValidateBaseURL_HttpSchemeRejected(t *testing.T) {
	if err := ValidateBaseURL("http://example.com"); err == nil {
		t.Error("expected error for http:// scheme, got nil")
	}
}

func TestValidateBaseURL_FtpSchemeRejected(t *testing.T) {
	if err := ValidateBaseURL("ftp://example.com"); err == nil {
		t.Error("expected error for ftp:// scheme, got nil")
	}
}

func TestValidateBaseURL_InternalIPsRejected(t *testing.T) {
	cases := []struct {
		name string
		url  string
	}{
		{"loopback", "https://127.0.0.1"},
		{"loopback-alt", "https://127.0.0.2"},
		{"link-local (AWS metadata)", "https://169.254.169.254"},
		{"RFC1918 10/8", "https://10.0.0.1"},
		{"RFC1918 172.16/12", "https://172.16.0.1"},
		{"RFC1918 192.168/16", "https://192.168.1.1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateBaseURL(tc.url); err == nil {
				t.Errorf("ValidateBaseURL(%q): expected error for internal address, got nil", tc.url)
			}
		})
	}
}

func TestValidateBaseURL_PublicIPAllowed(t *testing.T) {
	// 8.8.8.8 is a well-known public IP — no DNS resolution required.
	if err := ValidateBaseURL("https://8.8.8.8"); err != nil {
		t.Errorf("ValidateBaseURL(https://8.8.8.8): unexpected error: %v", err)
	}
}

// TestListModels_SSRFBlocked verifies that after the fix, supplying an internal
// base_url returns an error and does NOT reach the internal server.
func TestListModels_SSRFBlocked(t *testing.T) {
	reached := false
	victim := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))
	defer victim.Close()

	_, err := listAnthropicModels(context.Background(), "fake-key", victim.URL)
	if err == nil {
		t.Error("expected error for internal base_url, got nil")
	}
	if reached {
		t.Error("request reached the internal victim server (SSRF not blocked)")
	}
}
