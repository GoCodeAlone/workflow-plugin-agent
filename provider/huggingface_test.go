package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestDownloadHuggingFaceFile_BasicDownload(t *testing.T) {
	content := []byte("fake model data content")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "23")
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
		w.Header().Set("Content-Length", "1024")
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
