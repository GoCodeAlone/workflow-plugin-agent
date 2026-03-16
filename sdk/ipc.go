package sdk

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"time"
)

// CommandHandler is called by IPCServer for each incoming command.
// cmd is the raw command string sent by the client.
// sess is the current session state (may be modified in place; caller saves it).
// Return (response, nil) to send a JSON response, or (nil, err) to send an error.
type CommandHandler func(cmd string, sess *SessionState) (response []byte, err error)

// IPCServer listens on a Unix socket and dispatches incoming commands to a
// CommandHandler.  Each connection is handled in its own goroutine.
type IPCServer struct {
	listener net.Listener
	sockPath string
}

// NewIPCServer creates a Unix socket listener at sockPath, removing any stale
// socket file first.
func NewIPCServer(sockPath string) (*IPCServer, error) {
	os.Remove(sockPath) // clean up leftover socket
	l, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on unix socket %s: %w", sockPath, err)
	}
	return &IPCServer{listener: l, sockPath: sockPath}, nil
}

// Serve accepts connections and calls handler for each command.
// It blocks until the listener is closed.  Callers typically run it in a goroutine.
// The socket file is removed when Serve returns.
func (s *IPCServer) Serve(handler CommandHandler) {
	defer func() {
		s.listener.Close()
		os.Remove(s.sockPath)
	}()
	for {
		c, err := s.listener.Accept()
		if err != nil {
			return
		}
		go handleIPCConn(c, handler)
	}
}

// Close stops the server.
func (s *IPCServer) Close() error { return s.listener.Close() }

func handleIPCConn(c net.Conn, handler CommandHandler) {
	defer c.Close()
	c.SetDeadline(time.Now().Add(15 * time.Second)) //nolint:errcheck

	scanner := bufio.NewScanner(c)
	if !scanner.Scan() {
		return
	}
	line := strings.TrimSpace(scanner.Text())
	if line == "" {
		return
	}

	var req struct {
		Cmd string `json:"cmd"`
	}
	if err := json.Unmarshal([]byte(line), &req); err != nil {
		ipcReplyErr(c, fmt.Sprintf("invalid request: %v", err))
		return
	}

	// Build a temporary session for the handler so it can read/update state.
	sess := SessionState{}
	resp, err := handler(req.Cmd, &sess)
	if err != nil {
		ipcReplyErr(c, err.Error())
		return
	}
	if resp == nil {
		ipcReplyErr(c, "command produced no response")
		return
	}
	ipcReplyRaw(c, resp)
}

func ipcReplyErr(c net.Conn, msg string) {
	data, _ := json.Marshal(map[string]any{"error": msg})
	c.Write(data)       //nolint:errcheck
	c.Write([]byte("\n")) //nolint:errcheck
}

func ipcReplyRaw(c net.Conn, raw []byte) {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err == nil {
		data, _ := json.Marshal(map[string]any{"response": json.RawMessage(buf.Bytes())})
		c.Write(data) //nolint:errcheck
	} else {
		data, _ := json.Marshal(map[string]any{"response": string(raw)})
		c.Write(data) //nolint:errcheck
	}
	c.Write([]byte("\n")) //nolint:errcheck
}

// CommandClient sends commands to a running daemon via its Unix socket.
type CommandClient struct {
	sockPath string
}

// NewCommandClient creates a CommandClient that talks to the daemon at sockPath.
func NewCommandClient(sockPath string) *CommandClient {
	return &CommandClient{sockPath: sockPath}
}

// Send sends cmd to the daemon and returns the response bytes.
// Returns an error if the daemon is not reachable or the command fails.
func (c *CommandClient) Send(cmd string) ([]byte, error) {
	conn, err := net.DialTimeout("unix", c.sockPath, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to daemon at %s: %w", c.sockPath, err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(15 * time.Second)) //nolint:errcheck

	req, _ := json.Marshal(map[string]any{"cmd": cmd})
	conn.Write(req)         //nolint:errcheck
	conn.Write([]byte("\n")) //nolint:errcheck

	// Game state JSON can be large; use a 1MB scanner buffer.
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 1<<20), 1<<20)
	if !scanner.Scan() {
		return nil, fmt.Errorf("no response from daemon")
	}

	var resp struct {
		Response json.RawMessage `json:"response"`
		Error    string          `json:"error"`
	}
	if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
		return scanner.Bytes(), nil // return raw on decode failure
	}
	if resp.Error != "" {
		return nil, fmt.Errorf("%s", resp.Error)
	}
	return resp.Response, nil
}

// IsRunning returns true if a daemon is listening on the socket path.
func (c *CommandClient) IsRunning() bool {
	if _, err := os.Stat(c.sockPath); err != nil {
		return false
	}
	conn, err := net.DialTimeout("unix", c.sockPath, time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// SocketPath returns the canonical Unix socket path for a given session file path.
// Uses the first 12 hex chars of a SHA-256 hash to keep the path short and unique.
func SocketPath(sessionPath string) string {
	h := sha256.Sum256([]byte(sessionPath))
	return fmt.Sprintf("/tmp/daemon-%x.sock", h[:6])
}
