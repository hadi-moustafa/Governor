// Package provider defines the backend-agnostic interface that mock and real
// LLM providers (OpenAI, Anthropic, ...) implement.
package provider

import "context"

// Message is one turn in a chat request.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Request is a backend-agnostic chat completion request.
type Request struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
}

// Chunk is one incremental piece of a streaming response. It's wire-format
// agnostic on purpose — each adapter owns its own wire struct and translates
// into this shape, since real providers' streaming formats differ a lot
// (OpenAI reports cumulative usage only in a final stream_options chunk;
// Anthropic reports it incrementally via message_delta events) and this
// interface shouldn't need reshaping every time a new provider's quirks
// show up.
type Chunk struct {
	Delta        string
	Tokens       int
	FinishReason string
}

// Stream is an open, in-progress streaming response.
type Stream interface {
	// Next blocks until the next chunk is available, the stream ends
	// (io.EOF), or the ctx passed to Send is canceled.
	Next() (Chunk, error)
	// Close releases any resources still held. Idempotent.
	Close() error
}

// Provider sends a request to an LLM backend and returns a live stream.
// Implementations must derive their upstream call from ctx (e.g. via
// http.NewRequestWithContext) — canceling ctx is the only cancellation
// mechanism, and it must reach the actual upstream connection, not just
// stop the caller's own read loop.
type Provider interface {
	Send(ctx context.Context, req Request) (Stream, error)
}
