// Package api wires the HTTP surface: routing, middleware, authentication,
// admission control and the two DeepSeek-compatible handlers.
package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/Wu-jiyan/one-search-api/internal/auth"
	"github.com/Wu-jiyan/one-search-api/internal/config"
	"github.com/Wu-jiyan/one-search-api/internal/fetchsvc"
	"github.com/Wu-jiyan/one-search-api/internal/httpx"
	"github.com/Wu-jiyan/one-search-api/internal/limit"
	"github.com/Wu-jiyan/one-search-api/internal/obs"
	"github.com/Wu-jiyan/one-search-api/internal/query"
	"github.com/Wu-jiyan/one-search-api/internal/search"
)

// The API key is the traffic meter for outbound fetches.
var _ fetchsvc.Meter = (*auth.Key)(nil)

type ctxKey int

const (
	ctxRequestID ctxKey = iota
	ctxRecorder
	ctxInfo
)

// Server holds the HTTP dependencies.
type Server struct {
	cfg     *config.Config
	auth    *auth.Authenticator
	search  *search.Service
	fetch   *fetchsvc.Service
	metrics *obs.Metrics
	logger  *slog.Logger
	version string

	sem      *limit.Semaphore
	inflight atomic.Int64
	started  time.Time

	trusted []trustedNet
	mux     *http.ServeMux
}

type trustedNet struct {
	ipNet *net.IPNet
	ip    net.IP
}

type recorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
	written     int
}

func (r *recorder) WriteHeader(code int) {
	if r.wroteHeader {
		return
	}
	r.wroteHeader = true
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	n, err := r.ResponseWriter.Write(b)
	r.written += n
	return n, err
}

// reqInfo is mutable per-request state filled in by handlers and read by the
// logging middleware after the handler returns.
type reqInfo struct {
	keyName     string
	resultCount int
}

// New creates the API server.
func New(cfg *config.Config, a *auth.Authenticator, s *search.Service, f *fetchsvc.Service,
	metrics *obs.Metrics, logger *slog.Logger, version string) *Server {
	srv := &Server{
		cfg:     cfg,
		auth:    a,
		search:  s,
		fetch:   f,
		metrics: metrics,
		logger:  logger,
		version: version,
		sem:     limit.NewSemaphore(cfg.Search.MaxConcurrency),
		started: time.Now(),
	}
	srv.trusted = parseTrusted(cfg.Server.TrustedProxies)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/tools/search", srv.handleSearch)
	mux.HandleFunc("POST /v1/tools/fetch", srv.handleFetch)
	mux.HandleFunc("GET /healthz", srv.handleHealthz)
	mux.HandleFunc("GET /metrics", metrics.Handler().ServeHTTP)
	mux.HandleFunc("GET /{$}", srv.handleRoot)
	mux.HandleFunc("/", srv.handleNotFound)
	srv.mux = mux
	return srv
}

// Handler returns the fully wrapped HTTP handler.
func (s *Server) Handler() http.Handler {
	var h http.Handler = s.mux
	h = s.withLogging(h)
	h = s.withRequestID(h)
	h = s.withCORS(h)
	h = s.withRecover(h)
	return h
}

// withCORS mirrors the reference API's permissive CORS behavior: echo the
// request origin, allow credentials, allow POST and the JSON/Authorization
// headers, and expose the request id. OPTIONS preflight is answered directly.
func (s *Server) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Credentials", "true")
			h.Set("Access-Control-Expose-Headers", "X-Request-Id")
			h.Add("Vary", "Origin")
			h.Add("Vary", "Access-Control-Request-Method")
			h.Add("Vary", "Access-Control-Request-Headers")
		}
		if r.Method == http.MethodOptions {
			h := w.Header()
			if origin != "" {
				h.Set("Access-Control-Allow-Methods", "POST")
				allowed := r.Header.Get("Access-Control-Request-Headers")
				if allowed == "" {
					allowed = "authorization,content-type"
				}
				h.Set("Access-Control-Allow-Headers", allowed)
				h.Set("Access-Control-Max-Age", "86400")
			}
			h.Set("Allow", "POST")
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) withRecover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec, _ := w.(*recorder)
		if rec == nil {
			rec = &recorder{ResponseWriter: w}
		}
		ctx := context.WithValue(r.Context(), ctxRecorder, rec)
		info := &reqInfo{}
		ctx = context.WithValue(ctx, ctxInfo, info)
		defer func() {
			if v := recover(); v != nil {
				s.logger.Error("panic serving request",
					"panic", v, "path", r.URL.Path, "request_id", requestIDFrom(ctx))
				if !rec.wroteHeader {
					httpx.WriteError(rec, httpx.NewAPIError(http.StatusInternalServerError, httpx.TypeServer, "internal server error"))
				}
			}
		}()
		next.ServeHTTP(rec, r.WithContext(ctx))
	})
}

