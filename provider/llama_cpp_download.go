package provider

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// llama.cpp pinned version — bumped when we want to track a new release.
const llamaCppPinnedVersion = "b8586"

var (
	llamaServerGitHubAPI   = "https://api.github.com/repos/ggerganov/llama.cpp/releases/tags/" + llamaCppPinnedVersion
	llamaServerDownloadURL = "" // empty = derive from GitHub API; set in tests
	llamaCppCacheDir       = "" // empty = use default ~/.cache/...; set in tests
)

// ghRelease is the JSON response from the GitHub releases API.
type ghRelease struct {
	TagName string    `json:"tag_name"`
	Assets  []ghAsset `json:"assets"`
}

// ghAsset represents a single downloadable asset in a GitHub release.
type ghAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// EnsureLlamaServer finds or downloads the llama-server binary.
// Search order: PATH (checked by caller) → cache dir → download from GitHub releases.
func EnsureLlamaServer(ctx context.Context) (string, error) {
	cacheDir, err := llamaServerCacheDir()
	if err != nil {
		return "", err
	}

	binName := "llama-server"
	if runtime.GOOS == "windows" {
		binName = "llama-server.exe"
	}
	cachedBin := filepath.Join(cacheDir, binName)

	if _, err := os.Stat(cachedBin); err == nil {
		return cachedBin, nil
	}

	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", fmt.Errorf("llama_cpp_download: create cache dir: %w", err)
	}

	downloadURL, version, err := resolveLlamaServerURL(ctx)
	if err != nil {
		return "", err
	}

	data, err := downloadBytes(ctx, downloadURL)
	if err != nil {
		return "", fmt.Errorf("llama_cpp_download: download %s: %w", downloadURL, err)
	}

	if err := extractLlamaServer(data, cachedBin, version, strings.HasSuffix(downloadURL, ".tar.gz")); err != nil {
		return "", fmt.Errorf("llama_cpp_download: extract binary: %w", err)
	}

	if err := os.Chmod(cachedBin, 0o755); err != nil {
		return "", fmt.Errorf("llama_cpp_download: chmod binary: %w", err)
	}

	return cachedBin, nil
}

// llamaServerCacheDir returns the cache directory for the llama-server binary.
// Uses llamaCppCacheDir override when set (for tests), otherwise ~/.cache/workflow/llama-server/<version>.
func llamaServerCacheDir() (string, error) {
	if llamaCppCacheDir != "" {
		return filepath.Join(llamaCppCacheDir, llamaCppPinnedVersion), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("llama_cpp_download: resolve home dir: %w", err)
	}
	return filepath.Join(home, ".cache", "workflow", "llama-server", llamaCppPinnedVersion), nil
}

// resolveLlamaServerURL returns the download URL and version tag for the current platform.
// If llamaServerDownloadURL is set (tests), it is used directly.
func resolveLlamaServerURL(ctx context.Context) (url, version string, err error) {
	if llamaServerDownloadURL != "" {
		return llamaServerDownloadURL, llamaCppPinnedVersion, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, llamaServerGitHubAPI, nil)
	if err != nil {
		return "", "", fmt.Errorf("llama_cpp_download: create GitHub API request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "workflow-plugin-agent")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("llama_cpp_download: GitHub API request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("llama_cpp_download: GitHub API status %d", resp.StatusCode)
	}

	var release ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", "", fmt.Errorf("llama_cpp_download: parse GitHub API response: %w", err)
	}

	assetURL, findErr := findAssetURL(release, runtime.GOOS, runtime.GOARCH)
	if findErr != nil {
		return "", "", findErr
	}
	return assetURL, release.TagName, nil
}

