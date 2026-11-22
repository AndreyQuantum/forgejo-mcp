package forgejo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AndreyQuantum/forgejo-mcp/internal/clock"
)

func newTestClient(t *testing.T, h http.Handler, mod func(*Config)) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	cfg := Config{BaseURL: srv.URL, Token: "tok-main", UserAgent: "forgejo-mcp/test", Timeout: 5 * time.Second}
	if mod != nil {
		mod(&cfg)
	}
	return New(cfg), srv
}

// autoAdvance fires every pending fake timer until stop is closed; it returns
// the list of requested sleep lengths is not tracked, only that timers fire.
func autoAdvance(t *testing.T, fc *clock.Fake) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for fc.BlockUntil(ctx, 1) {
			fc.Advance(time.Minute)
		}
	}()
	return func() { cancel(); <-done }
}

func asAPIError(t *testing.T, err error) *APIError {
	t.Helper()
	var ae *APIError
	if !errors.As(err, &ae) {
		t.Fatalf("want *APIError, got %T: %v", err, err)
	}
	return ae
}

func TestRequestHeadersAndPathEscaping(t *testing.T) {
	var got *http.Request
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		w.Header().Set("X-Total-Count", "7")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}), func(c *Config) { c.Sudo = "alice" })

	p := Path("repos", "o", "r", "contents", Wild("dir with space/a%b.txt"), "x/y")
	resp, err := c.Do(context.Background(), Request{Method: "GET", Path: p, Query: url.Values{"ref": {"feat/x"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Total != 7 || resp.Status != 200 {
		t.Fatalf("resp = %+v", resp)
	}
	if got.Header.Get("Authorization") != "token tok-main" || got.Header.Get("Sudo") != "alice" ||
		got.Header.Get("User-Agent") != "forgejo-mcp/test" || got.Header.Get("Accept") != "application/json" {
		t.Fatalf("headers: %v", got.Header)
	}
	if got.Header.Get("Content-Type") != "" {
		t.Fatalf("unexpected content type on GET")
	}
	wantRaw := "/api/v1/repos/o/r/contents/dir%20with%20space/a%25b.txt/x%2Fy"
	if got.URL.EscapedPath() != wantRaw {
		t.Fatalf("raw path = %s want %s", got.URL.EscapedPath(), wantRaw)
	}
	if got.URL.Query().Get("ref") != "feat/x" {
		t.Fatalf("query = %v", got.URL.Query())
	}
}

func TestSubPathBaseAndAPISelectors(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.EscapedPath())
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c := New(Config{BaseURL: srv.URL + "/forgejo/", Token: "t"})
	for _, api := range []string{"", "forgejo", "root"} {
		if _, err := c.Do(context.Background(), Request{Method: "GET", Path: "/x", API: api}); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"/forgejo/api/v1/x", "/forgejo/api/forgejo/v1/x", "/forgejo/x"}
	if strings.Join(paths, ",") != strings.Join(want, ",") {
		t.Fatalf("paths = %v want %v", paths, want)
	}
}

func TestAnonymousSendsNoAuthorization(t *testing.T) {
	var auth string
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{}`))
	}), func(c *Config) { c.Token = "" })
	if _, err := c.Do(context.Background(), Request{Method: "GET", Path: "/x"}); err != nil || auth != "" {
		t.Fatalf("err=%v auth=%q", err, auth)
	}
}

func TestSendJSONAndEmptyBodies(t *testing.T) {
	var ct, body string
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ct = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		if r.Method == "DELETE" {
			w.WriteHeader(204)
			return
		}
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"id":5}`))
	}), nil)
	var out struct{ ID int }
	resp, err := c.SendJSON(context.Background(), "POST", "/x", nil, map[string]any{"a": 1}, &out)
	if err != nil || resp.Status != 201 || out.ID != 5 || ct != "application/json" || body != `{"a":1}` {
		t.Fatalf("err=%v resp=%+v out=%+v ct=%s body=%s", err, resp, out, ct, body)
	}
	if _, err := c.SendJSON(context.Background(), "DELETE", "/x", nil, nil, &out); err != nil {
		t.Fatalf("204 with out: %v", err)
	}
}

