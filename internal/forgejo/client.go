// Package forgejo is the HTTP client and API-level helpers (DESIGN.md
// sections 2, 5, 11).
package forgejo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/AndreyQuantum/forgejo-mcp/internal/clock"
)

const (
	defaultTimeout  = 30 * time.Second
	defaultMaxBody  = 8 << 20
	maxErrBody      = 64 << 10
	maxRedirects    = 3
	maxRetries      = 2
	maxRetryAfter   = 10 * time.Second
	versionTTL      = 0 // process lifetime
	meTTL           = 10 * time.Minute
	limitsFailTTL   = 5 * time.Minute
	fallbackDefault = 30
	fallbackMax     = 50
)

var backoffs = [maxRetries]time.Duration{300 * time.Millisecond, 900 * time.Millisecond}

// Config configures a Client.
type Config struct {
	BaseURL     string
	Token       string
	Timeout     time.Duration
	Sudo        string
	CAFile      string
	InsecureTLS bool
	UserAgent   string
	HTTPClient  *http.Client // tests inject a Transport
	Clock       clock.Clock  // nil = real; retries/backoff sleep through it
	Debug       bool         // log method, path, status and ms of every exchange to stderr
}

// Client talks to one Forgejo instance.
type Client struct {
	cfg     Config
	cache   *Cache
	base    *url.URL
	baseErr error
	hc      *http.Client
	clk     clock.Clock
	tok8    string
	debugLn *log.Logger
	redact  []string
}

// New returns a Client. A bad BaseURL or CAFile is reported by the first call.
func New(cfg Config) *Client {
	c := &Client{cfg: cfg}
	if c.cfg.Timeout <= 0 {
		c.cfg.Timeout = defaultTimeout
	}
	if c.cfg.UserAgent == "" {
		c.cfg.UserAgent = "forgejo-mcp"
	}
	c.clk = cfg.Clock
	if c.clk == nil {
		c.clk = clock.Real()
	}
	c.cache = NewCacheClock(c.clk)
	if cfg.Debug {
		c.debugLn = log.New(os.Stderr, "forgejo-mcp: ", log.LstdFlags)
	}

	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/"))
	switch {
	case err != nil:
		c.baseErr = fmt.Errorf("invalid FORGEJO_URL: %w", err)
	case u.Scheme != "http" && u.Scheme != "https", u.Host == "":
		c.baseErr = fmt.Errorf("invalid FORGEJO_URL %q: need http(s)://host", cfg.BaseURL)
	default:
		u.RawQuery, u.Fragment = "", ""
		c.base = u
	}

	c.hc, err = buildHTTPClient(c.cfg)
	if err != nil && c.baseErr == nil {
		c.baseErr = err
	}
	c.setToken(cfg.Token)
	return c
}

func (c *Client) setToken(tok string) {
	c.cfg.Token = tok
	if tok == "" {
		c.tok8 = "anon"
	} else {
		sum := sha256.Sum256([]byte(tok))
		c.tok8 = hex.EncodeToString(sum[:8])
	}
	c.redact = c.redact[:0:0]
	if tok != "" {
		c.redact = append(c.redact, tok)
	}
}

func buildHTTPClient(cfg Config) (*http.Client, error) {
	if cfg.HTTPClient != nil {
		hc := *cfg.HTTPClient
		hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		hc.Timeout = 0
		return &hc, nil
	}
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: cfg.InsecureTLS} //nolint:gosec // opt-in flag
	var err error
	if cfg.CAFile != "" {
		pem, rerr := os.ReadFile(cfg.CAFile)
		if rerr != nil {
			err = fmt.Errorf("cannot read FORGEJO_CA_FILE: %w", rerr)
		} else {
			pool, perr := x509.SystemCertPool()
			if perr != nil || pool == nil {
				pool = x509.NewCertPool()
			}
			if !pool.AppendCertsFromPEM(pem) {
				err = fmt.Errorf("FORGEJO_CA_FILE %s has no valid PEM certificate", cfg.CAFile)
			}
			tlsCfg.RootCAs = pool
		}
	}
	tr := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		DialContext:         (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSClientConfig:     tlsCfg,
		MaxIdleConnsPerHost: 8,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
		ForceAttemptHTTP2:   true,
	}
	return &http.Client{
		Transport:     tr,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, err
}

