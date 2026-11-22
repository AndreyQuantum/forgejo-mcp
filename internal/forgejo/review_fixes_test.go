package forgejo

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRequestTimeoutOverridesConfigTimeoutForHeaders(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(400 * time.Millisecond) // headers arrive after cfg.Timeout
		_, _ = io.WriteString(w, `{}`)
	}), func(cfg *Config) { cfg.Timeout = 150 * time.Millisecond })

	if _, err := c.Do(context.Background(), Request{Method: "POST", Path: "/x"}); err == nil {
		t.Fatal("want timeout with the config timeout")
	}
	if _, err := c.Do(context.Background(), Request{Method: "POST", Path: "/x", Timeout: 5 * time.Second}); err != nil {
		t.Fatalf("Request.Timeout must extend the header wait: %v", err)
	}
}

func TestRawBodyIsStreamedNotBuffered(t *testing.T) {
	var gotLen int64
	var gotBody string
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotLen = r.ContentLength
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusCreated)
	}), nil)
	// an opaque reader: not a bytes.Reader, so net/http cannot infer a length
	body := struct{ io.Reader }{strings.NewReader("hello")}
	if _, err := c.Do(context.Background(), Request{Method: "POST", Path: "/x", Body: body, ContentLength: 5, ContentType: "text/plain"}); err != nil {
		t.Fatal(err)
	}
	if gotLen != 5 || gotBody != "hello" {
		t.Errorf("len=%d body=%q", gotLen, gotBody)
	}
	if _, err := c.Do(context.Background(), Request{Method: "POST", Path: "/x", Body: struct{ io.Reader }{strings.NewReader("abc")}}); err != nil {
		t.Fatal(err)
	}
	if gotLen != -1 || gotBody != "abc" {
		t.Errorf("unknown length: len=%d body=%q", gotLen, gotBody)
	}
}

func TestDotSegmentsRejected(t *testing.T) {
	var hits atomic.Int32
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, `{}`)
	}), nil)
	for _, p := range []string{
		Path("repos", "o", "..", "branches"),
		Path("repos", "o", "r", "contents", Wild("../../../admin/users")),
		Path("repos", "o", "r", "branches", Wild("a/./b")),
		"/repos/o/%2e%2e/x",
	} {
		_, err := c.Do(context.Background(), Request{Path: p})
		if err == nil || !strings.Contains(err.Error(), "'..'") {
			t.Errorf("%s: want dot-segment error, got %v", p, err)
		}
	}
	if hits.Load() != 0 {
		t.Errorf("%d requests reached the server", hits.Load())
	}
	if _, err := c.Do(context.Background(), Request{Path: Path("repos", "o", "r.git", Wild("a..b/c"))}); err != nil {
		t.Errorf("dots inside names are fine: %v", err)
	}
}

func TestLimitsFallbackCached(t *testing.T) {
	var hits atomic.Int32
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/settings/api") {
			hits.Add(1)
			http.Error(w, `{"message":"nope"}`, http.StatusForbidden)
			return
		}
		_, _ = io.WriteString(w, `[]`)
	}), nil)
	for range 3 {
		if d, m := c.Limits(context.Background()); d != 30 || m != 50 {
			t.Fatalf("limits = %d/%d", d, m)
		}
	}
	if hits.Load() != 1 {
		t.Errorf("settings/api requested %d times, want 1", hits.Load())
	}
}

func TestMergeAndDeleteHints(t *testing.T) {
	e := mapErr("POST", "/repos/o/r/pulls/1/merge", 405, `{"message":"not allowed to merge [reason: Head branch is behind base]"}`, true)
	if strings.Contains(e.Error(), "write access") || !strings.Contains(e.Error(), "pr update") {
		t.Errorf("behind: %s", e.Error())
	}
	e = mapErr("POST", "/repos/o/r/pulls/1/merge", 405, `{"message":"User not allowed to merge PR"}`, true)
	if !strings.Contains(e.Error(), "write access") {
		t.Errorf("permission: %s", e.Error())
	}
	e = mapErr("DELETE", "/repos/o/r/contents/a.txt", 400, `{"message":"file CommitID does not match [given: a, expected: b]"}`, true)
	if !strings.Contains(e.Error(), "file changed since read") {
		t.Errorf("commit id: %s", e.Error())
	}
}
