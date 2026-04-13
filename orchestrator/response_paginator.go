package orchestrator

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// ResponsePaginator caches and paginates large tool responses so they fit
// within the LLM's context window without truncation.
type ResponsePaginator struct {
	mu              sync.Mutex
	cache           map[string]*cachedResponse
	maxResponseSize int // chars; responses larger than this are paginated
	pageSize        int // lines per page
}

type cachedResponse struct {
	items     []string
	createdAt time.Time
}

// NewResponsePaginator creates a paginator sized for the given context window.
// maxResponseSize is set to 20% of the context window (char estimate);
// pageSize targets ~10% of the context window at ~80 chars per line.
func NewResponsePaginator(contextWindow int) *ResponsePaginator {
	pageSize := (contextWindow / 10) / 80
	if pageSize < 10 {
		pageSize = 10
	}
	return &ResponsePaginator{
		cache:           make(map[string]*cachedResponse),
		maxResponseSize: contextWindow / 5,
		pageSize:        pageSize,
	}
}

// Paginate returns result unchanged if it fits within maxResponseSize.
// For larger results it caches the lines and returns page 1 with navigation hints.
// If args contains {"page": N}, it serves page N from the cache instead.
func (rp *ResponsePaginator) Paginate(toolName string, args map[string]any, result string) string {
	if p, ok := args["page"]; ok {
		return rp.getPage(toolName, args, p)
	}
	if len(result) <= rp.maxResponseSize {
		return result
	}
	key := rp.cacheKey(toolName, args)
	lines := strings.Split(result, "\n")
	rp.mu.Lock()
	rp.cache[key] = &cachedResponse{items: lines, createdAt: time.Now()}
	rp.mu.Unlock()
	return rp.formatPage(lines, 1)
}

func (rp *ResponsePaginator) getPage(toolName string, args map[string]any, pageVal any) string {
	var page int
	switch v := pageVal.(type) {
	case int:
		page = v
	case float64:
		page = int(v)
	default:
		return fmt.Sprintf("[pagination error: invalid page value %v]", pageVal)
	}
	key := rp.cacheKey(toolName, args)
	rp.mu.Lock()
	cached, ok := rp.cache[key]
	rp.mu.Unlock()
	if !ok {
		return "[pagination error: page cache expired or not found; re-run the tool without a page argument to rebuild the cache]"
	}
	return rp.formatPage(cached.items, page)
}

func (rp *ResponsePaginator) formatPage(lines []string, page int) string {
	start := (page - 1) * rp.pageSize
	totalPages := (len(lines) + rp.pageSize - 1) / rp.pageSize
	if start >= len(lines) {
		return fmt.Sprintf("[page %d is out of range; total pages: %d]", page, totalPages)
	}
	end := start + rp.pageSize
	if end > len(lines) {
		end = len(lines)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "[Page %d of %d — showing items %d-%d of %d]\n",
		page, totalPages, start+1, end, len(lines))
	for _, line := range lines[start:end] {
		sb.WriteString(line)
		sb.WriteByte('\n')
	}
	if page < totalPages {
		fmt.Fprintf(&sb, "\n[To see next page: call with {\"page\": %d}]", page+1)
	}
	if page > 1 {
		fmt.Fprintf(&sb, "\n[Previous page: {\"page\": %d}]", page-1)
	}
	sb.WriteString("\n[Search within results: {\"query\": \"keyword\"}]")
	return sb.String()
}

// cacheKey builds a stable cache key from toolName + args, excluding page and query params.
func (rp *ResponsePaginator) cacheKey(toolName string, args map[string]any) string {
	parts := make([]string, 0, len(args))
	for k, v := range args {
		if k != "page" && k != "query" {
			parts = append(parts, fmt.Sprintf("%s=%v", k, v))
		}
	}
	sort.Strings(parts)
	return fmt.Sprintf("%s|%s", toolName, strings.Join(parts, ","))
}