func TestGetJSONEmptyAndInvalid(t *testing.T) {
	mode := "empty"
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if mode == "bad" {
			_, _ = w.Write([]byte(`{nope`))
		}
	}), nil)
	var v map[string]any
	_, err := c.GetJSON(context.Background(), "/x", nil, &v)
	if ae := asAPIError(t, err); ae.Error() != "error: empty response" {
		t.Fatalf("got %q", ae.Error())
	}
	mode = "bad"
	_, err = c.GetJSON(context.Background(), "/x", nil, &v)
	if ae := asAPIError(t, err); !strings.HasPrefix(ae.Error(), "error: invalid JSON in response") {
		t.Fatalf("got %q", ae.Error())
	}
}

func TestMaxBodyTruncates(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("a"), 100))
	}), nil)
	resp, err := c.Do(context.Background(), Request{Method: "GET", Path: "/x", MaxBody: 10})
	if err != nil || !resp.Truncated || len(resp.Body) != 10 {
		t.Fatalf("err=%v resp=%+v", err, resp)
	}
	resp, err = c.Do(context.Background(), Request{Method: "GET", Path: "/x"})
	if err != nil || resp.Truncated || len(resp.Body) != 100 {
		t.Fatalf("default: err=%v trunc=%v len=%d", err, resp.Truncated, len(resp.Body))
	}
}

func TestRetryGETWithFakeClock(t *testing.T) {
	var n atomic.Int32
	fc := clock.NewFake(time.Time{})
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}), func(c *Config) { c.Clock = fc })

	type result struct {
		resp *Response
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		resp, err := c.Do(context.Background(), Request{Method: "GET", Path: "/x"})
		ch <- result{resp, err}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// first backoff 300ms +-20%
	if !fc.BlockUntil(ctx, 1) {
		t.Fatal("no first sleep")
	}
	fc.Advance(239 * time.Millisecond) // below the minimum 240ms
	if fc.Pending() != 1 {
		t.Fatal("first backoff fired too early")
	}
	fc.Advance(122 * time.Millisecond) // total 361ms, above max 360ms
	// second backoff 900ms +-20%
	waitPending(t, fc, 1)
	if n.Load() != 2 {
		t.Fatalf("requests after first backoff = %d", n.Load())
	}
	fc.Advance(719 * time.Millisecond)
	if fc.Pending() != 1 {
		t.Fatal("second backoff fired too early")
	}
	fc.Advance(362 * time.Millisecond)
	r := <-ch
	if r.err != nil || n.Load() != 3 {
		t.Fatalf("err=%v requests=%d", r.err, n.Load())
	}
}

// waitPending waits until exactly the next sleep is parked after a request.
func waitPending(t *testing.T, fc *clock.Fake, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if fc.Pending() >= n {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("pending timers < %d", n)
}

func TestRetryAfterHeader(t *testing.T) {
	var n atomic.Int32
	fc := clock.NewFake(time.Time{})
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			w.Header().Set("Retry-After", "5")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}), func(c *Config) { c.Clock = fc })
	ch := make(chan error, 1)
	go func() {
		_, err := c.Do(context.Background(), Request{Method: "GET", Path: "/x"})
		ch <- err
	}()
	waitPending(t, fc, 1)
	fc.Advance(4900 * time.Millisecond)
	if fc.Pending() != 1 {
		t.Fatal("Retry-After 5s not honoured")
	}
	fc.Advance(200 * time.Millisecond)
	if err := <-ch; err != nil {
		t.Fatal(err)
	}
}

func TestRetryAfterCappedAtTenSeconds(t *testing.T) {
	var n atomic.Int32
	fc := clock.NewFake(time.Time{})
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			w.Header().Set("Retry-After", "3600")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}), func(c *Config) { c.Clock = fc })
	ch := make(chan error, 1)
	go func() {
		_, err := c.Do(context.Background(), Request{Method: "GET", Path: "/x"})
		ch <- err
	}()
	waitPending(t, fc, 1)
	fc.Advance(10 * time.Second)
	if err := <-ch; err != nil {
		t.Fatal(err)
	}
}