func (s *Server) withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := randomID()
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxRequestID, id)))
	})
}

func (s *Server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec, _ := r.Context().Value(ctxRecorder).(*recorder)
		if rec == nil {
			rec = &recorder{ResponseWriter: w}
			r = r.WithContext(context.WithValue(r.Context(), ctxRecorder, rec))
		}
		info, _ := r.Context().Value(ctxInfo).(*reqInfo)

		start := time.Now()
		next.ServeHTTP(rec, r)
		dur := time.Since(start)

		status := rec.status
		if status == 0 {
			status = http.StatusOK
		}
		ep := endpointOf(r.URL.Path)
		s.metrics.Inc("onesearch_requests_total", "endpoint", ep, "status", strconv.Itoa(status))
		s.metrics.ObserveDuration("onesearch_request_duration_seconds", dur, "endpoint", ep)
		if ep == "metrics" {
			return
		}

		attrs := []any{
			"endpoint", ep,
			"method", r.Method,
			"path", r.URL.Path,
			"status", status,
			"duration_ms", dur.Milliseconds(),
			"request_id", requestIDFrom(r.Context()),
			"remote", s.clientIP(r),
		}
		if info != nil && info.keyName != "" {
			attrs = append(attrs, "key", info.keyName, "results", info.resultCount)
		}
		if status >= 500 {
			s.logger.Error("request", attrs...)
		} else {
			s.logger.Info("request", attrs...)
		}
	})
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.Server.RequestTimeout.D())
	defer cancel()
	r = r.WithContext(ctx)

	// The reference API authenticates before looking at the body, so a missing
	// or invalid key wins over any malformed payload.
	key, apiErr := s.authenticate(r)
	if apiErr != nil {
		s.fail(w, r, apiErr)
		return
	}
	body, apiErr := readJSONBody(w, r, s.cfg.Server.MaxBodyBytes)
	if apiErr != nil {
		s.fail(w, r, apiErr)
		return
	}
	var req httpx.SearchRequest
	if err := json.Unmarshal(body, &req); err != nil {
		s.fail(w, r, httpx.InvalidRequest("Invalid JSON request: "+err.Error()))
		return
	}

	q, count, needContent, apiErr := s.validateSearch(&req)
	if apiErr != nil {
		s.fail(w, r, apiErr)
		return
	}

	s.noteKey(r, key.Name)
	if apiErr := key.Admit(); apiErr != nil {
		s.metrics.Inc("onesearch_rate_limited_total", "reason", apiErr.Type)
		s.fail(w, r, apiErr)
		return
	}
	if key.MaxResults > 0 && count > key.MaxResults {
		count = key.MaxResults
	}

	if !s.acquire(w, r) {
		return
	}
	defer s.release()

	results, err := s.search.Search(ctx, q, count, needContent, key.AllowedSet(), key)
	if err != nil {
		s.writeError(w, err, r)
		return
	}
	s.noteResults(r, len(results))
	s.logger.Debug("search completed",
		"query", q.Raw, "count", count, "need_content", needContent, "results", len(results))

	httpx.WriteJSON(w, http.StatusOK, httpx.NewSearchResponse(results))
}

