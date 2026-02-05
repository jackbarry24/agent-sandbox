package main

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type execEvent struct {
	SandboxID string `json:"sandbox_id"`
	ExecID    string `json:"exec_id"`
	Seq       int64  `json:"seq"`
	Type      string `json:"type"`
	Stream    string `json:"stream,omitempty"`
	Data      string `json:"data,omitempty"`
	ExitCode  int    `json:"exit_code,omitempty"`
	Time      string `json:"time"`
}

type streamHub struct {
	mu      sync.Mutex
	buffers map[string]*streamBuffer
	limit   int
	seq     int64
}

type streamBuffer struct {
	mu     sync.Mutex
	events []execEvent
	subs   map[chan execEvent]struct{}
	limit  int
}

func newStreamHub(limit int) *streamHub {
	if limit <= 0 {
		limit = 200
	}
	return &streamHub{
		buffers: map[string]*streamBuffer{},
		limit:   limit,
	}
}

func (h *streamHub) nextSeq() int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seq++
	return h.seq
}

func (h *streamHub) bufferFor(sandboxID string) *streamBuffer {
	h.mu.Lock()
	defer h.mu.Unlock()
	buf, ok := h.buffers[sandboxID]
	if !ok {
		buf = &streamBuffer{
			events: make([]execEvent, 0, h.limit),
			subs:   map[chan execEvent]struct{}{},
			limit:  h.limit,
		}
		h.buffers[sandboxID] = buf
	}
	return buf
}

func (h *streamHub) publish(evt execEvent) {
	buf := h.bufferFor(evt.SandboxID)
	buf.mu.Lock()
	if len(buf.events) >= buf.limit {
		copy(buf.events, buf.events[1:])
		buf.events[len(buf.events)-1] = evt
	} else {
		buf.events = append(buf.events, evt)
	}
	for ch := range buf.subs {
		select {
		case ch <- evt:
		default:
		}
	}
	buf.mu.Unlock()
}

func (h *streamHub) subscribe(sandboxID string) (chan execEvent, []execEvent) {
	buf := h.bufferFor(sandboxID)
	ch := make(chan execEvent, 128)
	buf.mu.Lock()
	buf.subs[ch] = struct{}{}
	snapshot := make([]execEvent, len(buf.events))
	copy(snapshot, buf.events)
	buf.mu.Unlock()
	return ch, snapshot
}

func (h *streamHub) unsubscribe(sandboxID string, ch chan execEvent) {
	buf := h.bufferFor(sandboxID)
	buf.mu.Lock()
	delete(buf.subs, ch)
	close(ch)
	buf.mu.Unlock()
}

var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

func writeEventJSON(conn *websocket.Conn, evt execEvent) error {
	payload, err := json.Marshal(evt)
	if err != nil {
		return err
	}
	return conn.WriteMessage(websocket.TextMessage, payload)
}

func nowTS() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}
