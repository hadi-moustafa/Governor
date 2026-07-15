// Package mock implements provider.Provider against internal/mockprovider's
// SSE wire format, so the proxy can be exercised end-to-end without any
// real API cost.
package mock

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

// Client is a provider.Provider that talks to a mockprovider.Server over
// HTTP.
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
// ctx closes the underlying connection, which is the only cancellation
// path Stream.Next relies on.
func (c *Client) Send(ctx context.Context, req provider.Request) (provider.Stream, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("mock: encode request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("mock: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient().Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("mock: send request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("mock: unexpected status %d", resp.StatusCode)
	}

	return &stream{resp: resp, r: bufio.NewReader(resp.Body)}, nil
}

type wireChunk struct {
	Delta        string `json:"delta"`
	Tokens       int    `json:"tokens"`
	FinishReason string `json:"finish_reason"`
}

type stream struct {
	resp   *http.Response
	r      *bufio.Reader
	closed bool
}

var _ provider.Stream = (*stream)(nil)

// Next reads the next SSE "data:" line and decodes it. It returns io.EOF on
// the terminal [DONE] event, and returns whatever error the underlying
// read produced (including a context-canceled error) if the connection is
// torn down mid-stream.
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
			return provider.Chunk{}, fmt.Errorf("mock: decode chunk: %w", err)
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
