package httpx

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWriteErrorEnvelope(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, RateLimited("slow down", time.Second))

	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "1" {
		t.Errorf("Retry-After = %q", got)
	}
	var body ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error.Code != "rate_limit_exceeded" || body.Error.Type != TypeRateLimit {
		t.Errorf("error = %+v", body.Error)
	}
	if body.Error.Param != nil {
		t.Errorf("param = %v, want null", body.Error.Param)
	}
	if !strings.Contains(rec.Body.String(), `"param":null`) {
		t.Errorf("param must be JSON null: %s", rec.Body.String())
	}
}

func TestErrorConstructors(t *testing.T) {
	cases := []struct {
		err    *APIError
		status int
		typ    string
		code   string
	}{
		{InvalidRequest("x"), http.StatusBadRequest, TypeInvalidRequest, TypeInvalidRequest},
		{Unauthorized("x"), http.StatusUnauthorized, TypeAuthentication, TypeInvalidRequest},
		{QuotaExceeded("x"), http.StatusPaymentRequired, TypeInsufficientQuota, "insufficient_quota"},
		{Forbidden("x"), http.StatusForbidden, TypePermission, TypePermission},
		{NotFound("x"), http.StatusNotFound, TypeNotFound, TypeInvalidRequest},
		{UpstreamTimeout("x"), http.StatusGatewayTimeout, TypeGatewayTimeout, TypeGatewayTimeout},
		{PayloadTooLarge("x"), http.StatusRequestEntityTooLarge, TypePayloadTooLarge, TypeInvalidRequest},
	}
	for _, tc := range cases {
		if tc.err.Status != tc.status || tc.err.Type != tc.typ || tc.err.Code != tc.code {
			t.Errorf("constructor mismatch: %+v", tc.err)
		}
	}
}

func TestAsAPIError(t *testing.T) {
	orig := InvalidRequest("bad")
	if got := AsAPIError(orig); got != orig {
		t.Errorf("AsAPIError should return the original *APIError")
	}
	got := AsAPIError(errors.New("boom"))
	if got.Status != http.StatusInternalServerError || got.Type != TypeServer {
		t.Errorf("generic error mapped to %+v", got)
	}
}

func TestSearchResponseEmptyIsArray(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteJSON(rec, http.StatusOK, NewSearchResponse(nil))
	if !strings.Contains(rec.Body.String(), `"results":[]`) {
		t.Errorf("results should be [] not null: %s", rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("content type = %q", ct)
	}
}

func TestDateWireFormat(t *testing.T) {
	ts := time.Date(2026, 10, 17, 8, 9, 10, 123456789, time.UTC)
	d := NewDate(ts)
	if d == nil {
		t.Fatal("NewDate returned nil")
	}
	got, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// The official API emits an explicit +00:00 offset, not "Z".
	if string(got) != `"2026-10-17T08:09:10+00:00"` {
		t.Errorf("date = %s", got)
	}
	if NewDate(time.Time{}) != nil {
		t.Error("zero time must marshal as null")
	}

	var back Date
	if err := json.Unmarshal(got, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !back.Time().Equal(ts.Truncate(time.Second)) {
		t.Errorf("round trip = %v", back.Time())
	}
	var nullable *Date
	if err := json.Unmarshal([]byte("null"), &nullable); err != nil {
		t.Fatalf("unmarshal null: %v", err)
	}
	if nullable != nil {
		t.Errorf("null decoded to %v", nullable)
	}
}

func TestSearchResultNullables(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteJSON(rec, http.StatusOK, NewSearchResponse([]SearchResult{{
		RerankScore: 1.5,
		Title:       "t",
		URL:         "https://example.com",
		Authority:   -1,
	}}))
	body := rec.Body.String()
	for _, want := range []string{`"site_name":null`, `"site_icon":null`, `"date":null`, `"content":""`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s in %s", want, body)
		}
	}
}
