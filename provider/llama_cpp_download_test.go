package provider

import (
	"archive/zip"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestEnsureLlamaServer_DownloadsAndCaches(t *testing.T) {
	zipData := buildTestZip(t, "llama-server")

	// Serve the zip
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(zipData)
	}))
	defer srv.Close()

	// Override globals for this test
	origDownloadURL := llamaServerDownloadURL
	llamaServerDownloadURL = srv.URL + "/llama-server.zip"
	defer func() { llamaServerDownloadURL = origDownloadURL }()

	// Override cache dir by redirecting home via env (macOS/Linux)
	tmpHome := t.TempDir()
	origHome := os.Getenv("HOME")
	t.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	path, err := EnsureLlamaServer(context.Background())
	if err != nil {
		t.Fatalf("EnsureLlamaServer: %v", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Errorf("binary not found at %s: %v", path, err)
	}

	// Call again — should use cache, no new HTTP request.
	path2, err := EnsureLlamaServer(context.Background())
	if err != nil {
		t.Fatalf("EnsureLlamaServer (cached): %v", err)
	}
	if path != path2 {
		t.Errorf("cached path mismatch: %q vs %q", path, path2)
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
	// Zip with wrong binary name
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
