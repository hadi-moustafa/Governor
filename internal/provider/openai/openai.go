// Package openai implements provider.Provider against OpenAI's real
// chat completions streaming API. It's the first adapter this project has
// built against real auth and a real wire format, rather than a fake
// server — the shape of its quirks (usage reported only in a final chunk,
// distinct choices/usage split) was deliberately anticipated by
// internal/provider/mockfinal before this existed, so building it is also
// the first real test of that prediction.
package openai

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

// DefaultBaseURL is OpenAI's chat completions endpoint.
const DefaultBaseURL = "https://api.openai.com/v1/chat/completions"

// APIError is returned when OpenAI rejects a request outright (bad auth,
// rate limit, invalid model, ...) via an HTTP error status and a JSON
// error body, rather than by streaming anything.
type APIError struct {
	StatusCode int
	Type       string
	Message    string
	Code       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("openai: %d %s: %s", e.StatusCode, e.Type, e.Message)
}

// Client is a provider.Provider that talks to OpenAI's real API.
type Client struct {
	// APIKey authenticates every request via the Authorization header.
	APIKey string
	// BaseURL defaults to DefaultBaseURL; overridable for testing against
	// a local httptest server instead of the real API.
	BaseURL string
	HTTP    *http.Client
}

// New returns a Client authenticated with apiKey.
func New(apiKey string) *Client {
	return &Client{APIKey: apiKey}
}

var _ provider.Provider = (*Client)(nil)

func (c *Client) baseURL() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return DefaultBaseURL
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

// wireRequest is OpenAI's chat completions request body. provider.Message
// already matches OpenAI's {role, content} shape field-for-field, so
// req.Messages is reused directly with no per-message translation.
type wireRequest struct {
	Model         string             `json:"model"`
	Messages      []provider.Message `json:"messages"`
	Stream        bool               `json:"stream"`
	StreamOptions streamOptions      `json:"stream_options"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// Send opens a streaming chat completion request. ctx governs the whole
// call — canceling it tears down the underlying connection, the only
// cancellation path provider.Stream.Next relies on.
func (c *Client) Send(ctx context.Context, req provider.Request) (provider.Stream, error) {
	body, err := json.Marshal(wireRequest{
		Model:         req.Model,
		Messages:      req.Messages,
		Stream:        true,
		StreamOptions: streamOptions{IncludeUsage: true},
	})
	if err != nil {
		return nil, fmt.Errorf("openai: encode request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("openai: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)

	resp, err := c.httpClient().Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai: send request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, decodeAPIError(resp)
	}

	return &stream{resp: resp, r: bufio.NewReader(resp.Body)}, nil
}

func decodeAPIError(resp *http.Response) error {
	var body struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return fmt.Errorf("openai: unexpected status %d (unparseable error body: %w)", resp.StatusCode, err)
	}
	return &APIError{
		StatusCode: resp.StatusCode,
		Type:       body.Error.Type,
		Message:    body.Error.Message,
		Code:       body.Error.Code,
	}
}

// wireChunk is one SSE "data:" event from a chat completions stream.
// Content chunks carry Choices and a nil Usage; when
// stream_options.include_usage is set, one final chunk with an empty
// Choices array and a populated Usage arrives just before [DONE] — the
// only point at which real token counts are known. This is exactly the
// SummaryOnly shape internal/provider/mockfinal was built to anticipate.
type wireChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		TotalTokens int `json:"total_tokens"`
	} `json:"usage"`
}

type stream struct {
	resp   *http.Response
	r      *bufio.Reader
	closed bool
}

var _ provider.Stream = (*stream)(nil)

// Next reads the next SSE "data:" line and decodes it. It returns io.EOF
// on the terminal [DONE] event, an *APIError is never returned here (only
// from Send, since OpenAI reports request-level failures via HTTP status,
// not a wire-level error event mid-stream) and whatever error the
// underlying read produced otherwise (including a context-canceled error)
// if the connection is torn down mid-stream.
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
			return provider.Chunk{}, fmt.Errorf("openai: decode chunk: %w", err)
		}

		var chunk provider.Chunk
		if len(wc.Choices) > 0 {
			chunk.Delta = wc.Choices[0].Delta.Content
			if wc.Choices[0].FinishReason != nil {
				chunk.FinishReason = *wc.Choices[0].FinishReason
			}
		}
		if wc.Usage != nil {
			chunk.Tokens = wc.Usage.TotalTokens
		}
		return chunk, nil
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
