package provider

import "strings"

// ParseThinking extracts <think>...</think> blocks from model output.
// The first block's content becomes thinking; the remainder becomes content.
// If no <think> block is present, all text is returned as content.
func ParseThinking(raw string) (thinking, content string) {
	start := strings.Index(raw, "<think>")
	if start == -1 {
		return "", raw
	}
	end := strings.Index(raw[start:], "</think>")
	if end == -1 {
		// Unclosed tag: treat everything after <think> as thinking.
		return strings.TrimSpace(raw[start+len("<think>"):]), ""
	}
	end += start
	thinking = strings.TrimSpace(raw[start+len("<think>") : end])
	content = strings.TrimSpace(raw[end+len("</think>"):])
	return thinking, content
}

// ThinkingStreamParser tracks state across streaming chunks to split
// thinking vs content tokens. Handles <think>/<think> split across chunks.
type ThinkingStreamParser struct {
	buf     string // accumulated partial tag buffer
	inThink bool   // currently inside <think>...</think>
}

// Feed processes a streaming chunk and returns zero or more StreamEvents.
// Events may be of type "thinking" (Thinking field set) or "text" (Text field set).
func (p *ThinkingStreamParser) Feed(chunk string) []StreamEvent {
	var events []StreamEvent
	p.buf += chunk

	for {
		if p.inThink {
			// Look for </think>
			idx := strings.Index(p.buf, "</think>")
			if idx != -1 {
				if idx > 0 {
					events = append(events, StreamEvent{Type: "thinking", Thinking: p.buf[:idx]})
				}
				p.buf = p.buf[idx+len("</think>"):]
				p.inThink = false
				continue
			}
			// Check if buf ends with a partial "</think>" prefix — hold it back.
			partial := partialSuffix(p.buf, "</think>")
			safe := p.buf[:len(p.buf)-len(partial)]
			if safe != "" {
				events = append(events, StreamEvent{Type: "thinking", Thinking: safe})
			}
			p.buf = partial
			break
		}

		// Not in think — look for <think>
		idx := strings.Index(p.buf, "<think>")
		if idx != -1 {
			if idx > 0 {
				events = append(events, StreamEvent{Type: "text", Text: p.buf[:idx]})
			}
			p.buf = p.buf[idx+len("<think>"):]
			p.inThink = true
			continue
		}
		// Check for partial "<think>" prefix at end.
		partial := partialSuffix(p.buf, "<think>")
		safe := p.buf[:len(p.buf)-len(partial)]
		if safe != "" {
			events = append(events, StreamEvent{Type: "text", Text: safe})
		}
		p.buf = partial
		break
	}

	return events
}

// partialSuffix returns the longest suffix of s that is a prefix of tag.
// Used to avoid splitting a tag across chunk boundaries.
func partialSuffix(s, tag string) string {
	for i := len(tag) - 1; i > 0; i-- {
		if strings.HasSuffix(s, tag[:i]) {
			return tag[:i]
		}
	}
	return ""
}

// LocalAuthMode returns an AuthModeInfo for a local (no-API-key) provider.
func LocalAuthMode(name, displayName string) AuthModeInfo {
	return AuthModeInfo{
		Mode:        name,
		DisplayName: displayName,
		Description: "Local inference — no API key required.",
		ServerSafe:  true,
	}
}
