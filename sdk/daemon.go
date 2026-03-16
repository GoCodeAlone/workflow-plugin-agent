package sdk

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// DaemonConfig configures a DaemonClient.
type DaemonConfig struct {
	// WSURL is the WebSocket URL to connect to (e.g. "ws://localhost:8080/ws").
	WSURL string
	// SessionPath is the path to the session JSON file.
	// The daemon also writes a PID file at SessionPath+".pid" and events at
	// SessionPath+".events".
	SessionPath string
	// OnMessage is called for every WebSocket message received, including the
	// welcome message.  It is called from the dispatcher goroutine; do not block.
	OnMessage func(data []byte)
	// BroadcastFilter returns true if data is a known server broadcast that
	// should never be treated as a direct response to a command.  Used by the
	// IPC server to skip stale messages while waiting for command responses.
	// If nil, no filtering is applied.
	BroadcastFilter func(data []byte) bool
}

// ResponseSubscriber receives a copy of every WebSocket message via fan-out.
// Register one with DaemonClient.Subscribe before sending a command to ensure
// no messages are missed between send and receive.
type ResponseSubscriber struct {
	ch chan []byte
}

// C returns the channel on which messages are delivered.
func (s *ResponseSubscriber) C() <-chan []byte { return s.ch }

// DaemonClient maintains a persistent WebSocket connection, appends all
// received messages to an events file, and provides fan-out delivery to
// registered subscribers.
type DaemonClient struct {
	cfg         DaemonConfig
	conn        *websocket.Conn
	connID      string
	sess        SessionState
	evw         *EventWriter
	wsMsgCh     chan []byte
	wsErrCh     chan error
	subsMu      sync.Mutex
	subs        []*ResponseSubscriber
}

// NewDaemonClient creates a DaemonClient with the given configuration.
// Call Run to start it.
func NewDaemonClient(cfg DaemonConfig) *DaemonClient {
	return &DaemonClient{
		cfg:     cfg,
		wsMsgCh: make(chan []byte, 256),
		wsErrCh: make(chan error, 1),
	}
}

// Run connects to the WebSocket, performs session resume if a token is stored,
// writes a PID file, opens the events file, then blocks until ctx is cancelled
// or the WebSocket connection is lost.
func (d *DaemonClient) Run(ctx context.Context) error {
	conn, _, err := websocket.DefaultDialer.Dial(d.cfg.WSURL, nil)
	if err != nil {
		return fmt.Errorf("failed to connect to %s: %w", d.cfg.WSURL, err)
	}
	defer conn.Close()
	d.conn = conn

	// Read welcome message.
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, msg, err := conn.ReadMessage()
	conn.SetReadDeadline(time.Time{})
	if err != nil {
		return fmt.Errorf("failed to read welcome: %w", err)
	}

	var welcome map[string]any
	json.Unmarshal(msg, &welcome) //nolint:errcheck
	d.connID, _ = welcome["connectionId"].(string)

	// Persist connection ID.
	d.sess = LoadSession(d.cfg.SessionPath)
	d.sess.LastConnectionID = d.connID
	if err := SaveSession(d.cfg.SessionPath, d.sess); err != nil {
		return fmt.Errorf("failed to save session: %w", err)
	}

	// Attempt session resume if we have a stored token.
	if d.sess.Token != "" {
		resumeCmd, _ := json.Marshal(map[string]any{
			"type":  "session_resume",
			"token": d.sess.Token,
		})
		if err := conn.WriteMessage(websocket.TextMessage, resumeCmd); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to send session_resume: %v\n", err)
		} else {
			conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			_, resumeResp, err := conn.ReadMessage()
			conn.SetReadDeadline(time.Time{})
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: no response to session_resume: %v\n", err)
			} else {
				var r map[string]any
				json.Unmarshal(resumeResp, &r) //nolint:errcheck
				if rType, _ := r["type"].(string); rType == "session_resumed" {
					fmt.Fprintf(os.Stderr, "session resumed: gameId=%s playerId=%s\n",
						r["gameId"], r["playerId"])
				} else {
					fmt.Fprintf(os.Stderr, "session_resume failed, continuing as new connection\n")
					d.sess.Token = ""
					SaveSession(d.cfg.SessionPath, d.sess) //nolint:errcheck
				}
			}
		}
	}

	// Write PID file.
	pidPath := d.cfg.SessionPath + ".pid"
	if err := os.WriteFile(pidPath, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o600); err != nil {
		return fmt.Errorf("failed to write pid file: %w", err)
	}
	defer os.Remove(pidPath)

	// Open events file.
	eventsPath := d.cfg.SessionPath + ".events"
	evw, err := NewEventWriter(eventsPath)
	if err != nil {
		return fmt.Errorf("failed to open events file: %w", err)
	}
	defer evw.Close()
	d.evw = evw

	// Welcome message is the first event.
	evw.Append(msg)
	if d.cfg.OnMessage != nil {
		d.cfg.OnMessage(msg)
	}

	// WS reader goroutine — sole reader of the connection.
	go func() {
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				d.wsErrCh <- err
				return
			}
			d.wsMsgCh <- data
		}
	}()

	// Dispatcher goroutine — appends to events file and fans out.
	go func() {
		for data := range d.wsMsgCh {
			evw.Append(data)
			if d.cfg.OnMessage != nil {
				d.cfg.OnMessage(data)
			}
			d.fanOut(data)
		}
	}()

	fmt.Fprintf(os.Stderr, "daemon started: connectionId=%s events=%s\n", d.connID, eventsPath)

	// Block until ctx or WS error.
	select {
	case <-ctx.Done():
		fmt.Fprintf(os.Stderr, "daemon shutting down\n")
	case wsErr := <-d.wsErrCh:
		fmt.Fprintf(os.Stderr, "daemon WebSocket error: %v\n", wsErr)
	}
	return nil
}

