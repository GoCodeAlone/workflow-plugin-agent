package orchestrator

import (
	"fmt"
	"strings"
	"testing"
)

func TestResponsePaginator_SmallResponse_PassThrough(t *testing.T) {
	rp := NewResponsePaginator(128_000)
	result := rp.Paginate("some_tool", map[string]any{}, "small result")
	if result != "small result" {
		t.Errorf("expected pass-through for small result, got %q", result)
	}
}

func TestResponsePaginator_LargeResponse_ReturnsPage1WithNavigation(t *testing.T) {
	rp := &ResponsePaginator{
		cache:           make(map[string]*cachedResponse),
		maxResponseSize: 100,
		pageSize:        5,
	}

	lines := make([]string, 200)
	for i := range lines {
		lines[i] = fmt.Sprintf("item-%d", i+1)
	}
	result := strings.Join(lines, "\n")

	page1 := rp.Paginate("list_step_types", map[string]any{}, result)

	if !strings.Contains(page1, "Page 1 of") {
		t.Errorf("expected page header in response, got: %s", page1)
	}
	if !strings.Contains(page1, `"page": 2`) {
		t.Errorf("expected next-page hint in response, got: %s", page1)
	}
	if !strings.Contains(page1, "item-1") {
		t.Errorf("expected first item in page 1, got: %s", page1)
	}
	if strings.Contains(page1, "item-100") {
		t.Error("page 1 should not contain item-100")
	}
}

func TestResponsePaginator_Page2_FromCache(t *testing.T) {
	rp := &ResponsePaginator{
		cache:           make(map[string]*cachedResponse),
		maxResponseSize: 100,
		pageSize:        5,
	}

	lines := make([]string, 20)
	for i := range lines {
		lines[i] = fmt.Sprintf("item-%d", i+1)
	}
	result := strings.Join(lines, "\n")

	// First call populates the cache.
	rp.Paginate("list_step_types", map[string]any{}, result)

	// Second call with page=2 reads from cache.
	page2 := rp.Paginate("list_step_types", map[string]any{"page": 2}, "ignored")
	if !strings.Contains(page2, "Page 2 of") {
		t.Errorf("expected page 2 header, got: %s", page2)
	}
	if !strings.Contains(page2, "item-6") {
		t.Errorf("expected item-6 in page 2, got: %s", page2)
	}
	if !strings.Contains(page2, `"page": 1`) {
		t.Errorf("expected previous-page hint in page 2, got: %s", page2)
	}
}

func TestResponsePaginator_CacheKey_ExcludesPageAndQuery(t *testing.T) {
	rp := &ResponsePaginator{cache: make(map[string]*cachedResponse)}

	key1 := rp.cacheKey("tool", map[string]any{"workspace": "/tmp"})
	key2 := rp.cacheKey("tool", map[string]any{"workspace": "/tmp", "page": 3})
	key3 := rp.cacheKey("tool", map[string]any{"workspace": "/tmp", "query": "db"})

	if key1 != key2 {
		t.Errorf("page param should not affect cache key: %q vs %q", key1, key2)
	}
	if key1 != key3 {
		t.Errorf("query param should not affect cache key: %q vs %q", key1, key3)
	}
}

func TestResponsePaginator_OutOfRangePage(t *testing.T) {
	rp := &ResponsePaginator{
		cache:           make(map[string]*cachedResponse),
		maxResponseSize: 10,
		pageSize:        2,
	}
	lines := make([]string, 4)
	for i := range lines {
		lines[i] = fmt.Sprintf("item-%d", i+1)
	}
	result := strings.Join(lines, "\n")

	// Populate cache.
	rp.Paginate("tool", map[string]any{}, result)

	resp := rp.Paginate("tool", map[string]any{"page": 99}, "ignored")
	if !strings.Contains(resp, "out of range") {
		t.Errorf("expected out-of-range message, got: %s", resp)
	}
}
