// Package anthropic implements provider.Provider against Anthropic's real
// Messages API streaming format. Its usage-reporting shape is the "easy"
// case CLAUDE.md's plan anticipated: output token counts arrive
// incrementally via message_delta events during the stream, not only in
// one final chunk the way OpenAI's stream_options.include_usage does (see
// internal/provider/openai) — so live per-chunk budget metering should
// work close to as designed here, unlike the SummaryOnly gap
// internal/provider/mockfinal documents.
//
// Two real quirks distinct from OpenAI, deliberately preserved rather than
// smoothed over:
//   - Auth is an "x-api-key" header plus a required "anthropic-version"
//     header, not "Authorization: Bearer ...".
//   - The Messages API rejects a "system" role inside the messages array;
//     a system prompt is a separate top-level request field. Send splits
//     it out automatically.
//   - The stream is a sequence of named SSE events (message_start,
//     content_block_delta, message_delta, message_stop, periodic pings),
//     not a flat one-chunk-per-event stream terminated by a literal
//     "[DONE]" sentinel — most events carry nothing provider.Chunk cares
//     about and are skipped.
package anthropic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/hadi-moustafa/governor/internal/provider"
)

// DefaultBaseURL is Anthropic's Messages API endpoint.
const DefaultBaseURL = "https://api.anthropic.com/v1/messages"

// apiVersion is the required anthropic-version header value.
const apiVersion = "2023-06-01"

// defaultMaxTokens stands in for a real per-request max_tokens value.
// Anthropic's Messages API requires max_tokens on every request, but
// provider.Request has no field for it yet — this is a known gap to close
// if/when Request grows a MaxTokens field; a fixed default is enough to
// exercise and test this adapter today.
const defaultMaxTokens = 1024

// APIError is returned when Anthropic reports a request-level failure —
// either an HTTP error status before streaming starts, or a mid-stream
// "error" SSE event.
type APIError struct {
	Type    string
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("anthropic: %s: %s", e.Type, e.Message)
}

// Client is a provider.Provider that talks to Anthropic's real API.
type Client struct {
	// APIKey authenticates every request via the x-api-key header.
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

type wireRequest struct {
	Model     string             `json:"model"`
	System    string             `json:"system,omitempty"`
	Messages  []provider.Message `json:"messages"`
	MaxTokens int                `json:"max_tokens"`
	Stream    bool               `json:"stream"`
}

// splitSystem pulls any "system"-role messages out of msgs (concatenated,
// in order) since the Messages API doesn't accept them inline — it wants
// them in a separate top-level field.
func splitSystem(msgs []provider.Message) (system string, rest []provider.Message) {
	var sb strings.Builder
	for _, m := range msgs {
		if m.Role != "system" {
			rest = append(rest, m)
			continue
		}
		if sb.Len() > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(m.Content)
	}
	return sb.String(), rest
}

// Send opens a streaming Messages API request. ctx governs the whole call
// — canceling it tears down the underlying connection, the only
// cancellation path provider.Stream.Next relies on.
func (c *Client) Send(ctx context.Context, req provider.Request) (provider.Stream, error) {
	system, messages := splitSystem(req.Messages)

	body, err := json.Marshal(wireRequest{
		Model:     req.Model,
		System:    system,
		Messages:  messages,
		MaxTokens: defaultMaxTokens,
		Stream:    true,
	})
	if err != nil {
		return nil, fmt.Errorf("anthropic: encode request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("anthropic: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", c.APIKey)
	httpReq.Header.Set("anthropic-version", apiVersion)

	resp, err := c.httpClient().Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic: send request: %w", err)
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
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return fmt.Errorf("anthropic: unexpected status %d (unparseable error body: %w)", resp.StatusCode, err)
	}
	return &APIError{Type: body.Error.Type, Message: body.Error.Message}
}

// wireEvent covers the fields this adapter cares about across every
// Messages API SSE event type. Each payload is self-describing via Type,
// so Next switches on it directly rather than also tracking the separate
// "event:" line SSE sends alongside "data:".
type wireEvent struct {
	Type string `json:"type"`

	// Present on content_block_delta (Text, when Delta.Type=="text_delta")
	// and message_delta (StopReason).
	Delta *struct {
		Type       string `json:"type"`
		Text       string `json:"text"`
		StopReason string `json:"stop_reason"`
	} `json:"delta"`

	// Present on message_delta: cumulative output tokens for the message
	// so far. Note this is output tokens only — unlike OpenAI's
	// usage.total_tokens, input tokens arrive separately and earlier, in
	// message_start's nested message.usage, which this adapter doesn't
	// currently surface (see defaultMaxTokens's doc comment: Governor's
	// flat pricing model doesn't yet split input/output cost).
	Usage *struct {
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`

	// Present on the "error" event type.
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

type stream struct {
	resp   *http.Response
	r      *bufio.Reader
	closed bool
}

var _ provider.Stream = (*stream)(nil)

// Next reads SSE events until one carries something provider.Chunk cares
// about, decoding each "data:" line's JSON and switching on its own "type"
// field. Unlike internal/provider/mock and openai, Anthropic doesn't send
// a literal terminal sentinel: the stream just ends after message_stop,
// so Next's own read returns the real io.EOF once the connection closes.
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

		var we wireEvent
		if err := json.Unmarshal([]byte(payload), &we); err != nil {
			return provider.Chunk{}, fmt.Errorf("anthropic: decode event: %w", err)
		}

		switch we.Type {
		case "content_block_delta":
			if we.Delta != nil && we.Delta.Type == "text_delta" {
				return provider.Chunk{Delta: we.Delta.Text}, nil
			}
			// A non-text delta (e.g. tool-use input) — nothing to
			// forward yet.
			continue
		case "message_delta":
			var chunk provider.Chunk
			if we.Delta != nil {
				chunk.FinishReason = we.Delta.StopReason
			}
			if we.Usage != nil {
				chunk.Tokens = we.Usage.OutputTokens
			}
			return chunk, nil
		case "error":
			if we.Error == nil {
				return provider.Chunk{}, fmt.Errorf("anthropic: error event with no error body")
			}
			return provider.Chunk{}, &APIError{Type: we.Error.Type, Message: we.Error.Message}
		default:
			// message_start, content_block_start, content_block_stop,
			// message_stop, ping: nothing for provider.Chunk to carry.
			continue
		}
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
