package provider

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

var huggingFaceBaseURL = "https://huggingface.co"

// HuggingFaceBaseURL returns the base URL used for HuggingFace downloads.
func HuggingFaceBaseURL() string { return huggingFaceBaseURL }

// SetHuggingFaceBaseURL overrides the base URL (used in tests).
func SetHuggingFaceBaseURL(url string) { huggingFaceBaseURL = url }

// DownloadHuggingFaceFile downloads any file from a HuggingFace model repo.
// The file is saved to outputDir/<repo-slug>/<filename> where repo-slug replaces
// "/" with "--". If the file already exists it is returned as-is. Downloads resume
// from a .part temp file if interrupted. The optional progress callback receives
// completion percentage (0.0–1.0).
func DownloadHuggingFaceFile(ctx context.Context, repo, filename, outputDir string, progress func(pct float64)) (string, error) {
	if outputDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("huggingface: resolve home dir: %w", err)
		}
		outputDir = filepath.Join(home, ".cache", "workflow", "models")
	}

	// Sanitize filename to prevent path traversal.
	safeName := filepath.Base(filename)
	if safeName != filename || strings.Contains(filename, "..") {
		return "", fmt.Errorf("huggingface: invalid filename %q (must not contain path separators or '..')", filename)
	}

	repoSlug := strings.ReplaceAll(repo, "/", "--")
	destDir := filepath.Join(outputDir, repoSlug)
	destPath := filepath.Join(destDir, safeName)

	// Already downloaded — skip.
	if _, err := os.Stat(destPath); err == nil {
		if progress != nil {
			progress(1.0)
		}
		return destPath, nil
	}

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", fmt.Errorf("huggingface: create output dir: %w", err)
	}

	url := fmt.Sprintf("%s/%s/resolve/main/%s", huggingFaceBaseURL, repo, filename)
	partPath := destPath + ".part"

	// Resume: check existing .part file size.
	var resumeOffset int64
	if fi, err := os.Stat(partPath); err == nil {
		resumeOffset = fi.Size()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("huggingface: create request: %w", err)
	}
	if resumeOffset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", resumeOffset))
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("huggingface: download request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return "", fmt.Errorf("huggingface: unexpected status %d for %s", resp.StatusCode, url)
	}

	// Determine total size for progress reporting.
	var totalSize int64
	switch resp.StatusCode {
	case http.StatusPartialContent:
		// Content-Range: bytes <start>-<end>/<total>
		cr := resp.Header.Get("Content-Range")
		if _, err := fmt.Sscanf(cr, "bytes %*d-%*d/%d", &totalSize); err != nil {
			totalSize = 0
		}
	default:
		totalSize = resp.ContentLength
	}

	flag := os.O_CREATE | os.O_WRONLY
	if resumeOffset > 0 && resp.StatusCode == http.StatusPartialContent {
		flag |= os.O_APPEND
	} else {
		flag |= os.O_TRUNC
		resumeOffset = 0
	}

	f, err := os.OpenFile(partPath, flag, 0o644)
	if err != nil {
		return "", fmt.Errorf("huggingface: open part file: %w", err)
	}
	defer func() { _ = f.Close() }()

	var written int64
	buf := make([]byte, 32*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := f.Write(buf[:n]); writeErr != nil {
				return "", fmt.Errorf("huggingface: write part file: %w", writeErr)
			}
			written += int64(n)
			if progress != nil && totalSize > 0 {
				pct := float64(resumeOffset+written) / float64(totalSize)
				if pct > 1.0 {
					pct = 1.0
				}
				progress(pct)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", fmt.Errorf("huggingface: read response body: %w", readErr)
		}
	}

	if err := f.Close(); err != nil {
		return "", fmt.Errorf("huggingface: close part file: %w", err)
	}
	if err := os.Rename(partPath, destPath); err != nil {
		return "", fmt.Errorf("huggingface: rename part file: %w", err)
	}

	if progress != nil {
		progress(1.0)
	}
	return destPath, nil
}