// WithToken returns a client that uses tok for this request chain. It shares
// the transport and the `version` and `settings/api` caches only; every other
// cache is namespaced by sha256(tok)[:8] and tok joins the redaction list.
func (c *Client) WithToken(tok string) *Client {
	n := *c
	prev := c.redact
	n.setToken(tok)
	// every token seen so far (the configured one included) stays redacted
	n.redact = append(n.redact, prev...)
	return &n
}

// Token returns the token this client sends (empty when anonymous).
func (c *Client) Token() string { return c.cfg.Token }

// BaseURL returns the instance URL without trailing slash.
func (c *Client) BaseURL() string {
	if c.base == nil {
		return ""
	}
	return c.base.String()
}

// Redact replaces every token known to this client in s with `***`.
func (c *Client) Redact(s string) string {
	for _, t := range c.redact {
		if t != "" {
			s = strings.ReplaceAll(s, t, "***")
		}
	}
	return s
}

// CacheKey returns a cache key namespaced by instance and token
// (`host|<tok8>|parts...`, section 5.5).
func (c *Client) CacheKey(parts ...string) string {
	return c.host() + "|" + c.tok8 + "|" + strings.Join(parts, "|")
}

// SharedCacheKey returns a key shared by all tokens (`host|name`); only the
// `version` and `settings/api` entries use it.
func (c *Client) SharedCacheKey(name string) string { return c.host() + "|" + name }

func (c *Client) host() string {
	if c.base == nil {
		return ""
	}
	return c.base.Host + c.base.Path
}

// Request describes one API call.
type Request struct {
	Method, Path  string // Path relative to /api/v1, ALREADY escaped (use Path()/Seg())
	Query         url.Values
	JSON          any       // marshalled, sets Content-Type: application/json
	Body          io.Reader // raw body (file upload); with ContentType. Streamed for non-GET/HEAD
	ContentLength int64     // length of Body when known (> 0); otherwise the body is sent chunked
	ContentType   string
	Header        http.Header   // extra, e.g. Range
	Timeout       time.Duration // override (default cfg.Timeout)
	MaxBody       int64         // default 8 MiB; excess is dropped and Response.Truncated=true
	API           string        // "" = /api/v1, "forgejo" = /api/forgejo/v1, "root" = no prefix
}

// Response is a successful (2xx) answer.
type Response struct {
	Status    int
	Header    http.Header
	Body      []byte
	Truncated bool
	Total     int64 // X-Total-Count, or -1 when absent
}

// buildURL builds the request URL from the already-escaped Request.Path so a
// %20 is never turned into %2520.
func (c *Client) buildURL(r Request) (*url.URL, error) {
	if c.baseErr != nil {
		return nil, c.baseErr
	}
	prefix := strings.TrimRight(c.base.EscapedPath(), "/")
	switch r.API {
	case "":
		prefix += "/api/v1"
	case "forgejo":
		prefix += "/api/forgejo/v1"
	case "root":
	default:
		return nil, fmt.Errorf("unknown API selector %q", r.API)
	}
	p := r.Path
	if p != "" && !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	raw := prefix + p
	plain, err := url.PathUnescape(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid path %q: %w", r.Path, err)
	}
	if err := rejectDotSegments(p); err != nil {
		return nil, err
	}
	u := &url.URL{Scheme: c.base.Scheme, Host: c.base.Host, Path: plain, RawPath: raw}
	if len(r.Query) > 0 {
		u.RawQuery = r.Query.Encode()
	}
	return u, nil
}

// rejectDotSegments refuses a request path with a "." or ".." segment, also
// when percent-encoded: proxies in front of Forgejo may normalise them and
// reach another endpoint than the one the tool policy checked.
func rejectDotSegments(escaped string) error {
	for _, seg := range strings.Split(escaped, "/") {
		if u, err := url.PathUnescape(seg); err == nil && (u == "." || u == "..") {
			return errors.New("invalid: path segment '.' or '..' is not allowed")
		}
	}
	return nil
}

// attemptResult is the outcome of one HTTP exchange (redirects resolved).
type attemptResult struct {
	status      int
	header      http.Header
	body        []byte
	truncated   bool
	written     int64
	contentType string
}

