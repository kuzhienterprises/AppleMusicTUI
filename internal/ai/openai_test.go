package ai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func schema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"properties":           map[string]any{"ok": map[string]any{"type": "boolean"}},
		"required":             []string{"ok"},
		"additionalProperties": false,
	}
}

func TestJSONSendsASchemaAndReturnsTheAnswer(t *testing.T) {
	var request map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Errorf("authorization = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decoding the request: %v", err)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"{\"ok\":true}"}}]}`))
	}))
	defer server.Close()

	client := NewClient("sk-test", "gpt-6-luna")
	client.BaseURL = server.URL

	raw, err := client.JSON(context.Background(), "answer", schema(), "system", "user")
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if string(raw) != `{"ok":true}` {
		t.Errorf("answer = %s", raw)
	}

	if request["model"] != "gpt-6-luna" {
		t.Errorf("model = %v", request["model"])
	}
	format, _ := request["response_format"].(map[string]any)
	if format["type"] != "json_schema" {
		t.Errorf("response_format = %v", format)
	}
	defined, _ := format["json_schema"].(map[string]any)
	if defined["strict"] != true || defined["name"] != "answer" {
		t.Errorf("json_schema = %v", defined)
	}
}

func TestJSONWithoutAKey(t *testing.T) {
	client := NewClient("", "gpt-6-luna")

	_, err := client.JSON(context.Background(), "answer", schema(), "system", "user")
	if err == nil || !strings.Contains(err.Error(), "OPENAI_API_KEY") {
		t.Fatalf("err = %v, want a hint about the key", err)
	}
}

// A rejection has to name the thing that went wrong, because "status 401" on
// its own does not tell anybody what to do about it.
func TestJSONExplainsARejection(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{
			name:   "bad key",
			status: http.StatusUnauthorized,
			body:   `{"error":{"message":"Incorrect API key provided","type":"invalid_request_error","code":"invalid_api_key"}}`,
			want:   "rejected the key",
		},
		{
			name:   "quota",
			status: http.StatusTooManyRequests,
			body:   `{"error":{"message":"You exceeded your current quota","type":"insufficient_quota","code":"insufficient_quota"}}`,
			want:   "quota",
		},
		{
			name:   "model",
			status: http.StatusNotFound,
			body:   `{"error":{"message":"The model does not exist","type":"invalid_request_error","code":"model_not_found"}}`,
			want:   "does not offer that model",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(c.status)
				_, _ = w.Write([]byte(c.body))
			}))
			defer server.Close()

			client := NewClient("sk-test", "gpt-6-luna")
			client.BaseURL = server.URL

			_, err := client.JSON(context.Background(), "answer", schema(), "system", "user")
			if err == nil {
				t.Fatal("a rejection was reported as success")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %q, want it to mention %q", err, c.want)
			}

			var apiErr *Error
			if !errors.As(err, &apiErr) || apiErr.Status != c.status {
				t.Errorf("err = %v, want an *Error with status %d", err, c.status)
			}
		})
	}
}

func TestJSONWithoutChoices(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer server.Close()

	client := NewClient("sk-test", "gpt-6-luna")
	client.BaseURL = server.URL

	if _, err := client.JSON(context.Background(), "answer", schema(), "system", "user"); err == nil {
		t.Fatal("an empty answer was accepted")
	}
}

func TestJSONReportsARefusal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":null,"refusal":"I cannot help with that."}}]}`))
	}))
	defer server.Close()

	client := NewClient("sk-test", "gpt-6-luna")
	client.BaseURL = server.URL

	_, err := client.JSON(context.Background(), "answer", schema(), "system", "user")
	if err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("err = %v, want it to report the refusal", err)
	}
}

// A model that will not take a JSON schema still has to answer in JSON: the
// request is retried once without the schema rather than failing a command
// over a provider quirk.
func TestJSONRetriesWithoutTheSchema(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"response_format json_schema is not supported for this model"}}`))
			return
		}

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding the retry: %v", err)
		}
		if _, ok := body["response_format"]; ok {
			t.Error("the retry still asked for a schema")
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"{\"ok\":true}"}}]}`))
	}))
	defer server.Close()

	client := NewClient("sk-test", "gpt-6-luna")
	client.BaseURL = server.URL

	raw, err := client.JSON(context.Background(), "answer", schema(), "system", "user")
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if string(raw) != `{"ok":true}` {
		t.Errorf("answer = %s", raw)
	}
	if requests != 2 {
		t.Errorf("made %d requests, want 2", requests)
	}
}