func (s *Server) handleFetch(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.Fetch.Timeout.D()+2*time.Second)
	defer cancel()
	r = r.WithContext(ctx)

	key, apiErr := s.authenticate(r)
	if apiErr != nil {
		s.fail(w, r, apiErr)
		return
	}
	body, apiErr := readJSONBody(w, r, s.cfg.Server.MaxBodyBytes)
	if apiErr != nil {
		s.fail(w, r, apiErr)
		return
	}
	var req httpx.FetchRequest
	if err := json.Unmarshal(body, &req); err != nil {
		s.fail(w, r, httpx.InvalidRequest("Invalid JSON request: "+err.Error()))
		return
	}
	if _, err := s.fetch.ValidateURL(req.URL); err != nil {
		s.writeError(w, fetchsvc.MapError(err), r)
		return
	}

	s.noteKey(r, key.Name)
	if apiErr := key.Admit(); apiErr != nil {
		s.metrics.Inc("onesearch_rate_limited_total", "reason", apiErr.Type)
		s.fail(w, r, apiErr)
		return
	}

	if !s.acquire(w, r) {
		return
	}
	defer s.release()

	page, err := s.fetch.Fetch(ctx, key, req.URL)
	if err != nil {
		s.writeError(w, err, r)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, page)
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	type providerJSON struct {
		Name  string `json:"name"`
		Type  string `json:"type"`
		State string `json:"state"`
	}
	states := s.search.ProviderStates()
	providers := make([]providerJSON, 0, len(states))
	for _, st := range states {
		providers = append(providers, providerJSON{Name: st.Name, Type: st.Type, State: st.State})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"status":         "ok",
		"version":        s.version,
		"uptime_seconds": int64(time.Since(s.started).Seconds()),
		"providers":      providers,
	})
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"service": "one-search-api",
		"version": s.version,
		"endpoints": []string{
			"POST /v1/tools/search",
			"POST /v1/tools/fetch",
			"GET /healthz",
			"GET /metrics",
		},
	})
}

func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/v1/tools/search", "/v1/tools/fetch":
		// The reference API answers 405 with an empty body and an Allow header.
		w.Header().Set("Allow", "POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	case "/healthz", "/metrics", "/":
		w.Header().Set("Allow", "GET")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	s.fail(w, r, httpx.NotFound("unknown endpoint: "+r.URL.Path))
}

func (s *Server) validateSearch(req *httpx.SearchRequest) (query.Query, int, bool, *httpx.APIError) {
	var q query.Query
	if strings.TrimSpace(req.Query) == "" {
		return q, 0, false, httpx.InvalidRequest("'query' must not be empty")
	}
	parsed, err := query.Parse(req.Query)
	if err != nil {
		if errors.Is(err, query.ErrTooLong) {
			return q, 0, false, httpx.InvalidRequest("'query' must be at most 128 characters")
		}
		return q, 0, false, httpx.InvalidRequest("'query' must not be empty")
	}
	q = parsed

	count := s.cfg.Search.DefaultCount
	if req.Count != nil {
		count = *req.Count
		if count < 1 || count > 50 {
			return q, 0, false, httpx.InvalidRequest("'count' must be between 1 and 50")
		}
	}
	if count > s.cfg.Search.MaxCount {
		count = s.cfg.Search.MaxCount
	}

	if req.OriQuestion != nil {
		n := utf8.RuneCountInString(*req.OriQuestion)
		if n < 1 || n > 128 {
			return q, 0, false, httpx.InvalidRequest("'ori_question' must be between 1 and 128 characters")
		}
	}

	needContent := false
	if req.NeedContent != nil {
		needContent = *req.NeedContent
	}
	return q, count, needContent, nil
}

// fail writes an error, appending the reference API's request-id suffix so
// clients that surface the message verbatim still see a correlation id.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, apiErr *httpx.APIError) {
	if id := requestIDFrom(r.Context()); id != "" {
		cp := *apiErr
		cp.Message = cp.Message + " (request_id: " + id + ")"
		apiErr = &cp
	}
	httpx.WriteError(w, apiErr)
}