// do runs the request with retries. w != nil streams a 2xx body into w.
func (c *Client) do(ctx context.Context, r Request, w io.Writer) (*attemptResult, error) {
	if r.Method == "" {
		r.Method = http.MethodGet
	}
	u, err := c.buildURL(r)
	if err != nil {
		return nil, &APIError{Message: c.Redact(err.Error()), Method: r.Method, Path: r.Path}
	}
	var payload []byte
	switch {
	case r.JSON != nil:
		payload, err = json.Marshal(r.JSON)
		if err != nil {
			return nil, &APIError{Message: "invalid: cannot encode request body: " + err.Error(), Method: r.Method, Path: r.Path}
		}
	case r.Body != nil && w == nil && idempotentMethod(r.Method):
		payload, err = io.ReadAll(r.Body)
		if err != nil {
			return nil, &APIError{Message: "invalid: cannot read request body: " + err.Error(), Method: r.Method, Path: r.Path}
		}
	}
	idempotent := idempotentMethod(r.Method)
	retries := 0
	if idempotent {
		retries = maxRetries
	}

	var last *APIError
	for attempt := 0; ; attempt++ {
		res, aerr, retryAfter := c.attempt(ctx, r, u, payload, w)
		if aerr == nil && res.status >= 200 && res.status < 300 {
			return res, nil
		}
		if aerr == nil {
			aerr = c.statusError(r, res, attempt)
		}
		last = aerr
		retryable := aerr.retryable && (res == nil || res.written == 0)
		if !retryable || attempt >= retries || ctx.Err() != nil {
			if aerr.retryable && aerr.Status != 0 && idempotent {
				aerr.head = fmt.Sprintf("%d server busy/unavailable (retried %dx)", aerr.Status, attempt)
			} else if aerr.retryable && aerr.Status != 0 {
				aerr.head = fmt.Sprintf("%d server busy/unavailable", aerr.Status)
			}
			return nil, aerr
		}
		d := jitter(backoffs[attempt])
		if retryAfter > 0 {
			d = min(retryAfter, maxRetryAfter)
		}
		if serr := c.clk.Sleep(ctx, d); serr != nil {
			return nil, last
		}
	}
}

func idempotentMethod(m string) bool { return m == http.MethodGet || m == http.MethodHead }

func jitter(d time.Duration) time.Duration {
	f := 0.8 + 0.4*rand.Float64() //nolint:gosec // jitter, not security
	return time.Duration(float64(d) * f)
}

// attempt performs one exchange. It returns either a result (any status), or
// an *APIError for transport failures. retryAfter is the parsed header of a
// retryable status.
func (c *Client) attempt(ctx context.Context, r Request, u *url.URL, payload []byte, w io.Writer) (*attemptResult, *APIError, time.Duration) {
	timeout := r.Timeout
	if timeout <= 0 && w == nil {
		timeout = c.cfg.Timeout
	}
	actx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		actx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	tmo := timeout
	if tmo <= 0 {
		tmo = c.cfg.Timeout
	}

	cur := u
	for hop := 0; ; hop++ {
		req, err := c.newRequest(actx, r, cur, payload, w != nil)
		if err != nil {
			return nil, &APIError{Message: c.Redact(err.Error()), Method: r.Method, Path: r.Path}, 0
		}
		if hop > 0 && !c.sameHost(cur) {
			req.Header.Del("Authorization")
			req.Header.Del("Sudo")
		}
		began := time.Now()
		resp, err := c.hc.Do(req)
		c.debugf(r, resp, err, began)
		if err != nil {
			return nil, c.transportError(ctx, actx, r, tmo, err), 0
		}

		if resp.StatusCode >= 300 && resp.StatusCode < 400 && resp.StatusCode != http.StatusNotModified {
			loc := resp.Header.Get("Location")
			_ = resp.Body.Close()
			if loc == "" {
				return &attemptResult{status: resp.StatusCode, header: resp.Header}, nil, 0
			}
			next, perr := cur.Parse(loc)
			if perr != nil || (next.Scheme != "http" && next.Scheme != "https") {
				return &attemptResult{status: resp.StatusCode, header: resp.Header}, nil, 0
			}
			isGet := r.Method == http.MethodGet || r.Method == http.MethodHead
			canFollow := isGet && hop < maxRedirects && (w != nil || c.sameHost(next))
			if !canFollow {
				res := &attemptResult{status: resp.StatusCode, header: resp.Header}
				res.header = resp.Header.Clone()
				res.header.Set("Location", next.String())
				return res, nil, 0
			}
			cur = next
			continue
		}

		res, aerr := c.readResult(actx, ctx, r, resp, w, tmo)
		if aerr != nil {
			return res, aerr, 0
		}
		return res, nil, retryAfterOf(resp)
	}
}

