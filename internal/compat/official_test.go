//go:build official

// Package compat holds the opt-in golden test that pins this project's
// DeepSeek-compatible contract to the behavior measured against the reference
// API. It is excluded from normal builds; run it with:
//
//	DEEPSEEK_API_KEY=sk-... go test ./internal/compat -tags official -v -count=1
//
// Each assertion below encodes a fact that was actually observed on
// api.deepseek.com (see docs/COMPAT.md §8). If the reference API changes, this
// test is the place to notice it first.
package compat

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

const baseURL = "https://api.deepseek.com"

func apiKey(t *testing.T) string {
	t.Helper()
	key := os.Getenv("DEEPSEEK_API_KEY")
	if key == "" {
		t.Skip("set DEEPSEEK_API_KEY to run the reference-API golden test")
	}
	return key
}

func post(t *testing.T, path, key string, payload string) (int, http.Header, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, baseURL+path, bytes.NewReader([]byte(payload)))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, resp.Header, body
}

// TestSearchShape pins the search response wire format.
func TestSearchShape(t *testing.T) {
	key := apiKey(t)

	status, header, body := post(t, "/v1/tools/search", key, `{"query":"deepseek v4 发布","count":2}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", status, body)
	}
	if ct := header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json (no charset)", ct)
	}

	var out struct {
		Results []map[string]json.RawMessage `json:"results"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Results) == 0 {
		t.Fatal("no results")
	}
	want := []string{
		"title", "site_name", "site_icon", "authority", "rerank_score",
		"date", "url", "summary", "snippet", "content",
	}
	for i, item := range out.Results {
		if len(item) != len(want) {
			t.Errorf("result[%d] has %d fields, want %d", i, len(item), len(want))
		}
		for _, k := range want {
			if _, ok := item[k]; !ok {
				t.Errorf("result[%d] missing %q", i, k)
			}
		}

		var date string
		raw := item["date"]
		if string(raw) != "null" {
			if err := json.Unmarshal(raw, &date); err != nil {
				t.Fatalf("date: %v", err)
			}
			// Measured: an explicit +00:00 offset, never a "Z" suffix.
			if !strings.HasSuffix(date, "+00:00") {
				t.Errorf("date = %q, want +00:00 offset", date)
			}
		}

		var icon string
		if string(item["site_icon"]) != "null" {
			_ = json.Unmarshal(item["site_icon"], &icon)
			if !strings.HasPrefix(icon, "https://cdn.deepseek.com/site-icons/") {
				t.Errorf("site_icon = %q", icon)
			}
		}

		var authority int
		_ = json.Unmarshal(item["authority"], &authority)
		if authority < -1 || authority > 4 {
			t.Errorf("authority = %d, out of range", authority)
		}

		var content string
		_ = json.Unmarshal(item["content"], &content)
		if content != "" {
			t.Errorf("content must be empty without need_content, got %d bytes", len(content))
		}
	}
}

