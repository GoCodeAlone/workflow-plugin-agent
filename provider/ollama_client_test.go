package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestOllamaClientListModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/tags" {
			http.Error(w, "unexpected request", http.StatusNotFound)
			return
		}
		if err := json.NewEncoder(w).Encode(map[string]any{
			"models": []map[string]string{
				{"name": "named:latest", "model": "ignored:latest"},
				{"model": "fallback:latest"},
			},
		}); err != nil {
			t.Errorf("encode list response: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	models, err := NewOllamaClient(server.URL).ListModels(t.Context())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	want := []ModelInfo{
		{ID: "named:latest", Name: "named:latest"},
		{ID: "fallback:latest", Name: "fallback:latest"},
	}
	if !reflect.DeepEqual(models, want) {
		t.Fatalf("ListModels = %#v, want %#v", models, want)
	}
}

func TestOllamaClientPullProgress(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/pull" {
			http.Error(w, "unexpected request", http.StatusNotFound)
			return
		}
		var request struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if request.Model != "qwen3:8b" {
			http.Error(w, "unexpected model", http.StatusBadRequest)
			return
		}
		for _, progress := range []map[string]any{
			{"status": "pulling", "total": 200, "completed": 50},
			{"status": "success", "total": 200, "completed": 200},
		} {
			if err := json.NewEncoder(w).Encode(progress); err != nil {
				t.Errorf("encode pull response: %v", err)
				return
			}
		}
	}))
	t.Cleanup(server.Close)

	var progress []float64
	err := NewOllamaClient(server.URL).Pull(t.Context(), "qwen3:8b", func(percent float64) {
		progress = append(progress, percent)
	})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if want := []float64{25, 100}; !reflect.DeepEqual(progress, want) {
		t.Fatalf("progress = %v, want %v", progress, want)
	}
}

func TestOllamaClientHealth(t *testing.T) {
	for _, test := range []struct {
		name    string
		status  int
		wantErr bool
	}{
		{name: "healthy", status: http.StatusOK},
		{name: "server error", status: http.StatusInternalServerError, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodHead || r.URL.Path != "/" {
					http.Error(w, "unexpected request", http.StatusNotFound)
					return
				}
				w.WriteHeader(test.status)
			}))
			t.Cleanup(server.Close)

			err := NewOllamaClient(server.URL).Health(t.Context())
			if (err != nil) != test.wantErr {
				t.Fatalf("Health error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}

func TestOllamaClientResponseErrors(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
	}{
		{name: "server error", status: http.StatusInternalServerError, body: `{"error":"server failed"}`},
		{name: "malformed response", status: http.StatusOK, body: `{"models":`},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				if _, err := io.WriteString(w, test.body); err != nil {
					t.Errorf("write response: %v", err)
				}
			}))
			t.Cleanup(server.Close)

			if _, err := NewOllamaClient(server.URL).ListModels(t.Context()); err == nil {
				t.Fatal("ListModels returned nil error")
			}
		})
	}
}

func TestOllamaClientContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
		w.WriteHeader(http.StatusRequestTimeout)
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := NewOllamaClient(server.URL).ListModels(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ListModels error = %v, want context.Canceled", err)
	}
}

func TestOllamaClientMalformedURLFallsBackToDefault(t *testing.T) {
	originalTransport := http.DefaultTransport
	t.Cleanup(func() {
		http.DefaultTransport = originalTransport
	})

	var requestedURL string
	http.DefaultTransport = ollamaRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		requestedURL = request.URL.String()
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"models":[]}`)),
			Request:    request,
		}, nil
	})

	if _, err := NewOllamaClient("://invalid").ListModels(t.Context()); err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if requestedURL != defaultOllamaClientURL+"/api/tags" {
		t.Fatalf("request URL = %q, want default Ollama URL", requestedURL)
	}
}

type ollamaRoundTripFunc func(*http.Request) (*http.Response, error)

func (f ollamaRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
