package sdk

import (
	"encoding/json"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// EventEntry is one line in the JSONL events file.
type EventEntry struct {
	Seq int             `json:"seq"`
	Ts  string          `json:"ts"`
	Msg json.RawMessage `json:"msg"`
}

// EventWriter appends JSONL event entries to a file in a goroutine-safe manner.
// Each call to Append assigns a monotonically increasing sequence number.
type EventWriter struct {
	f          *os.File
	mu         sync.Mutex
	seqCounter atomic.Int64
}

// NewEventWriter opens (or creates) the file at path in append mode and returns
// an EventWriter ready to use. The caller must call Close when done.
func NewEventWriter(path string) (*EventWriter, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	return &EventWriter{f: f}, nil
}

// Append writes raw as a JSON event entry (with seq + timestamp) and returns
// the assigned sequence number. Errors writing to disk are silently ignored to
// avoid disrupting the caller's read loop.
func (w *EventWriter) Append(raw []byte) int64 {
	seq := w.seqCounter.Add(1)
	entry := EventEntry{
		Seq: int(seq),
		Ts:  time.Now().UTC().Format(time.RFC3339),
		Msg: json.RawMessage(raw),
	}
	line, _ := json.Marshal(entry)
	w.mu.Lock()
	w.f.Write(line)  //nolint:errcheck
	w.f.Write([]byte("\n")) //nolint:errcheck
	w.mu.Unlock()
	return seq
}

// Close flushes and closes the underlying file.
func (w *EventWriter) Close() error {
	return w.f.Close()
}