// TestSearchCountAndNeedContent pins count handling and the fact that
// need_content fills every returned result, not just the first N.
func TestSearchCountAndNeedContent(t *testing.T) {
	key := apiKey(t)

	status, _, body := post(t, "/v1/tools/search", key, `{"query":"deepseek v4 发布","count":50}`)
	if status != http.StatusOK {
		t.Fatalf("count=50 status = %d, body = %s", status, body)
	}
	var many struct {
		Results []json.RawMessage `json:"results"`
	}
	if err := json.Unmarshal(body, &many); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(many.Results) != 50 {
		t.Errorf("count=50 returned %d results", len(many.Results))
	}

	status, _, body = post(t, "/v1/tools/search", key,
		`{"query":"golang context cancellation","count":8,"need_content":true}`)
	if status != http.StatusOK {
		t.Fatalf("need_content status = %d, body = %s", status, body)
	}
	var nc struct {
		Results []struct {
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &nc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	filled := 0
	for _, r := range nc.Results {
		if r.Content != "" {
			filled++
		}
	}
	if filled != len(nc.Results) {
		t.Errorf("need_content filled %d/%d results", filled, len(nc.Results))
	}
}

// TestSearchErrors pins status codes and envelope vocabulary.
func TestSearchErrors(t *testing.T) {
	key := apiKey(t)

	cases := []struct {
		name    string
		key     string
		payload string
		status  int
		typ     string
		code    string
		message string
	}{
		{"missing key", "", `{"query":"x"}`, 401, "authentication_error", "invalid_request_error", "Authentication Fails"},
		{"bad key", "sk-not-a-real-key-0000", `{"query":"x"}`, 401, "authentication_error", "invalid_request_error", "Authentication Fails"},
		{"blank query", key, `{"query":"   "}`, 400, "invalid_request_error", "invalid_request_error", "'query' must not be empty"},
		{"count zero", key, `{"query":"x","count":0}`, 400, "invalid_request_error", "invalid_request_error", "'count' must be between 1 and 50"},
		{"count too big", key, `{"query":"x","count":51}`, 400, "invalid_request_error", "invalid_request_error", "'count' must be between 1 and 50"},
		{"query too long", key, `{"query":"` + strings.Repeat("a", 129) + `"}`, 400, "invalid_request_error", "invalid_request_error", "'query' must be at most 128 characters"},
		{"ori_question empty", key, `{"query":"x","ori_question":""}`, 400, "invalid_request_error", "invalid_request_error", "'ori_question' must be between 1 and 128 characters"},
		{"non-object body", key, `[]`, 400, "invalid_request_error", "invalid_request_error", "Request body must be a JSON object"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, _, body := post(t, "/v1/tools/search", tc.key, tc.payload)
			if status != tc.status {
				t.Fatalf("status = %d, want %d, body = %s", status, tc.status, body)
			}
			var eb struct {
				Error struct {
					Message string `json:"message"`
					Type    string `json:"type"`
					Param   any    `json:"param"`
					Code    string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(body, &eb); err != nil {
				t.Fatalf("decode error body %q: %v", body, err)
			}
			if eb.Error.Type != tc.typ || eb.Error.Code != tc.code {
				t.Errorf("type/code = %q/%q, want %q/%q", eb.Error.Type, eb.Error.Code, tc.typ, tc.code)
			}
			if !strings.Contains(eb.Error.Message, tc.message) {
				t.Errorf("message = %q, want it to contain %q", eb.Error.Message, tc.message)
			}
			// Every measured error message carries a request-id suffix.
			if !strings.Contains(eb.Error.Message, "(request_id: ") {
				t.Errorf("message missing request_id suffix: %q", eb.Error.Message)
			}
			if eb.Error.Param != nil {
				t.Errorf("param = %v, want null", eb.Error.Param)
			}
		})
	}
}

// TestFetch pins fetch response shape and its (mildly surprising) status
// mapping: validation errors are 400, everything else is 404 or 504.
func TestFetch(t *testing.T) {
	key := apiKey(t)

	status, _, body := post(t, "/v1/tools/fetch", key, `{"url":"https://example.com/"}`)
	if status != http.StatusOK {
		t.Fatalf("example.com status = %d, body = %s", status, body)
	}
	var ok struct {
		Content string `json:"content"`
		Title   string `json:"title"`
	}
	if err := json.Unmarshal(body, &ok); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if ok.Content == "" || ok.Title == "" {
		t.Errorf("fetch = %+v, want non-empty title and content", ok)
	}

	cases := []struct {
		name   string
		url    string
		status int
		typ    string
		msg    string
	}{
		{"file scheme", "file:///etc/passwd", 400, "invalid_request_error", "must be an HTTP or HTTPS URL with a host"},
		{"ftp scheme", "ftp://example.com/x", 400, "invalid_request_error", "must be an HTTP or HTTPS URL with a host"},
		{"credentials", "https://user:pass@example.com/", 400, "invalid_request_error", "must not contain credentials"},
		{"blank url", "   ", 400, "invalid_request_error", "must be a valid HTTP or HTTPS URL"},
		{"too long", "https://example.com/" + strings.Repeat("a", 8200), 400, "invalid_request_error", "must be at most 8192 characters"},
		// Loopback, link-local metadata and DNS failures are opaque 404s.
		{"loopback", "http://127.0.0.1:8080/", 404, "not_found_error", "failed to fetch URL"},
		{"metadata", "http://169.254.169.254/latest/meta-data/", 404, "not_found_error", "failed to fetch URL"},
		{"text/xml", "https://httpbin.org/xml", 404, "not_found_error", "failed to fetch URL"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload, _ := json.Marshal(map[string]string{"url": tc.url})
			status, _, body := post(t, "/v1/tools/fetch", key, string(payload))
			if status != tc.status {
				t.Fatalf("status = %d, want %d, body = %.160s", status, tc.status, body)
			}
			var eb struct {
				Error struct {
					Message string `json:"message"`
					Type    string `json:"type"`
				} `json:"error"`
			}
			if err := json.Unmarshal(body, &eb); err != nil {
				t.Fatalf("decode error body %q: %v", body, err)
			}
			if eb.Error.Type != tc.typ {
				t.Errorf("type = %q, want %q", eb.Error.Type, tc.typ)
			}
			if !strings.Contains(eb.Error.Message, tc.msg) {
				t.Errorf("message = %q, want it to contain %q", eb.Error.Message, tc.msg)
			}
		})
	}
}

// TestMethodNotAllowed pins the empty-body 405 with an Allow header.
func TestMethodNotAllowed(t *testing.T) {
	key := apiKey(t)

	req, err := http.NewRequest(http.MethodGet, baseURL+"/v1/tools/search", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", resp.StatusCode)
	}
	if len(bytes.TrimSpace(body)) != 0 {
		t.Errorf("405 body = %q, want empty", body)
	}
	if allow := resp.Header.Get("Allow"); !strings.Contains(allow, "POST") {
		t.Errorf("Allow = %q, want POST", allow)
	}
}