// debugf logs one exchange when FORGEJO_DEBUG is on: never headers or bodies.
func (c *Client) debugf(r Request, resp *http.Response, err error, began time.Time) {
	if c.debugLn == nil {
		return
	}
	status := "error"
	if err == nil {
		status = strconv.Itoa(resp.StatusCode)
	}
	c.debugLn.Printf("http %s %s -> %s %dms", r.Method, c.Redact(r.Path), status, time.Since(began).Milliseconds())
}

func (c *Client) readResult(actx, ctx context.Context, r Request, resp *http.Response, w io.Writer, tmo time.Duration) (*attemptResult, *APIError) {
	defer resp.Body.Close()
	res := &attemptResult{status: resp.StatusCode, header: resp.Header, contentType: resp.Header.Get("Content-Type")}
	ok := resp.StatusCode >= 200 && resp.StatusCode < 300
	var err error
	switch {
	case ok && w != nil:
		res.written, err = io.Copy(w, resp.Body)
	case ok:
		limit := r.MaxBody
		if limit <= 0 {
			limit = defaultMaxBody
		}
		var buf []byte
		buf, err = io.ReadAll(io.LimitReader(resp.Body, limit+1))
		if int64(len(buf)) > limit {
			buf, res.truncated = buf[:limit], true
		}
		res.body = buf
	default:
		res.body, err = io.ReadAll(io.LimitReader(resp.Body, maxErrBody))
	}
	if err != nil {
		return res, c.transportError(ctx, actx, r, tmo, err)
	}
	return res, nil
}

func (c *Client) newRequest(ctx context.Context, r Request, u *url.URL, payload []byte, download bool) (*http.Request, error) {
	var body io.Reader
	streamed := false
	switch {
	case payload != nil:
		body = bytes.NewReader(payload)
	case r.JSON == nil && r.Body != nil && !download && !idempotentMethod(r.Method):
		// mutations are never retried or redirected, so the body streams
		body, streamed = r.Body, true
	}
	req, err := http.NewRequestWithContext(ctx, r.Method, u.String(), body)
	if err != nil {
		return nil, err
	}
	if streamed {
		switch {
		case r.ContentLength > 0:
			req.ContentLength = r.ContentLength
		default:
			if f, ok := r.Body.(*os.File); ok {
				if fi, serr := f.Stat(); serr == nil && fi.Mode().IsRegular() {
					if pos, perr := f.Seek(0, io.SeekCurrent); perr == nil && fi.Size() > pos {
						req.ContentLength = fi.Size() - pos
					}
				}
			}
		}
		if req.ContentLength == 0 {
			req.ContentLength = -1 // unknown: chunked
		}
	}
	h := req.Header
	if download {
		h.Set("Accept", "*/*")
	} else {
		h.Set("Accept", "application/json")
	}
	h.Set("User-Agent", c.cfg.UserAgent)
	if c.cfg.Token != "" {
		h.Set("Authorization", "token "+c.cfg.Token)
	}
	if c.cfg.Sudo != "" {
		h.Set("Sudo", c.cfg.Sudo)
	}
	switch {
	case r.JSON != nil:
		h.Set("Content-Type", "application/json")
	case (payload != nil || streamed) && r.ContentType != "":
		h.Set("Content-Type", r.ContentType)
	}
	for k, vs := range r.Header {
		h.Del(k)
		for _, v := range vs {
			h.Add(k, v)
		}
	}
	return req, nil
}

func (c *Client) sameHost(u *url.URL) bool {
	return c.base != nil && strings.EqualFold(u.Host, c.base.Host)
}

func (c *Client) transportError(parent, actx context.Context, r Request, tmo time.Duration, err error) *APIError {
	e := &APIError{Method: r.Method, Path: r.Path}
	switch {
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(actx.Err(), context.DeadlineExceeded):
		shown := tmo
		if tmo >= time.Second {
			shown = tmo.Round(time.Second)
		}
		e.Message = fmt.Sprintf("timeout after %s calling %s %s", shown, r.Method, r.Path)
		return e
	case parent.Err() != nil:
		e.Message = "cancelled: " + parent.Err().Error()
		return e
	}
	host := ""
	if c.base != nil {
		host = c.base.Host
	}
	e.Message = c.Redact(fmt.Sprintf("cannot reach %s: %s", host, causeText(err)))
	e.Hint = "check FORGEJO_URL / network"
	e.retryable = true
	return e
}