func TestRetriesExhaustedAndMutationsNotRetried(t *testing.T) {
	var n atomic.Int32
	fc := clock.NewFake(time.Time{})
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html>bad gateway</html>"))
	}), func(c *Config) { c.Clock = fc })

	stop := autoAdvance(t, fc)
	_, err := c.Do(context.Background(), Request{Method: "GET", Path: "/x"})
	stop()
	ae := asAPIError(t, err)
	if n.Load() != 3 || !strings.HasPrefix(ae.Error(), "error: 502 server busy/unavailable (retried 2x)") {
		t.Fatalf("requests=%d err=%q", n.Load(), ae.Error())
	}
	if !strings.Contains(ae.Error(), "\nat: GET /x") {
		t.Fatalf("missing at line: %q", ae.Error())
	}

	n.Store(0)
	_, err = c.Do(context.Background(), Request{Method: "POST", Path: "/x", JSON: map[string]any{}})
	ae = asAPIError(t, err)
	if n.Load() != 1 || ae.Error() != "error: 502 server busy/unavailable\nat: POST /x" {
		t.Fatalf("POST requests=%d err=%q", n.Load(), ae.Error())
	}
}

func TestNetworkErrorRetriedThenReported(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	base := srv.URL
	srv.Close() // nothing listens any more
	fc := clock.NewFake(time.Time{})
	c := New(Config{BaseURL: base, Token: "tok-network", Clock: fc, Timeout: 2 * time.Second})
	stop := autoAdvance(t, fc)
	_, err := c.Do(context.Background(), Request{Method: "GET", Path: "/x"})
	stop()
	ae := asAPIError(t, err)
	if !strings.HasPrefix(ae.Error(), "error: cannot reach 127.0.0.1:") || !strings.Contains(ae.Error(), "\nhint: check FORGEJO_URL / network") {
		t.Fatalf("got %q", ae.Error())
	}
	// POST: no retry, same message
	_, err = c.Do(context.Background(), Request{Method: "POST", Path: "/x"})
	if ae := asAPIError(t, err); !strings.HasPrefix(ae.Error(), "error: cannot reach") {
		t.Fatalf("got %q", ae.Error())
	}
}

func TestTimeoutMessage(t *testing.T) {
	release := make(chan struct{})
	c, srv := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}), nil)
	defer close(release)
	_ = srv
	_, err := c.Do(context.Background(), Request{Method: "POST", Path: "/slow", Timeout: 50 * time.Millisecond})
	ae := asAPIError(t, err)
	if ae.Error() != "error: timeout after 50ms calling POST /slow" {
		t.Fatalf("got %q", ae.Error())
	}
}

func TestContextCancelled(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}), nil)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(30*time.Millisecond, cancel)
	_, err := c.Do(ctx, Request{Method: "POST", Path: "/x"})
	if ae := asAPIError(t, err); !strings.HasPrefix(ae.Error(), "error: cancelled") {
		t.Fatalf("got %q", ae.Error())
	}
}

