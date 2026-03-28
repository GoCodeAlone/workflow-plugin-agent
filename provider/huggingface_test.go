package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDownloadHuggingFaceFile_BasicDownload(t *testing.T) {
	content := []byte("fake model data content")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(content)))
		_, _ = w.Write(content)
	}))
	defer srv.Close()

	origURL := huggingFaceBaseURL
	SetHuggingFaceBaseURL(srv.URL)
	defer SetHuggingFaceBaseURL(origURL)

	outDir := t.TempDir()
	var lastPct float64
	path, err := DownloadHuggingFaceFile(context.Background(), "org/model", "model.gguf", outDir, func(pct float64) {
		lastPct = pct
	})
	if err != nil {
		t.Fatalf("DownloadHuggingFaceFile: %v", err)
	}

	expected := filepath.Join(outDir, "org--model", "model.gguf")
	if path != expected {
		t.Errorf("path: want %q, got %q", expected, path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read downloaded file: %v", err)
	}
	if string(data) != string(content) {
		t.Errorf("content: want %q, got %q", content, data)
	}

	if lastPct != 1.0 {
		t.Errorf("final progress: want 1.0, got %v", lastPct)
	}
}

func TestDownloadHuggingFaceFile_AlreadyExists(t *testing.T) {
	outDir := t.TempDir()
	destDir := filepath.Join(outDir, "org--model")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatal(err)
	}
	destPath := filepath.Join(destDir, "model.gguf")
	if err := os.WriteFile(destPath, []byte("existing content"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Server should NOT be contacted.
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	origURL := huggingFaceBaseURL
	SetHuggingFaceBaseURL(srv.URL)
	defer SetHuggingFaceBaseURL(origURL)

	path, err := DownloadHuggingFaceFile(context.Background(), "org/model", "model.gguf", outDir, nil)
	if err != nil {
		t.Fatalf("DownloadHuggingFaceFile: %v", err)
	}
	if path != destPath {
		t.Errorf("path: want %q, got %q", destPath, path)
	}
	if callCount != 0 {
		t.Errorf("expected no HTTP calls for already-existing file, got %d", callCount)
	}
}

func TestDownloadHuggingFaceFile_ProgressCallback(t *testing.T) {
	content := make([]byte, 1024)
	for i := range content {
		content[i] = byte(i % 256)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(content)))
		_, _ = w.Write(content)
	}))
	defer srv.Close()

	origURL := huggingFaceBaseURL
	SetHuggingFaceBaseURL(srv.URL)
	defer SetHuggingFaceBaseURL(origURL)

	outDir := t.TempDir()
	var progressCalls int
	_, err := DownloadHuggingFaceFile(context.Background(), "myorg/mymodel", "weights.bin", outDir, func(pct float64) {
		progressCalls++
		if pct < 0 || pct > 1 {
			t.Errorf("progress out of range: %v", pct)
		}
	})
	if err != nil {
		t.Fatalf("DownloadHuggingFaceFile: %v", err)
	}
	if progressCalls == 0 {
		t.Error("expected at least one progress callback")
	}
}

// TestDownloadHuggingFaceFile_Resume verifies Range header is sent and partial content is appended.
func TestDownloadHuggingFaceFile_Resume(t *testing.T) {
	// First 5 bytes are "hello", remaining 5 are " world"
	firstPart := []byte("hello")
	secondPart := []byte(" world")
	fullContent := append(firstPart, secondPart...)

	outDir := t.TempDir()
	destDir := filepath.Join(outDir, "org--repo")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Write the .part file with the first 5 bytes already present
	partPath := filepath.Join(destDir, "file.bin.part")
	if err := os.WriteFile(partPath, firstPart, 0o644); err != nil {
		t.Fatal(err)
	}

	var receivedRangeHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedRangeHeader = r.Header.Get("Range")
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", len(firstPart), len(fullContent)-1, len(fullContent)))
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(secondPart)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(secondPart)
	}))
	defer srv.Close()

	origURL := huggingFaceBaseURL
	SetHuggingFaceBaseURL(srv.URL)
	defer SetHuggingFaceBaseURL(origURL)

	path, err := DownloadHuggingFaceFile(context.Background(), "org/repo", "file.bin", outDir, nil)
	if err != nil {
		t.Fatalf("DownloadHuggingFaceFile resume: %v", err)
	}

	// Verify the Range header was sent
	if receivedRangeHeader == "" {
		t.Error("expected Range header to be sent")
	}
	if !strings.HasPrefix(receivedRangeHeader, fmt.Sprintf("bytes=%d-", len(firstPart))) {
		t.Errorf("Range header: want bytes=%d-, got %q", len(firstPart), receivedRangeHeader)
	}

	// Verify final file contains the complete content
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read final file: %v", err)
	}
	if string(data) != string(fullContent) {
		t.Errorf("file content: want %q, got %q", fullContent, data)
	}
}

// TestDownloadHuggingFaceFile_ErrorStatus verifies that a non-200 response returns an error.
func TestDownloadHuggingFaceFile_ErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r) // 404
	}))
	defer srv.Close()

	origURL := huggingFaceBaseURL
	SetHuggingFaceBaseURL(srv.URL)
	defer SetHuggingFaceBaseURL(origURL)

	outDir := t.TempDir()
	_, err := DownloadHuggingFaceFile(context.Background(), "org/model", "missing.gguf", outDir, nil)
	if err == nil {
		t.Error("expected error for 404 response")
	}
}

// TestDownloadHuggingFaceFile_DefaultOutputDir verifies fallback to ~/.cache/workflow/models/.
func TestDownloadHuggingFaceFile_DefaultOutputDir(t *testing.T) {
	content := []byte("small model")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(content)))
		_, _ = w.Write(content)
	}))
	defer srv.Close()

	origURL := huggingFaceBaseURL
	SetHuggingFaceBaseURL(srv.URL)
	defer SetHuggingFaceBaseURL(origURL)

	// Override HOME so we don't pollute real user cache
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	path, err := DownloadHuggingFaceFile(context.Background(), "org/model", "tiny.bin", "", nil)
	if err != nil {
		t.Fatalf("DownloadHuggingFaceFile default dir: %v", err)
	}

	// Path must be under ~/.cache/workflow/models/
	expectedPrefix := filepath.Join(tmpHome, ".cache", "workflow", "models")
	if !strings.HasPrefix(path, expectedPrefix) {
		t.Errorf("path %q should be under %q", path, expectedPrefix)
	}

	// Cleanup
	_ = os.RemoveAll(expectedPrefix)
}
