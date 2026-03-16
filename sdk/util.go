package sdk

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// WSURLToHTTP converts a WebSocket URL to its HTTP equivalent and strips the
// path so that only the base URL (scheme + host) is returned.
// Examples:
//
//	ws://localhost:8080/ws  →  http://localhost:8080
//	wss://api.example.com/ws  →  https://api.example.com
func WSURLToHTTP(wsURL string) string {
	s := strings.Replace(wsURL, "ws://", "http://", 1)
	s = strings.Replace(s, "wss://", "https://", 1)
	if idx := strings.Index(s, "/ws"); idx != -1 {
		s = s[:idx]
	}
	return s
}

// HTTPGET performs a GET request and returns the response body as a compact JSON
// string if the body is valid JSON, or the raw trimmed string otherwise.
func HTTPGET(url string) (string, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var sb strings.Builder
	if _, err := io.Copy(&sb, resp.Body); err != nil {
		return "", err
	}
	body := strings.TrimSpace(sb.String())
	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(body)); err == nil {
		return buf.String(), nil
	}
	return body, nil
}

// IsTerminal returns true if f is connected to a TTY (best-effort; defaults to false).
func IsTerminal(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