func TestRedirectRules(t *testing.T) {
	var hits []string
	var auth []string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/old", func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, r.Method+" old")
		http.Redirect(w, r, "/api/v1/new", http.StatusMovedPermanently)
	})
	mux.HandleFunc("/api/v1/new", func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, r.Method+" new")
		auth = append(auth, r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{"ok":1}`))
	})
	mux.HandleFunc("/api/v1/loop", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/api/v1/loop", http.StatusFound)
	})
	var other atomic.Int32
	otherSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { other.Add(1) }))
	defer otherSrv.Close()
	mux.HandleFunc("/api/v1/away", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, otherSrv.URL+"/steal", http.StatusFound)
	})
	c, _ := newTestClient(t, mux, nil)

	if _, err := c.Do(context.Background(), Request{Method: "GET", Path: "/old"}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(hits, ",") != "GET old,GET new" || auth[0] != "token tok-main" {
		t.Fatalf("hits=%v auth=%v", hits, auth)
	}

	// non-GET never follows
	hits = nil
	_, err := c.Do(context.Background(), Request{Method: "POST", Path: "/old", JSON: map[string]any{}})
	ae := asAPIError(t, err)
	if ae.Status != 301 || ae.Error() != "error: moved to /api/v1/new\nhint: repo or user was renamed; retry with the new name" || len(hits) != 1 {
		t.Fatalf("err=%q hits=%v", ae.Error(), hits)
	}

	// loop: gives up after 3 redirects
	_, err = c.Do(context.Background(), Request{Method: "GET", Path: "/loop"})
	if ae := asAPIError(t, err); ae.Status != 302 {
		t.Fatalf("loop err = %v", err)
	}

	// Do never crosses hosts
	_, err = c.Do(context.Background(), Request{Method: "GET", Path: "/away"})
	asAPIError(t, err)
	if other.Load() != 0 {
		t.Fatal("Do followed a redirect to another host")
	}
}

func TestDownloadCrossHostDropsCredentials(t *testing.T) {
	var seenAuth, seenSudo string
	var storageHits atomic.Int32
	storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		storageHits.Add(1)
		seenAuth, seenSudo = r.Header.Get("Authorization"), r.Header.Get("Sudo")
		_, _ = w.Write([]byte("payload"))
	}))
	defer storage.Close()
	var apiAuth string
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiAuth = r.Header.Get("Authorization")
		http.Redirect(w, r, storage.URL+"/bucket/obj?sig=abc", http.StatusFound)
	}), func(c *Config) { c.Sudo = "bob" })

	var buf bytes.Buffer
	n, resp, err := c.Download(context.Background(), Request{Path: "/repos/o/r/releases/assets/1"}, &buf)
	if err != nil || n != 7 || buf.String() != "payload" || resp.Status != 200 {
		t.Fatalf("n=%d err=%v body=%q", n, err, buf.String())
	}
	if apiAuth != "token tok-main" {
		t.Fatal("first hop must carry the token")
	}
	if seenAuth != "" || seenSudo != "" {
		t.Fatalf("credentials leaked cross-host: auth=%q sudo=%q", seenAuth, seenSudo)
	}
}

func TestDownloadSameHostKeepsAuthAndTooManyRedirects(t *testing.T) {
	var auths []string
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auths = append(auths, r.Header.Get("Authorization"))
		switch {
		case strings.HasSuffix(r.URL.Path, "/a"):
			http.Redirect(w, r, "/api/v1/b", http.StatusFound)
		case strings.HasSuffix(r.URL.Path, "/b"):
			_, _ = w.Write([]byte("B"))
		default:
			http.Redirect(w, r, r.URL.Path, http.StatusFound)
		}
	}), nil)
	var buf bytes.Buffer
	if _, _, err := c.Download(context.Background(), Request{Path: "/a"}, &buf); err != nil || buf.String() != "B" {
		t.Fatalf("err=%v body=%q", err, buf.String())
	}
	if auths[0] != "token tok-main" || auths[1] != "token tok-main" {
		t.Fatalf("auths = %v", auths)
	}
	if _, _, err := c.Download(context.Background(), Request{Path: "/loop"}, io.Discard); err == nil {
		t.Fatal("expected too many redirects error")
	}
	if _, _, err := c.Download(context.Background(), Request{Method: "POST", Path: "/a"}, io.Discard); err == nil {
		t.Fatal("download must refuse non-GET")
	}
}

func TestDownloadNoSizeLimit(t *testing.T) {
	const size = 9 << 20
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chunk := bytes.Repeat([]byte("z"), 1<<20)
		for range 9 {
			_, _ = w.Write(chunk)
		}
	}), nil)
	n, _, err := c.Download(context.Background(), Request{Path: "/big"}, io.Discard)
	if err != nil || n != size {
		t.Fatalf("n=%d err=%v", n, err)
	}
}

func TestDownloadErrorStatus(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"message":"GetAttachment","errors":["attachment does not exist"]}`))
	}), nil)
	var buf bytes.Buffer
	_, _, err := c.Download(context.Background(), Request{Path: "/x"}, &buf)
	ae := asAPIError(t, err)
	if buf.Len() != 0 || !strings.HasPrefix(ae.Error(), "error: 404 not found: attachment does not exist") {
		t.Fatalf("got %q", ae.Error())
	}
}

func TestTokenRedaction(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(422)
		_, _ = fmt.Fprintf(w, `{"message":"[Title]: bad value %s"}`, r.Header.Get("Authorization")[6:])
	}), nil)
	for _, cl := range []*Client{c, c.WithToken("per-request-secret")} {
		_, err := cl.Do(context.Background(), Request{Method: "POST", Path: "/x", JSON: map[string]any{}})
		ae := asAPIError(t, err)
		msg := ae.Error() + ae.Body
		if strings.Contains(msg, "tok-main") || strings.Contains(msg, "per-request-secret") || !strings.Contains(ae.Error(), "[Title]: bad value ***") {
			t.Fatalf("token leaked or missing mask: %q", msg)
		}
	}
}

