package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

type execEvent struct {
	SandboxID string `json:"sandbox_id"`
	ExecID    string `json:"exec_id"`
	Type      string `json:"type"`
	Stream    string `json:"stream,omitempty"`
	Data      string `json:"data,omitempty"`
	ExitCode  int    `json:"exit_code,omitempty"`
	Time      string `json:"time"`
}

type execState struct {
	stdoutOff    int64
	stderrOff    int64
	exitSent     bool
	startSent    bool
	exitCode     int
	exitSeen     bool
	lastActivity time.Time
	exitReadyAt  time.Time
}

func main() {
	sandboxID := getenv("SBX_SANDBOX_ID", "")
	endpoint := getenv("SBX_STREAM_ENDPOINT", "")
	eventsDir := getenv("SBX_EVENTS_DIR", "/sbx-events")
	if sandboxID == "" || endpoint == "" {
		fmt.Fprintln(os.Stderr, "SBX_SANDBOX_ID and SBX_STREAM_ENDPOINT are required")
		os.Exit(2)
	}
	wsURL, err := streamURL(endpoint, sandboxID)
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid SBX_STREAM_ENDPOINT:", err)
		os.Exit(2)
	}

	_ = os.MkdirAll(eventsDir, 0o755)
	state := map[string]*execState{}
	for {
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			time.Sleep(2 * time.Second)
			continue
		}
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		_ = conn.WriteMessage(websocket.PingMessage, []byte("ping"))
		_ = conn.SetWriteDeadline(time.Time{})

		for {
			if err := pump(eventsDir, sandboxID, state, conn); err != nil {
				_ = conn.Close()
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
}

func pump(dir, sandboxID string, state map[string]*execState, conn *websocket.Conn) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		execID, kind := parseEventFile(name)
		if execID == "" {
			continue
		}
		st := state[execID]
		if st == nil {
			st = &execState{}
			state[execID] = st
		}
		if !st.startSent {
			if err := sendEvent(conn, execEvent{
				SandboxID: sandboxID,
				ExecID:    execID,
				Type:      "start",
				Time:      time.Now().UTC().Format(time.RFC3339Nano),
			}); err != nil {
				return err
			}
			st.startSent = true
			st.lastActivity = time.Now()
		}

		path := filepath.Join(dir, name)
		switch kind {
		case "stdout":
			data, off, err := readNew(path, st.stdoutOff)
			if err != nil {
				return err
			}
			if len(data) > 0 {
				if err := sendEvent(conn, execEvent{
					SandboxID: sandboxID,
					ExecID:    execID,
					Type:      "output",
					Stream:    "stdout",
					Data:      data,
					Time:      time.Now().UTC().Format(time.RFC3339Nano),
				}); err != nil {
					return err
				}
				st.stdoutOff = off
				st.lastActivity = time.Now()
			}
		case "stderr":
			data, off, err := readNew(path, st.stderrOff)
			if err != nil {
				return err
			}
			if len(data) > 0 {
				if err := sendEvent(conn, execEvent{
					SandboxID: sandboxID,
					ExecID:    execID,
					Type:      "output",
					Stream:    "stderr",
					Data:      data,
					Time:      time.Now().UTC().Format(time.RFC3339Nano),
				}); err != nil {
					return err
				}
				st.stderrOff = off
				st.lastActivity = time.Now()
			}
		case "exit":
			if st.exitSent {
				continue
			}
			data, _, err := readNew(path, 0)
			if err != nil {
				return err
			}
			exitCode := 0
			if v := strings.TrimSpace(data); v != "" {
				if n, err := strconv.Atoi(v); err == nil {
					exitCode = n
				}
			}
			st.exitSeen = true
			st.exitCode = exitCode
		}
	}
	now := time.Now()
	for execID, st := range state {
		if st.exitSent || !st.exitSeen {
			continue
		}
		pending, err := hasPendingOutput(dir, execID, st)
		if err != nil {
			return err
		}
		if pending {
			st.exitReadyAt = time.Time{}
			continue
		}
		if st.exitReadyAt.IsZero() {
			st.exitReadyAt = now
			continue
		}
		if now.Sub(st.exitReadyAt) < 300*time.Millisecond {
			continue
		}
		if err := sendEvent(conn, execEvent{
			SandboxID: sandboxID,
			ExecID:    execID,
			Type:      "exit",
			ExitCode:  st.exitCode,
			Time:      time.Now().UTC().Format(time.RFC3339Nano),
		}); err != nil {
			return err
		}
		st.exitSent = true
	}
	return nil
}

func hasPendingOutput(dir, execID string, st *execState) (bool, error) {
	stdoutPath := filepath.Join(dir, execID+".stdout")
	stderrPath := filepath.Join(dir, execID+".stderr")

	stdoutSize, err := fileSize(stdoutPath)
	if err != nil {
		return false, err
	}
	stderrSize, err := fileSize(stderrPath)
	if err != nil {
		return false, err
	}
	return stdoutSize > st.stdoutOff || stderrSize > st.stderrOff, nil
}

func fileSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	return info.Size(), nil
}

func sendEvent(conn *websocket.Conn, evt execEvent) error {
	payload, err := json.Marshal(evt)
	if err != nil {
		return err
	}
	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	err = conn.WriteMessage(websocket.TextMessage, payload)
	conn.SetWriteDeadline(time.Time{})
	return err
}

func readNew(path string, offset int64) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", offset, err
	}
	defer f.Close()
	_, err = f.Seek(offset, io.SeekStart)
	if err != nil {
		return "", offset, err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return "", offset, err
	}
	return string(data), offset + int64(len(data)), nil
}

func parseEventFile(name string) (execID, kind string) {
	switch {
	case strings.HasSuffix(name, ".stdout"):
		return strings.TrimSuffix(name, ".stdout"), "stdout"
	case strings.HasSuffix(name, ".stderr"):
		return strings.TrimSuffix(name, ".stderr"), "stderr"
	case strings.HasSuffix(name, ".exit"):
		return strings.TrimSuffix(name, ".exit"), "exit"
	default:
		return "", ""
	}
}

func streamURL(endpoint, sandboxID string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + "/sandboxes/" + sandboxID + "/ingest"
	if u.Scheme == "http" {
		u.Scheme = "ws"
	} else if u.Scheme == "https" {
		u.Scheme = "wss"
	}
	return u.String(), nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
