// Package httpx holds the DeepSeek-compatible request/response wire types and
// JSON/error helpers.
package httpx

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

// SearchRequest is POST /v1/tools/search.
type SearchRequest struct {
	Query        string  `json:"query"`
	Count        *int    `json:"count"`
	OriQuestion  *string `json:"ori_question"`
	Localization *string `json:"localization"`
	NeedContent  *bool   `json:"need_content"`
}

// SearchResult is one entry of the results array. Every field is always
// serialized; nullable fields are pointers so they render as JSON null.
type SearchResult struct {
	RerankScore float64    `json:"rerank_score"`
	Title       string     `json:"title"`
	URL         string     `json:"url"`
	Snippet     string     `json:"snippet"`
	Summary     string     `json:"summary"`
	Content     string     `json:"content"`
	SiteName    *string    `json:"site_name"`
	SiteIcon    *string    `json:"site_icon"`
	Date        *time.Time `json:"date"`
	Authority   int        `json:"authority"`
}

// SearchResponse is the 200 body for /v1/tools/search.
type SearchResponse struct {
	Results []SearchResult `json:"results"`
}

// NewSearchResponse guarantees a non-nil slice so JSON emits [] not null.
func NewSearchResponse(results []SearchResult) SearchResponse {
	if results == nil {
		results = []SearchResult{}
	}
	return SearchResponse{Results: results}
}

// FetchRequest is POST /v1/tools/fetch.
type FetchRequest struct {
	URL string `json:"url"`
}

// FetchResponse is the 200 body for /v1/tools/fetch.
type FetchResponse struct {
	Title   string `json:"title"`
	Content string `json:"content"`
}

// ErrorBody is the OpenAI-compatible error envelope.
type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail carries the error payload. Param is always null.
type ErrorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Param   any    `json:"param"`
	Code    string `json:"code"`
}

// Error types/codes, kept in the OpenAI vocabulary used by DeepSeek.
const (
	TypeInvalidRequest    = "invalid_request_error"
	TypeAuthentication    = "authentication_error"
	TypeInsufficientQuota = "insufficient_quota"
	TypePermission        = "permission_error"
	TypeNotFound          = "not_found_error"
	TypeTimeout           = "timeout_error"
	TypeRateLimit         = "rate_limit_error"
	TypeServer            = "server_error"
)

// APIError is an error that carries an HTTP status and wire error payload.
type APIError struct {
	Status     int
	Type       string
	Code       string
	Message    string
	RetryAfter time.Duration
}

func (e *APIError) Error() string { return e.Message }

// NewAPIError builds an APIError with Code defaulting to Type.
func NewAPIError(status int, typ, message string) *APIError {
	return &APIError{Status: status, Type: typ, Code: typ, Message: message}
}

// InvalidRequest builds a 400 error.
func InvalidRequest(msg string) *APIError {
	return NewAPIError(http.StatusBadRequest, TypeInvalidRequest, msg)
}

// NotFound builds a 404 error.
func NotFound(msg string) *APIError {
	return NewAPIError(http.StatusNotFound, TypeNotFound, msg)
}

// Forbidden builds a 403 error.
func Forbidden(msg string) *APIError {
	return NewAPIError(http.StatusForbidden, TypePermission, msg)
}

// Unauthorized builds a 401 error.
func Unauthorized(msg string) *APIError {
	e := NewAPIError(http.StatusUnauthorized, TypeAuthentication, msg)
	e.Code = "invalid_api_key"
	return e
}

// RateLimited builds a 429 error with a Retry-After hint.
func RateLimited(msg string, retryAfter time.Duration) *APIError {
	e := NewAPIError(http.StatusTooManyRequests, TypeRateLimit, msg)
	e.Code = "rate_limit_exceeded"
	e.RetryAfter = retryAfter
	return e
}

// QuotaExceeded builds a 402 error.
func QuotaExceeded(msg string) *APIError {
	e := NewAPIError(http.StatusPaymentRequired, TypeInsufficientQuota, msg)
	e.Code = "insufficient_quota"
	return e
}

// UpstreamTimeout builds a 504 error.
func UpstreamTimeout(msg string) *APIError {
	e := NewAPIError(http.StatusGatewayTimeout, TypeTimeout, msg)
	e.Code = "upstream_timeout"
	return e
}

// AsAPIError unwraps or wraps an arbitrary error as a 500 APIError.
func AsAPIError(err error) *APIError {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr
	}
	return NewAPIError(http.StatusInternalServerError, TypeServer, err.Error())
}

// WriteJSON writes v as JSON with the given status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// WriteError writes an APIError envelope.
func WriteError(w http.ResponseWriter, err *APIError) {
	if err.RetryAfter > 0 {
		secs := int(err.RetryAfter.Seconds())
		if secs < 1 {
			secs = 1
		}
		w.Header().Set("Retry-After", itoa(secs))
	}
	body := ErrorBody{Error: ErrorDetail{
		Message: err.Message,
		Type:    err.Type,
		Param:   nil,
		Code:    err.Code,
	}}
	if body.Error.Code == "" {
		body.Error.Code = body.Error.Type
	}
	WriteJSON(w, err.Status, body)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