// Subscribe registers a new ResponseSubscriber to receive all subsequent
// WebSocket messages.  Unsubscribe when done to avoid leaking goroutines.
func (d *DaemonClient) Subscribe() *ResponseSubscriber {
	sub := &ResponseSubscriber{ch: make(chan []byte, 64)}
	d.subsMu.Lock()
	d.subs = append(d.subs, sub)
	d.subsMu.Unlock()
	return sub
}

// Unsubscribe removes a subscriber registered with Subscribe.
func (d *DaemonClient) Unsubscribe(sub *ResponseSubscriber) {
	d.subsMu.Lock()
	for i, s := range d.subs {
		if s == sub {
			d.subs = append(d.subs[:i], d.subs[i+1:]...)
			break
		}
	}
	d.subsMu.Unlock()
}

// SendWS sends raw bytes as a WebSocket text message.
func (d *DaemonClient) SendWS(data []byte) error {
	if d.conn == nil {
		return fmt.Errorf("not connected")
	}
	return d.conn.WriteMessage(websocket.TextMessage, data)
}

// WSConn returns the underlying WebSocket connection.
// Do NOT read from it directly — the daemon's reader goroutine is the sole reader.
func (d *DaemonClient) WSConn() *websocket.Conn { return d.conn }

// ConnectionID returns the server-assigned connection ID received in the welcome message.
func (d *DaemonClient) ConnectionID() string { return d.connID }

// Session returns a pointer to the current session state.
func (d *DaemonClient) Session() *SessionState { return &d.sess }

// fanOut delivers a copy of data to every registered subscriber, dropping the
// message if a subscriber's buffer is full.
func (d *DaemonClient) fanOut(data []byte) {
	d.subsMu.Lock()
	for _, sub := range d.subs {
		select {
		case sub.ch <- append([]byte(nil), data...):
		default: // drop — subscriber is stuck
		}
	}
	d.subsMu.Unlock()
}

// IsBroadcast delegates to DaemonConfig.BroadcastFilter. Returns false if no
// filter is configured.
func (d *DaemonClient) IsBroadcast(data []byte) bool {
	if d.cfg.BroadcastFilter == nil {
		return false
	}
	return d.cfg.BroadcastFilter(data)
}
