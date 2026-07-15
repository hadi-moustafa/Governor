// Package mockprovider implements a fake streaming LLM backend: an SSE
// server that emits chunks with fake token counts at a configurable pace,
// zero API cost. It exists so proxy cutoff behavior can be proven against a
// real upstream HTTP connection instead of a mock in the loose sense.
//
// It also records, per request, whether it observed the client tearing
// down the connection mid-stream. That's the mechanism a test uses to prove
// an upstream request was actually canceled, not just abandoned
// client-side: net/http only cancels a server request's context when the
// underlying TCP connection is actually closed, so ServeHTTP seeing
// r.Context().Done() fire is a direct consequence of the socket dying, not
// of the caller merely stopping reading.
package mockprovider

import (
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Config controls how a Server streams a response.
type Config struct {
	Chunks         int
	TokensPerChunk int
	ChunkDelay     time.Duration
}

// Stats is what a Server observed while handling its request.
type Stats struct {
	ChunksWritten    int
	Completed        bool // ran to completion, wrote all Config.Chunks
	CanceledByClient bool // observed r.Context().Done() before finishing
	HandlerDuration  time.Duration
}

// Server is a fake streaming LLM backend. One Server meaningfully tracks
// exactly one request — construct a fresh Server per test/use. This is
// deliberate for now: the crux this package exists to prove only needs
// single-request tracking, and Await panics on a second completed request
// rather than silently reporting stale data.
type Server struct {
	cfg Config

	mu    sync.Mutex
	stats Stats

	done   chan struct{}
	closed bool
}

// NewServer returns a ready-to-use Server for cfg.
func NewServer(cfg Config) *Server {
	return &Server{cfg: cfg, done: make(chan struct{})}
}

var _ http.Handler = (*Server)(nil)

// ServeHTTP streams cfg.Chunks fake SSE chunks, cfg.ChunkDelay apart, then a
// terminal [DONE]. The wait before each chunk is preemptible on the
// request's context so cancellation is caught promptly instead of only
// once per full chunk cycle.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)

	ctx := r.Context()
	for i := 0; i < s.cfg.Chunks; i++ {
		select {
		case <-ctx.Done():
			s.finish(start, false)
			return
		case <-time.After(s.cfg.ChunkDelay):
		}

		fmt.Fprintf(w, "data: {\"delta\":\"chunk-%d\",\"tokens\":%d,\"finish_reason\":\"\"}\n\n", i, s.cfg.TokensPerChunk)
		if flusher != nil {
			flusher.Flush()
		}
		s.mu.Lock()
		s.stats.ChunksWritten++
		s.mu.Unlock()
	}

	fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
	s.finish(start, true)
}

func (s *Server) finish(start time.Time, completed bool) {
	s.mu.Lock()
	s.stats.Completed = completed
	s.stats.CanceledByClient = !completed
	s.stats.HandlerDuration = time.Since(start)
	s.mu.Unlock()
	close(s.done)
}

// Await blocks until the one request this Server has handled completes, or
// timeout elapses.
func (s *Server) Await(timeout time.Duration) (Stats, error) {
	select {
	case <-s.done:
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.stats, nil
	case <-time.After(timeout):
		return Stats{}, errors.New("mockprovider: timed out waiting for the handler to finish")
	}
}
