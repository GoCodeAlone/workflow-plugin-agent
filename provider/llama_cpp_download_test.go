package provider

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// buildTestZip creates an in-memory zip containing a llama-server binary.
func buildTestZip(t *testing.T, binName string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	f, err := w.Create(binName)
	if err != nil {
		t.Fatalf("zip.Create: %v", err)
	}
	if _, err := f.Write([]byte("#!/bin/sh\necho llama-server")); err != nil {
		t.Fatalf("zip.Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("zip.Close: %v", err)
	}
	return buf.Bytes()
}

// TestAssetNameForPlatform_AllPlatforms verifies platform asset suffix selection.
func TestAssetNameForPlatform_AllPlatforms(t *testing.T) {
	cases := []struct {
		goos   string
		goarch string
		want   string
	}{
		{"darwin", "arm64", "-bin-macos-arm64.zip"},
		{"darwin", "amd64", "-bin-macos-x64.zip"},
		{"windows", "amd64", "-bin-win-avx-x64.zip"},
		{"linux", "amd64", "-bin-linux-amd64.zip"},
		{"linux", "arm64", "-bin-linux-arm64.zip"},
	}
	for _, tc := range cases {
		got := assetNameForPlatform("b3447", tc.goos, tc.goarch)
		if got != tc.want {
			t.Errorf("assetNameForPlatform(%q, %q): want %q, got %q", tc.goos, tc.goarch, tc.want, got)
		}
	}
}

// TestParseLlamaCppRelease_FindsAsset verifies findAssetURL selects the correct asset.
func TestParseLlamaCppRelease_FindsAsset(t *testing.T) {
	tag := "b3447"
	// Build a fake release with multiple platform assets
	release := ghRelease{
		TagName: tag,
		Assets: []ghAsset{
			{Name: "llama-b3447-bin-macos-arm64.zip", BrowserDownloadURL: "https://example.com/macos-arm64.zip"},
			{Name: "llama-b3447-bin-macos-x64.zip", BrowserDownloadURL: "https://example.com/macos-x64.zip"},
			{Name: "llama-b3447-bin-linux-amd64.zip", BrowserDownloadURL: "https://example.com/linux-amd64.zip"},
			{Name: "llama-b3447-bin-linux-arm64.zip", BrowserDownloadURL: "https://example.com/linux-arm64.zip"},
			{Name: "llama-b3447-bin-win-avx-x64.zip", BrowserDownloadURL: "https://example.com/win-x64.zip"},
		},
	}

	// Test current platform
	url, err := findAssetURL(release, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatalf("findAssetURL for current platform: %v", err)
	}
	if url == "" {
		t.Error("expected non-empty URL for current platform")
	}

	// Spot-check specific platforms
	u, err := findAssetURL(release, "darwin", "arm64")
	if err != nil {
		t.Fatalf("darwin/arm64: %v", err)
	}
	if u != "https://example.com/macos-arm64.zip" {
		t.Errorf("darwin/arm64: want macos-arm64 URL, got %q", u)
	}

	u, err = findAssetURL(release, "linux", "amd64")
	if err != nil {
		t.Fatalf("linux/amd64: %v", err)
	}
	if u != "https://example.com/linux-amd64.zip" {
		t.Errorf("linux/amd64: want linux-amd64 URL, got %q", u)
	}
}

// TestParseLlamaCppRelease_NoMatchingAsset verifies an error is returned when no asset matches.
func TestParseLlamaCppRelease_NoMatchingAsset(t *testing.T) {
	release := ghRelease{
		TagName: "b1234",
		Assets: []ghAsset{
			{Name: "llama-b1234-other-platform.zip", BrowserDownloadURL: "https://example.com/other.zip"},
		},
	}
	_, err := findAssetURL(release, "linux", "amd64")
	if err == nil {
		t.Error("expected error when no matching asset")
	}
}

// TestEnsureLlamaServer_DownloadsAndCaches verifies the full download + cache flow.
func TestEnsureLlamaServer_DownloadsAndCaches(t *testing.T) {
	zipData := buildTestZip(t, "llama-server")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(zipData)
	}))
	defer srv.Close()

	origDownloadURL := llamaServerDownloadURL
	origCacheDir := llamaCppCacheDir
	llamaServerDownloadURL = srv.URL + "/llama-server.zip"
	llamaCppCacheDir = t.TempDir()
	defer func() {
		llamaServerDownloadURL = origDownloadURL
		llamaCppCacheDir = origCacheDir
	}()

	path, err := EnsureLlamaServer(context.Background())
	if err != nil {
		t.Fatalf("EnsureLlamaServer: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("binary not found at %s: %v", path, err)
	}

	// Second call should use cache (no new HTTP request).
	path2, err := EnsureLlamaServer(context.Background())
	if err != nil {
		t.Fatalf("EnsureLlamaServer (cached): %v", err)
	}
	if path != path2 {
		t.Errorf("cached path mismatch: %q vs %q", path, path2)
	}
}

// TestEnsureLlamaServer_GitHubAPIFlow verifies that the GitHub API is called and parsed correctly.
func TestEnsureLlamaServer_GitHubAPIFlow(t *testing.T) {
	zipData := buildTestZip(t, "llama-server")

	// Determine the asset suffix for the current platform so we can craft a matching release.
	suffix := assetNameForPlatform("b9999", runtime.GOOS, runtime.GOARCH)
	assetName := "llama-b9999" + suffix

	var zipSrvURL string
	zipSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(zipData)
	}))
	defer zipSrv.Close()
	zipSrvURL = zipSrv.URL + "/download.zip"

	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		release := ghRelease{
			TagName: "b9999",
			Assets: []ghAsset{
				{Name: assetName, BrowserDownloadURL: zipSrvURL},
			},
		}
		_ = json.NewEncoder(w).Encode(release)
	}))
	defer apiSrv.Close()

	origAPI := llamaServerGitHubAPI
	origDownload := llamaServerDownloadURL
	origCacheDir := llamaCppCacheDir
	llamaServerGitHubAPI = apiSrv.URL
	llamaServerDownloadURL = "" // force API path
	llamaCppCacheDir = t.TempDir()
	defer func() {
		llamaServerGitHubAPI = origAPI
		llamaServerDownloadURL = origDownload
		llamaCppCacheDir = origCacheDir
	}()

	path, err := EnsureLlamaServer(context.Background())
	if err != nil {
		t.Fatalf("EnsureLlamaServer via API: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("binary not at %s: %v", path, err)
	}
}

func TestExtractLlamaServerFromZip_Found(t *testing.T) {
	zipData := buildTestZip(t, "llama-server")
	dest := filepath.Join(t.TempDir(), "llama-server")

	if err := extractLlamaServerFromZip(zipData, dest, "b1234"); err != nil {
		t.Fatalf("extractLlamaServerFromZip: %v", err)
	}
	if _, err := os.Stat(dest); err != nil {
		t.Errorf("dest not created: %v", err)
	}
}

func TestExtractLlamaServerFromZip_NotFound(t *testing.T) {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	f, _ := w.Create("other-binary")
	_, _ = f.Write([]byte("data"))
	_ = w.Close()

	dest := filepath.Join(t.TempDir(), "llama-server")
	err := extractLlamaServerFromZip(buf.Bytes(), dest, "b1234")
	if err == nil {
		t.Error("expected error when binary not in zip")
	}
}
