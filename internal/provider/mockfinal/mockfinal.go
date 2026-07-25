// Package mockfinal implements provider.Provider against a second fake
// wire format (served by internal/mockprovider's SummaryOnly/FailAfterChunk
// modes), deliberately different from internal/provider/mock, to stress-test
// the Provider interface before real providers (OpenAI/Anthropic) show up
// and force a reshape.
//
// Two quirks are exercised here:
//
//   - Usage is reported only on the final chunk (tokens:0 on every chunk
//     before it), mirroring OpenAI's stream_options.include_usage behavior.
//     internal/proxy/stream.go reserves budget per chunk as it arrives, so a
//     provider shaped like this makes live mid-stream cutoff structurally
//     impossible: by the time real cost is known, the stream is already
//     over. That's a real finding for Phase 2, not something this package
//     tries to paper over.
//   - Errors arrive as a distinct wire payload (a JSON "error" object)
//     rather than a torn-down connection, surfaced through Next as a typed
//     ProviderError rather than an opaque transport error.
package mockfinal

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/hadi-moustafa/governor/internal/provider"
)

// ProviderError is returned by Stream.Next when the upstream reports an
// error via its own wire shape instead of tearing down the connection.
type ProviderError struct {
	Type    string
	Message string
}

func (e *ProviderError) Error() string {
	return fmt.Sprintf("mockfinal: upstream error (%s): %s", e.Type, e.Message)
}

// Client is a provider.Provider that talks to a mockprovider.Server running
// in SummaryOnly and/or FailAfterChunk mode.
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

// New returns a Client pointed at baseURL (a running mockprovider.Server).
func New(baseURL string) *Client {
	return &Client{BaseURL: baseURL}
}

var _ provider.Provider = (*Client)(nil)

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

// Send opens a streaming request to the mock provider. The upstream HTTP
// request is built with http.NewRequestWithContext(ctx, ...) — canceling
// ctx closes the underlying connection, which is the only cancellation path
// Stream.Next relies on.
func (c *Client) Send(ctx context.Context, req provider.Request) (provider.Stream, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("mockfinal: encode request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("mockfinal: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient().Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("mockfinal: send request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("mockfinal: unexpected status %d", resp.StatusCode)
	}

	return &stream{resp: resp, r: bufio.NewReader(resp.Body)}, nil
}

type wireChunk struct {
	Delta        string     `json:"delta"`
	Tokens       int        `json:"tokens"`
	FinishReason string     `json:"finish_reason"`
	Error        *wireError `json:"error"`
}

type wireError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type stream struct {
	resp   *http.Response
	r      *bufio.Reader
	closed bool
}

var _ provider.Stream = (*stream)(nil)

// Next reads the next SSE "data:" line and decodes it. It returns io.EOF on
// the terminal [DONE] event, a *ProviderError if the payload is a wire-level
// error event, or whatever error the underlying read produced (including a
// context-canceled error) if the connection is torn down mid-stream.
func (s *stream) Next() (provider.Chunk, error) {
	for {
		line, err := s.r.ReadString('\n')
		if err != nil {
			return provider.Chunk{}, err
		}
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			return provider.Chunk{}, io.EOF
		}

		var wc wireChunk
		if err := json.Unmarshal([]byte(payload), &wc); err != nil {
			return provider.Chunk{}, fmt.Errorf("mockfinal: decode chunk: %w", err)
		}
		if wc.Error != nil {
			return provider.Chunk{}, &ProviderError{Type: wc.Error.Type, Message: wc.Error.Message}
		}
		return provider.Chunk{Delta: wc.Delta, Tokens: wc.Tokens, FinishReason: wc.FinishReason}, nil
	}
}

// Close releases the underlying HTTP response body. Idempotent.
func (s *stream) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	return s.resp.Body.Close()
}