// findAssetURL returns the browser_download_url for the asset matching goos/goarch.
// Tries tar.gz first (newer releases), then falls back to zip (older releases).
func findAssetURL(release ghRelease, goos, goarch string) (string, error) {
	suffixes := assetSuffixesForPlatform(goos, goarch)
	for _, suffix := range suffixes {
		for _, asset := range release.Assets {
			if strings.HasSuffix(asset.Name, suffix) {
				return asset.BrowserDownloadURL, nil
			}
		}
	}
	return "", fmt.Errorf("llama_cpp_download: no asset matching suffixes %v for platform %s/%s in release %s",
		suffixes, goos, goarch, release.TagName)
}

// assetSuffixesForPlatform returns candidate asset suffixes in priority order.
// Newer llama.cpp releases (b4000+) use .tar.gz, older ones use .zip.
func assetSuffixesForPlatform(goos, goarch string) []string {
	switch goos {
	case "darwin":
		if goarch == "arm64" {
			return []string{"-bin-macos-arm64.tar.gz", "-bin-macos-arm64.zip"}
		}
		return []string{"-bin-macos-x64.tar.gz", "-bin-macos-x64.zip"}
	case "windows":
		return []string{"-bin-win-avx-x64.zip"} // Windows likely still zip
	default: // linux
		if goarch == "arm64" {
			return []string{"-bin-linux-arm64.tar.gz", "-bin-linux-arm64.zip"}
		}
		return []string{"-bin-linux-amd64.tar.gz", "-bin-linux-amd64.zip"}
	}
}

// assetNameForPlatform returns the expected asset filename suffix for the given OS/arch.
// Deprecated: use assetSuffixesForPlatform for multi-format support.
func assetNameForPlatform(tag, goos, goarch string) string {
	_ = tag
	suffixes := assetSuffixesForPlatform(goos, goarch)
	if len(suffixes) > 0 {
		return suffixes[0]
	}
	return ""
}

// downloadBytes fetches a URL and returns the full body as bytes.
func downloadBytes(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// extractLlamaServer extracts the llama-server binary from either tar.gz or zip data.
func extractLlamaServer(data []byte, destPath, version string, isTarGz bool) error {
	if isTarGz {
		return extractLlamaServerFromTarGz(data, destPath, version)
	}
	return extractLlamaServerFromZip(data, destPath, version)
}

// extractLlamaServerFromTarGz finds the llama-server binary inside tar.gz data and writes it to destPath.
func extractLlamaServerFromTarGz(data []byte, destPath, version string) error {
	gr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("open gzip: %w", err)
	}
	defer func() { _ = gr.Close() }()

	tr := tar.NewReader(gr)
	binName := "llama-server"
	if runtime.GOOS == "windows" {
		binName = "llama-server.exe"
	}

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar entry: %w", err)
		}
		if filepath.Base(header.Name) != binName {
			continue
		}
		out, err := os.Create(destPath)
		if err != nil {
			return fmt.Errorf("create dest file: %w", err)
		}
		defer func() { _ = out.Close() }()
		if _, err := io.Copy(out, tr); err != nil {
			return fmt.Errorf("copy binary: %w", err)
		}
		return nil
	}
	return fmt.Errorf("llama-server binary not found in tar.gz archive (version %s)", version)
}

// extractLlamaServerFromZip finds the llama-server binary inside zipData and writes it to destPath.
func extractLlamaServerFromZip(zipData []byte, destPath, version string) error {
	r, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}

	binName := "llama-server"
	if runtime.GOOS == "windows" {
		binName = "llama-server.exe"
	}

	for _, f := range r.File {
		if filepath.Base(f.Name) != binName {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("open zip entry %s: %w", f.Name, err)
		}
		defer func() { _ = rc.Close() }()

		out, err := os.Create(destPath)
		if err != nil {
			return fmt.Errorf("create dest file: %w", err)
		}
		defer func() { _ = out.Close() }()

		if _, err := io.Copy(out, rc); err != nil {
			return fmt.Errorf("copy binary: %w", err)
		}
		return nil
	}
	return fmt.Errorf("llama-server binary not found in archive (version %s)", version)
}