func TestTwoTokenCacheIsolation(t *testing.T) {
	var versionHits, settingsHits, meHits atomic.Int32
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/version":
			versionHits.Add(1)
			_, _ = w.Write([]byte(`{"version":"17.0.2+gitea-1.22.0"}`))
		case "/api/v1/settings/api":
			settingsHits.Add(1)
			_, _ = w.Write([]byte(`{"default_paging_num":20,"max_response_items":40}`))
		case "/api/v1/user":
			meHits.Add(1)
			who := strings.TrimPrefix(r.Header.Get("Authorization"), "token ")
			_, _ = fmt.Fprintf(w, `{"login":%q}`, "user-of-"+who)
		}
	}), nil)
	a, b := c.WithToken("tok-a"), c.WithToken("tok-b")
	ctx := context.Background()

	for _, cl := range []*Client{c, a, b, a} {
		v, err := cl.ServerVersion(ctx)
		if err != nil || v.Major != 17 || v.Minor != 0 || v.Patch != 2 {
			t.Fatalf("version %+v err=%v", v, err)
		}
		if def, mx := cl.Limits(ctx); def != 20 || mx != 40 {
			t.Fatalf("limits %d/%d", def, mx)
		}
	}
	if versionHits.Load() != 1 || settingsHits.Load() != 1 {
		t.Fatalf("version/settings must be shared: %d/%d", versionHits.Load(), settingsHits.Load())
	}

	ua, _ := a.Me(ctx)
	ub, _ := b.Me(ctx)
	um, _ := c.Me(ctx)
	if ua.Login != "user-of-tok-a" || ub.Login != "user-of-tok-b" || um.Login != "user-of-tok-main" {
		t.Fatalf("identities mixed: %s %s %s", ua.Login, ub.Login, um.Login)
	}
	if _, _ = a.Me(ctx); meHits.Load() != 3 {
		t.Fatalf("me must be cached per token, hits=%d", meHits.Load())
	}

	// namespaced custom keys do not collide, and the cache is shared storage
	ka, kb := a.CacheKey("labels:o/r"), b.CacheKey("labels:o/r")
	if ka == kb || !strings.Contains(ka, "|") {
		t.Fatalf("keys: %q %q", ka, kb)
	}
	va, _ := a.Cache().Get(ka, time.Minute, func() (any, error) { return "private-labels", nil })
	vb, _ := b.Cache().Get(kb, time.Minute, func() (any, error) { return "other", nil })
	if va != "private-labels" || vb != "other" {
		t.Fatalf("cache leak: %v %v", va, vb)
	}
	if anon := New(Config{BaseURL: "http://h"}); !strings.Contains(anon.CacheKey("x"), "|anon|") {
		t.Fatalf("anon key: %s", anon.CacheKey("x"))
	}
	a.Cache().Invalidate(ka)
	if v, _ := b.Cache().Get(kb, time.Minute, func() (any, error) { return "reload", nil }); v != "other" {
		t.Fatal("invalidating one token's prefix must not touch another")
	}
}

