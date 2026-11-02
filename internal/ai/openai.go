// Package ai is a small OpenAI client: enough to ask a model for one JSON
// answer, which is all the recommender needs.
//
// There is no SDK here on purpose. One request and one response is not worth a
// dependency, and the shape of the answer is pinned by a JSON schema, so the
// decoding is the same whether the model is clever or careless.
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client talks to the OpenAI API.
type Client struct {
	HTTP    *http.Client
	BaseURL string
	Key     string
	Model   string
}

// NewClient builds a client for the given key and model.
func NewClient(key, model string) *Client {
	return &Client{
		HTTP:    &http.Client{Timeout: 90 * time.Second},
		BaseURL: "https://api.openai.com/v1",
		Key:     key,
		Model:   model,
	}
}

// Error is a refusal from the API. The status is kept because the useful thing
// to say differs by it: a bad key, a model the account cannot use, and an
// exhausted quota all need different advice.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string {
	detail := strings.TrimSpace(e.Message)
	if detail == "" {
		detail = http.StatusText(e.Status)
	}

	switch {
	case e.Status == http.StatusUnauthorized:
		return fmt.Sprintf("OpenAI rejected the key: %s", detail)
	case e.Status == http.StatusForbidden:
		return fmt.Sprintf("OpenAI refused the request for this key: %s", detail)
	case e.Status == http.StatusTooManyRequests:
		return fmt.Sprintf("OpenAI rate limited the request or the account is out of quota: %s", detail)
	case e.Status == http.StatusNotFound && strings.Contains(strings.ToLower(e.Code+detail), "model"):
		return fmt.Sprintf("OpenAI does not offer that model (%s): %s", e.Code, detail)
	default:
		return fmt.Sprintf("OpenAI returned %d: %s", e.Status, detail)
	}
}

// JSON asks the model for a single JSON object matching schema, and returns
// the object as it arrived.
//
// The schema is enforced by the API rather than described in prose, so the
// answer can be decoded once and does not need repair. strict requires every
// property to be listed in required and additionalProperties to be false; the
// schemas the recommender passes do both.
func (c *Client) JSON(ctx context.Context, name string, schema map[string]any, system, user string) ([]byte, error) {
	if strings.TrimSpace(c.Key) == "" {
		return nil, errors.New("no OpenAI key: set OPENAI_API_KEY, or run `am config set openaiKey`")
	}

	request := map[string]any{
		"model": c.Model,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
		"response_format": map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name":   name,
				"strict": true,
				"schema": schema,
			},
		},
		// A reasoning model spends part of this on thinking before it writes
		// anything, which is why the cap is well above the size of the answer.
		"max_completion_tokens": 4000,
	}

	body, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}

	status, payload, err := c.post(ctx, body)
	if err != nil {
		return nil, err
	}

	// A model that cannot honour a JSON schema still has to answer in JSON,
	// so the request is retried once with the schema dropped and the shape
	// asked for in words instead. A rejected request is not billed, so the
	// retry costs nothing but a round trip.
	if status == http.StatusBadRequest && schemaRejected(payload) {
		delete(request, "response_format")
		request["messages"] = append(request["messages"].([]map[string]string), map[string]string{
			"role":    "system",
			"content": "Reply with a single JSON object and nothing else.",
		})
		body, err = json.Marshal(request)
		if err != nil {
			return nil, err
		}
		status, payload, err = c.post(ctx, body)
		if err != nil {
			return nil, err
		}
	}

	if status != http.StatusOK {
		return nil, apiError(status, payload)
	}

	var reply struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
				Refusal string `json:"refusal"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(payload, &reply); err != nil {
		return nil, fmt.Errorf("decoding OpenAI's reply: %w", err)
	}
	if len(reply.Choices) == 0 {
		return nil, errors.New("OpenAI returned no choices")
	}
	if refusal := strings.TrimSpace(reply.Choices[0].Message.Refusal); refusal != "" {
		return nil, fmt.Errorf("the model refused: %s", refusal)
	}

	content := strings.TrimSpace(reply.Choices[0].Message.Content)
	if content == "" {
		return nil, errors.New("OpenAI returned an empty answer")
	}
	return []byte(content), nil
}

// post sends one request and returns the status and body, whatever the status
// was: the caller decides whether a non-200 is an error worth explaining or a
// request worth making differently.
func (c *Client) post(ctx context.Context, body []byte) (int, []byte, error) {
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Authorization", "Bearer "+c.Key)

	response, err := c.HTTP.Do(httpRequest)
	if err != nil {
		return 0, nil, fmt.Errorf("asking OpenAI: %w", err)
	}
	defer response.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return 0, nil, fmt.Errorf("reading OpenAI's answer: %w", err)
	}
	return response.StatusCode, payload, nil
}

// schemaRejected reports whether a 400 was the schema's fault rather than the
// request's: that is the case worth retrying without it.
func schemaRejected(payload []byte) bool {
	text := strings.ToLower(string(payload))
	return strings.Contains(text, "response_format") ||
		strings.Contains(text, "json_schema") ||
		strings.Contains(text, "structured output")
}

// apiError turns an error body into an Error, keeping the code when the API
// supplies one.
func apiError(status int, payload []byte) error {
	var body struct {
		Error struct {
			Message string          `json:"message"`
			Code    json.RawMessage `json:"code"`
			Type    string          `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(payload, &body); err != nil || body.Error.Message == "" {
		return &Error{Status: status, Message: strings.TrimSpace(string(payload))}
	}

	code := strings.Trim(string(body.Error.Code), `"`)
	if code == "" || code == "null" {
		code = body.Error.Type
	}
	return &Error{Status: status, Code: code, Message: body.Error.Message}
}