func causeText(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return err.Error()
}

func retryAfterOf(resp *http.Response) time.Duration {
	switch resp.StatusCode {
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
	default:
		return 0
	}
	v := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if v == "" {
		return 0
	}
	if n, err := strconv.Atoi(v); err == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// statusError turns a non-2xx exchange into an *APIError.
func (c *Client) statusError(r Request, res *attemptResult, _ int) *APIError {
	if res.status >= 300 && res.status < 400 {
		return c.redirectError(r, res)
	}
	e := newAPIError(r.Method, r.Path, res.status, res.contentType, res.body, c.cfg.Token != "", c.Redact)
	switch res.status {
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		e.retryable = true
	}
	return e
}

func (c *Client) redirectError(r Request, res *attemptResult) *APIError {
	e := &APIError{Status: res.status, Method: r.Method, Path: r.Path}
	loc := res.header.Get("Location")
	lp := loc
	if u, err := url.Parse(loc); err == nil && u.Path != "" {
		lp = u.Path
		if c.base != nil && u.Host != "" && !c.sameHost(u) {
			lp = u.Host + u.Path
		}
	}
	switch res.status {
	case http.StatusMovedPermanently, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		e.Message = "moved: " + lp
		e.head = "moved to " + lp
		e.Hint = "repo or user was renamed; retry with the new name"
	default:
		e.Message = "unexpected redirect to " + lp
	}
	e.Message = c.Redact(e.Message)
	return e
}

// Do performs the request. 2xx returns a nil error; anything else *APIError.
func (c *Client) Do(ctx context.Context, r Request) (*Response, error) {
	res, err := c.do(ctx, r, nil)
	if err != nil {
		return nil, err
	}
	return toResponse(res), nil
}

func toResponse(res *attemptResult) *Response {
	resp := &Response{Status: res.status, Header: res.header, Body: res.body, Truncated: res.truncated, Total: -1}
	if v := res.header.Get("X-Total-Count"); v != "" {
		if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil && n >= 0 {
			resp.Total = n
		}
	}
	return resp
}

// Download streams a GET body into w (no MaxBody). It follows up to 3
// redirects including cross-host ones and drops Authorization and Sudo on
// every hop whose host differs from the instance host.
func (c *Client) Download(ctx context.Context, r Request, w io.Writer) (n int64, resp *Response, err error) {
	if r.Method != "" && r.Method != http.MethodGet {
		return 0, nil, &APIError{Message: "invalid: download needs GET", Method: r.Method, Path: r.Path}
	}
	r.Method = http.MethodGet
	res, err := c.do(ctx, r, w)
	if err != nil {
		return 0, nil, err
	}
	return res.written, toResponse(res), nil
}

// GetJSON performs a GET and decodes the JSON body into out.
func (c *Client) GetJSON(ctx context.Context, path string, q url.Values, out any) (*Response, error) {
	resp, err := c.Do(ctx, Request{Method: http.MethodGet, Path: path, Query: q})
	if err != nil {
		return nil, err
	}
	if out == nil {
		return resp, nil
	}
	if len(bytes.TrimSpace(resp.Body)) == 0 {
		return resp, &APIError{Message: "empty response", Method: http.MethodGet, Path: path}
	}
	if err := decodeBody(resp, out, http.MethodGet, path); err != nil {
		return resp, err
	}
	return resp, nil
}

func decodeBody(resp *Response, out any, method, path string) error {
	if resp.Truncated {
		return &APIError{Message: "response too large to decode", Method: method, Path: path}
	}
	if err := json.Unmarshal(resp.Body, out); err != nil {
		return &APIError{Message: "invalid JSON in response: " + err.Error(), Method: method, Path: path}
	}
	return nil
}

// SendJSON sends in as JSON; out may be nil; 204/empty bodies are fine.
func (c *Client) SendJSON(ctx context.Context, method, path string, q url.Values, in, out any) (*Response, error) {
	resp, err := c.Do(ctx, Request{Method: method, Path: path, Query: q, JSON: in})
	if err != nil {
		return nil, err
	}
	if out != nil && resp.Status != http.StatusNoContent && len(bytes.TrimSpace(resp.Body)) > 0 {
		if err := decodeBody(resp, out, method, path); err != nil {
			return resp, err
		}
	}
	return resp, nil
}

// ServerVersion returns the parsed /version, cached for the process lifetime.
func (c *Client) ServerVersion(ctx context.Context) (Version, error) {
	v, err := c.cache.Get(c.SharedCacheKey("version"), versionTTL, func() (any, error) {
		var body struct {
			Version string `json:"version"`
		}
		if _, err := c.GetJSON(ctx, "/version", nil, &body); err != nil {
			return nil, err
		}
		return ParseVersion(body.Version)
	})
	if err != nil {
		return Version{}, err
	}
	return v.(Version), nil
}

// Limits returns the default and max page size from /settings/api (cached;
// falls back to 30/50).
func (c *Client) Limits(ctx context.Context) (def, maxItems int) {
	v, err := c.cache.Get(c.SharedCacheKey("settings/api"), 0, func() (any, error) {
		var s APISettings
		if _, err := c.GetJSON(ctx, "/settings/api", nil, &s); err != nil {
			return nil, err
		}
		return s, nil
	})
	if err != nil {
		// remember the fallback for a while so every list does not pay a
		// failing (and retried) /settings/api request
		c.cache.Put(c.SharedCacheKey("settings/api"), APISettings{DefaultPagingNum: fallbackDefault, MaxResponseItems: fallbackMax}, limitsFailTTL)
		return fallbackDefault, fallbackMax
	}
	s := v.(APISettings)
	def, maxItems = s.DefaultPagingNum, s.MaxResponseItems
	if def <= 0 {
		def = fallbackDefault
	}
	if maxItems <= 0 {
		maxItems = fallbackMax
	}
	return def, maxItems
}

// Me returns the authenticated user (cached 10 min).
func (c *Client) Me(ctx context.Context) (*User, error) {
	v, err := c.cache.Get(c.CacheKey("me"), meTTL, func() (any, error) {
		var u User
		if _, err := c.GetJSON(ctx, "/user", nil, &u); err != nil {
			return nil, err
		}
		return &u, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*User), nil
}

// List fetches one page into out, sets page/limit and clamps limit to the
// server maximum. A limit <= 0 means the server default page size. out is
// usually a pointer to a slice; bodies that are objects with `total_count`
// (Actions lists) are decoded as is and their total is used when the
// X-Total-Count header is absent.
func (c *Client) List(ctx context.Context, path string, q url.Values, page, limit int, out any) (Page, error) {
	def, maxItems := c.Limits(ctx)
	if page < 1 {
		page = 1
	}
	if limit <= 0 {
		limit = def
	}
	limit = min(limit, maxItems)
	qq := url.Values{}
	for k, v := range q {
		qq[k] = append([]string(nil), v...)
	}
	qq.Set("page", strconv.Itoa(page))
	qq.Set("limit", strconv.Itoa(limit))

	resp, err := c.GetJSON(ctx, path, qq, out)
	if err != nil {
		var ae *APIError
		if errors.As(err, &ae) && ae.Message == "empty response" {
			// an empty body on a list is an empty list
			return PageFrom(resp, page, limit, 0), nil
		}
		return Page{}, err
	}
	shown := 0
	body := bytes.TrimSpace(resp.Body)
	switch {
	case len(body) > 0 && body[0] == '[':
		var items []json.RawMessage
		if json.Unmarshal(body, &items) == nil {
			shown = len(items)
		}
	case len(body) > 0 && body[0] == '{':
		var obj map[string]json.RawMessage
		if json.Unmarshal(body, &obj) == nil {
			if tc, ok := obj["total_count"]; ok && resp.Total < 0 {
				if n, err := strconv.ParseInt(string(tc), 10, 64); err == nil && n >= 0 {
					resp.Total = n
				}
			}
			for k, v := range obj {
				if k == "total_count" || len(v) == 0 || v[0] != '[' {
					continue
				}
				var items []json.RawMessage
				if json.Unmarshal(v, &items) == nil {
					shown = len(items)
				}
				break
			}
		}
	}
	return PageFrom(resp, page, limit, shown), nil
}

// Cache returns the client's TTL cache (shared by WithToken clones; keys are
// namespaced through CacheKey).
func (c *Client) Cache() *Cache { return c.cache }