func TestMeCacheExpiresOnFakeClock(t *testing.T) {
	var hits atomic.Int32
	fc := clock.NewFake(time.Time{})
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"login":"x"}`))
	}), func(c *Config) { c.Clock = fc })
	ctx := context.Background()
	_, _ = c.Me(ctx)
	fc.Advance(9 * time.Minute)
	_, _ = c.Me(ctx)
	if hits.Load() != 1 {
		t.Fatalf("hits=%d", hits.Load())
	}
	fc.Advance(2 * time.Minute)
	_, _ = c.Me(ctx)
	if hits.Load() != 2 {
		t.Fatalf("hits=%d", hits.Load())
	}
}

func TestLimitsFallbackAndVersionErrors(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/version" {
			_, _ = w.Write([]byte(`{"version":"weird"}`))
			return
		}
		w.WriteHeader(404)
	}), nil)
	if def, mx := c.Limits(context.Background()); def != 30 || mx != 50 {
		t.Fatalf("fallback = %d/%d", def, mx)
	}
	if _, err := c.ServerVersion(context.Background()); err == nil {
		t.Fatal("unparseable version must be an error")
	}
}

func TestListClampsAndPages(t *testing.T) {
	var q url.Values
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/settings/api" {
			_, _ = w.Write([]byte(`{"default_paging_num":20,"max_response_items":40}`))
			return
		}
		q = r.URL.Query()
		w.Header().Set("X-Total-Count", "95")
		_, _ = w.Write([]byte(`[{"name":"a"},{"name":"b"}]`))
	}), nil)
	in := url.Values{"state": {"open"}}
	var out []Label
	p, err := c.List(context.Background(), "/repos/o/r/labels", in, 2, 100, &out)
	if err != nil {
		t.Fatal(err)
	}
	if q.Get("limit") != "40" || q.Get("page") != "2" || q.Get("state") != "open" {
		t.Fatalf("query = %v", q)
	}
	if in.Get("limit") != "" {
		t.Fatal("caller's query was mutated")
	}
	if len(out) != 2 || p != (Page{Page: 2, Limit: 40, Total: 95, Shown: 2, More: true}) {
		t.Fatalf("out=%v page=%+v", out, p)
	}
	if _, err = c.List(context.Background(), "/x", nil, 0, 0, &out); err != nil || q.Get("limit") != "20" || q.Get("page") != "1" {
		t.Fatalf("defaults: err=%v q=%v", err, q)
	}
}

func TestListActionsStyleObjectBody(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/settings/api" {
			w.WriteHeader(404)
			return
		}
		_, _ = w.Write([]byte(`{"workflow_runs":[{"id":1},{"id":2}],"total_count":5}`))
	}), nil)
	var out struct {
		Runs []struct{ ID int } `json:"workflow_runs"`
	}
	p, err := c.List(context.Background(), "/repos/o/r/actions/runs", nil, 1, 2, &out)
	if err != nil || len(out.Runs) != 2 || p.Total != 5 || p.Shown != 2 || !p.More {
		t.Fatalf("p=%+v err=%v", p, err)
	}
}

func TestPageFrom(t *testing.T) {
	tests := []struct {
		name               string
		total              int64
		page, limit, shown int
		more               bool
	}{
		{"total known more", 134, 1, 20, 20, true},
		{"total known last", 134, 7, 20, 14, false},
		{"exact multiple", 40, 2, 20, 20, false},
		{"unknown full", -1, 1, 20, 20, true},
		{"unknown short", -1, 1, 20, 7, false},
		{"empty", 0, 1, 20, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := PageFrom(&Response{Total: tt.total}, tt.page, tt.limit, tt.shown)
			if p.More != tt.more || p.Total != tt.total || p.Shown != tt.shown {
				t.Fatalf("%+v", p)
			}
		})
	}
	if p := PageFrom(nil, 1, 10, 10); p.Total != -1 || !p.More {
		t.Fatalf("nil resp: %+v", p)
	}
	if p := PageUnknownTotal(1, 20, 20, 17); p.Total != -1 || !p.More || p.Shown != 17 {
		t.Fatalf("unknown total: %+v", p)
	}
}

func TestInvalidBaseURLReportedOnUse(t *testing.T) {
	for _, base := range []string{"", "ftp://x", "not a url", "//host"} {
		c := New(Config{BaseURL: base})
		_, err := c.Do(context.Background(), Request{Method: "GET", Path: "/x"})
		if ae := asAPIError(t, err); !strings.Contains(ae.Error(), "FORGEJO_URL") {
			t.Fatalf("base %q: %q", base, ae.Error())
		}
	}
}

func TestInjectedHTTPClientIsUsed(t *testing.T) {
	var used atomic.Bool
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		used.Store(true)
		return &http.Response{
			StatusCode: 200, Header: http.Header{"X-Total-Count": {"3"}},
			Body: io.NopCloser(strings.NewReader(`{"version":"16.1.0"}`)), Request: r,
		}, nil
	})
	c := New(Config{BaseURL: "https://forge.invalid", HTTPClient: &http.Client{Transport: rt}})
	v, err := c.ServerVersion(context.Background())
	if err != nil || !used.Load() || !v.Atleast(16) || v.Atleast(17) {
		t.Fatalf("v=%+v err=%v used=%v", v, err, used.Load())
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestConcurrentUse(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"login":"x","version":"17.0.0"}`))
	}), nil)
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = c.Me(context.Background())
			_, _ = c.ServerVersion(context.Background())
			_ = c.WithToken("t").CacheKey("x")
		}()
	}
	wg.Wait()
}