// readJSONBody enforces the reference API's request framing rules: the body must
// be a JSON object sent as application/json.
func readJSONBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, *httpx.APIError) {
	ct := r.Header.Get("Content-Type")
	if ct != "" {
		if mt, _, err := mime.ParseMediaType(ct); err != nil || mt != "application/json" {
			return nil, httpx.InvalidRequest("Content-Type must be application/json")
		}
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		if isMaxBytes(err) {
			return nil, httpx.PayloadTooLarge("Request body exceeds the configured limit")
		}
		if isTimeoutError(err) {
			return nil, httpx.UpstreamTimeout("timed out reading request body")
		}
		return nil, httpx.InvalidRequest("failed to read request body")
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, httpx.InvalidRequest("Request body must be a JSON object")
	}
	return trimmed, nil
}

func (s *Server) authenticate(r *http.Request) (*auth.Key, *httpx.APIError) {
	return s.auth.Authenticate(bearerToken(r), s.clientIP(r))
}

func (s *Server) acquire(w http.ResponseWriter, r *http.Request) bool {
	if !s.sem.TryAcquire() {
		s.metrics.Inc("onesearch_rate_limited_total", "reason", "concurrency")
		s.fail(w, r, httpx.RateLimited("server is at capacity, retry later", time.Second))
		return false
	}
	s.inflight.Add(1)
	s.metrics.Set("onesearch_inflight", float64(s.inflight.Load()))
	return true
}

func (s *Server) release() {
	s.inflight.Add(-1)
	s.metrics.Set("onesearch_inflight", float64(s.inflight.Load()))
	s.sem.Release()
}

func (s *Server) writeError(w http.ResponseWriter, err error, r *http.Request) {
	apiErr := httpx.AsAPIError(err)
	if apiErr.Status >= 500 {
		s.logger.Error("upstream failure",
			"error", err.Error(), "path", r.URL.Path, "request_id", requestIDFrom(r.Context()))
	}
	s.fail(w, r, apiErr)
}

func (s *Server) noteKey(r *http.Request, name string) {
	if info, ok := r.Context().Value(ctxInfo).(*reqInfo); ok {
		info.keyName = name
	}
}

func (s *Server) noteResults(r *http.Request, n int) {
	if info, ok := r.Context().Value(ctxInfo).(*reqInfo); ok {
		info.resultCount = n
	}
}

func (s *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if !s.isTrusted(host) {
		return host
	}
	xff := r.Header.Get("X-Forwarded-For")
	if xff == "" {
		if v := r.Header.Get("X-Real-IP"); v != "" {
			return strings.TrimSpace(v)
		}
		return host
	}
	parts := strings.Split(xff, ",")
	for i := len(parts) - 1; i >= 0; i-- {
		ip := strings.TrimSpace(parts[i])
		if ip == "" {
			continue
		}
		if !s.isTrusted(ip) {
			return ip
		}
	}
	return strings.TrimSpace(parts[0])
}

func (s *Server) isTrusted(ipStr string) bool {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}
	for _, t := range s.trusted {
		if t.ipNet != nil && t.ipNet.Contains(ip) {
			return true
		}
		if t.ip != nil && t.ip.Equal(ip) {
			return true
		}
	}
	return false
}

func parseTrusted(list []string) []trustedNet {
	out := make([]trustedNet, 0, len(list))
	for _, raw := range list {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if strings.Contains(raw, "/") {
			if _, n, err := net.ParseCIDR(raw); err == nil {
				out = append(out, trustedNet{ipNet: n})
				continue
			}
			continue
		}
		if ip := net.ParseIP(raw); ip != nil {
			out = append(out, trustedNet{ip: ip})
		}
	}
	return out
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if h == "" {
		return ""
	}
	const prefix = "bearer "
	if len(h) < len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(h[len(prefix):])
}

func endpointOf(path string) string {
	switch path {
	case "/v1/tools/search":
		return "search"
	case "/v1/tools/fetch":
		return "fetch"
	case "/healthz":
		return "healthz"
	case "/metrics":
		return "metrics"
	default:
		return "other"
	}
}

func requestIDFrom(ctx context.Context) string {
	if v, ok := ctx.Value(ctxRequestID).(string); ok {
		return v
	}
	return ""
}

func randomID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b[:])
}

// isMaxBytes reports whether err is the MaxBytesReader overflow error.
func isMaxBytes(err error) bool {
	var mbe *http.MaxBytesError
	return errors.As(err, &mbe)
}

func isTimeoutError(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}
