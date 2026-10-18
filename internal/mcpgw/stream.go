package mcpgw

import (
	"bytes"
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ncecere/open-mcp-portal/internal/upstream"
)

// Streaming a tools/call (docs/mcp-protocol.md "The portal as a client",
// Streamable HTTP "Receiving Messages"). When a tools/call carries
// _meta.progressToken (either era) and the client accepts text/event-stream,
// the answer is an SSE stream scoped to the request: the server's
// notifications/progress for that token as they arrive, then the one JSON-RPC
// response, then the stream closes. Anything else stays one buffered JSON
// object. notifications/message (logging, deprecated) is not relayed.
//
// The stream starts when the call is about to reach the server, so every
// refusal before that (unknown tool, denied, consent needed, rate limited)
// is the same JSON answer as ever. After it starts, errors too are the
// response event. A comment line every streamKeepAlive keeps proxies from
// timing out a quiet call. Closing the connection cancels the request's
// context and so the call to the server.
//
// Bounds: at most maxProgressEvents progress events and maxStreamBytes bytes
// in all (further progress is dropped, the call goes on); the response is at
// most MaxResponseBytes as before.

const (
	maxProgressEvents = 64
	maxStreamBytes    = 4 << 20
	// defaultStreamKeepAlive is the interval of SSE comment lines.
	defaultStreamKeepAlive = 15 * time.Second
	maxProgressMessage     = 1000
	maxProgressToken       = 256
)

// SetStreamKeepAlive sets the interval of keep-alive comments on streamed
// calls (tests). Call it before the handler serves its first request.
func (h *Handler) SetStreamKeepAlive(d time.Duration) { h.keepAlive = d }

func (h *Handler) streamKeepAlive() time.Duration {
	if h.keepAlive > 0 {
		return h.keepAlive
	}
	return defaultStreamKeepAlive
}

// progressToken is the request's _meta.progressToken (a JSON string or
// integer) when it is one.
func (rc *request) progressToken() (json.RawMessage, bool) {
	raw, ok := rc.paramsMeta()["progressToken"]
	if !ok {
		return nil, false
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, false
	}
	switch {
	case raw[0] == '"':
		var s string
		if json.Unmarshal(raw, &s) != nil || s == "" || len(s) > maxProgressToken {
			return nil, false
		}
	default:
		var n json.Number
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if dec.Decode(&n) != nil {
			return nil, false
		}
		if _, err := n.Int64(); err != nil {
			return nil, false
		}
	}
	return raw, true
}

// acceptsStream reports whether the client's Accept lists text/event-stream.
func acceptsStream(values []string) bool {
	for _, v := range values {
		for _, part := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(strings.Split(part, ";")[0]), "text/event-stream") {
				return true
			}
		}
	}
	return false
}

// wantsStream: a tools/call with a progress token from a client that accepts
// an event stream.
func (rc *request) wantsStream() (json.RawMessage, bool) {
	if rc.msg.Method != methodToolsCall || !acceptsStream(rc.r.Header.Values("Accept")) {
		return nil, false
	}
	return rc.progressToken()
}

// stream is the SSE response of one call. It stands in for the request's
// ResponseWriter once started: whatever the handler writes as a JSON-RPC
// response becomes the final event.
type stream struct {
	w     http.ResponseWriter
	flush func()
	token json.RawMessage

	mu     sync.Mutex
	events int
	total  int64
	last   float64
	done   bool
	stop   chan struct{}
	wg     sync.WaitGroup
}

// startStream commits the response to SSE and starts the keep-alive; the
// request's writer is replaced by the stream.
func (rc *request) startStream(token json.RawMessage) *stream {
	rw := http.NewResponseController(rc.w)
	s := &stream{w: rc.w, token: token, stop: make(chan struct{}), last: math.Inf(-1),
		flush: func() { _ = rw.Flush() }}
	h := rc.w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("X-Accel-Buffering", "no")
	rc.w.WriteHeader(http.StatusOK)
	s.flush()
	rc.w, rc.stream = s, s
	every := rc.h.streamKeepAlive()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-s.stop:
				return
			case <-t.C:
				s.comment()
			}
		}
	}()
	return s
}

func (s *stream) comment() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return
	}
	_, _ = s.w.Write([]byte(": keep-alive\n\n"))
	s.flush()
}

// close ends the stream: no event follows it.
func (s *stream) close() {
	s.mu.Lock()
	s.done = true
	s.mu.Unlock()
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
	s.wg.Wait()
}

// bytes is what was sent, for the call record.
func (s *stream) bytes() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.total
}

// Header, WriteHeader and Write make the stream the request's
// http.ResponseWriter: the status is already sent, and a body is the
// response event.
func (s *stream) Header() http.Header { return s.w.Header() }
func (s *stream) WriteHeader(int)     {}

func (s *stream) Write(b []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return len(b), nil
	}
	s.event(b)
	s.done = true // the response is the last event
	return len(b), nil
}

// event writes one message event (the caller holds mu).
func (s *stream) event(data []byte) {
	frame := make([]byte, 0, len(data)+24)
	frame = append(frame, "event: message\ndata: "...)
	frame = append(frame, data...)
	frame = append(frame, '\n', '\n')
	_, _ = s.w.Write(frame)
	s.total += int64(len(frame))
	s.flush()
}

// progress relays one of the server's progress notifications under the
// request's own token, within the bounds. Progress that doesn't increase, or
// can't be encoded, is dropped.
func (s *stream) progress(p upstream.Progress) {
	if math.IsNaN(p.Progress) || math.IsInf(p.Progress, 0) || (p.Total != nil && (math.IsNaN(*p.Total) || math.IsInf(*p.Total, 0))) {
		return
	}
	params := map[string]any{"progressToken": s.token, "progress": p.Progress}
	if p.Total != nil {
		params["total"] = *p.Total
	}
	if p.Message != "" {
		params["message"] = clipBytes(p.Message, maxProgressMessage)
	}
	b, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "notifications/progress", "params": params})
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done || s.events >= maxProgressEvents || s.total+int64(len(b))+32 > maxStreamBytes || p.Progress <= s.last {
		return
	}
	s.last = p.Progress
	s.events++
	s.event(b)
}

// clipBytes cuts s to at most n bytes on a rune boundary.
func clipBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
