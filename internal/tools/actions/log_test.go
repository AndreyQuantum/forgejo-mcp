package actions

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/AndreyQuantum/forgejo-mcp/internal/testutil"
)

// stored log: timestamp prefix, ANSI colour, group markers, a repeated line.
func storedLog() string {
	var b strings.Builder
	n := 0
	add := func(s string) {
		fmt.Fprintf(&b, "2026-11-22T09:50:%02d.1234567Z %s\n", n%60, s)
		n++
	}
	add("::group::Run tests")
	add("\x1b[36mgo test ./...\x1b[0m")
	add("::endgroup::")
	for range 4 {
		add("downloading")
	}
	add("ok pkg/a 0.01s")
	add("before-failure")
	add("FAIL TestSessionRefresh (0.02s)")
	add("    session_test.go:44: expected 200, got 500")
	return b.String()
}

// logRoutes serves job 5678 (failed, step 1 failing) and its log with Range.
func logRoutes(s *fakeServer, body string) {
	s.reply("GET /repos/o/r/actions/jobs/5678", 200, jobDetail())
	s.on("GET /repos/o/r/actions/jobs/5678/logs", func(w http.ResponseWriter, r *http.Request) {
		if q := r.URL.Query().Get("q"); q != "" {
			for _, l := range strings.Split(strings.TrimRight(body, "\n"), "\n") {
				ll, qq := l, q
				if r.URL.Query().Get("qi") == "true" {
					ll, qq = strings.ToLower(l), strings.ToLower(q)
				}
				if strings.Contains(ll, qq) {
					fmt.Fprintln(w, l)
				}
			}
			return
		}
		http.ServeContent(w, r, "job.log", time.Time{}, strings.NewReader(body))
	})
}

func TestLogFailedStepDefaultGolden(t *testing.T) {
	s := newFake(t)
	logRoutes(s, storedLog())
	text := runOK(t, s, map[string]any{"action": "log", "job": 5678, "tail": 6})
	testutil.Golden(t, "log_failed_step", text)
	r := s.only("GET /repos/o/r/actions/jobs/5678/logs")
	wantQuery(t, r, map[string][]string{"step": {"1"}})
	if got := r.Header.Get("Range"); got != "bytes=-16000" {
		t.Errorf("Range = %q, want bytes=-16000 (2 x default max_chars 8000)", got)
	}
}

func TestLogMaxCharsAndNumericStep(t *testing.T) {
	s := newFake(t)
	logRoutes(s, storedLog())
	// a JSON number is accepted for step
	runOK(t, s, map[string]any{"action": "log", "job": 5678, "step": 0, "max_chars": 1000})
	r := s.only("GET /repos/o/r/actions/jobs/5678/logs")
	wantQuery(t, r, map[string][]string{"step": {"0"}})
	if got := r.Header.Get("Range"); got != "bytes=-2000" {
		t.Errorf("Range = %q, want bytes=-2000", got)
	}
}

func TestLogByRunAndName(t *testing.T) {
	s := newFake(t)
	logRoutes(s, storedLog())
	s.reply("GET /repos/o/r/actions/runs/1234/jobs", 200, jobsBody())
	text := runOK(t, s, map[string]any{"action": "log", "run": 1234, "name": "Build", "step": "failed", "tail": 3})
	if !strings.HasPrefix(text, `job 5678 "build" attempt 1 step 1 "Run tests" [failure]`) {
		t.Fatalf("header: %q", text)
	}
	if !strings.Contains(text, "expected 200, got 500") {
		t.Fatalf("tail missing: %q", text)
	}
}

func TestLogByNumberAndName(t *testing.T) {
	s := newFake(t)
	logRoutes(s, storedLog())
	s.reply("GET /repos/o/r/actions/runs", 200,
		runsBody(1, runJSON(1234, 56, "failure", "ci.yml", "wip", "push", "main", sha2, "alice", "2026-11-22T09:50:00Z", 0)))
	s.reply("GET /repos/o/r/actions/runs/1234/jobs", 200, jobsBody())
	runOK(t, s, map[string]any{"action": "log", "number": 56, "name": "build"})
	wantQuery(t, s.only("GET /repos/o/r/actions/runs"), map[string][]string{"run_number": {"56"}})
}

func TestLogGrep(t *testing.T) {
	s := newFake(t)
	logRoutes(s, storedLog())
	text := runOK(t, s, map[string]any{"action": "log", "job": 5678, "grep": "FAIL"})
	if !strings.Contains(text, "matches 1 of 1 lines containing") || !strings.Contains(text, "FAIL TestSessionRefresh") || strings.Contains(text, "before-failure") {
		t.Fatalf("got %q", text)
	}
	text = runOK(t, s, map[string]any{"action": "log", "job": 5678, "grep": "FAIL", "ignore_case": true})
	if !strings.Contains(text, "before-failure") {
		t.Fatalf("ignore_case must match lower case too: %q", text)
	}
	got := s.seen("GET /repos/o/r/actions/jobs/5678/logs")
	if len(got) != 2 {
		t.Fatalf("want 2 log requests, got %d", len(got))
	}
	wantQuery(t, got[0], map[string][]string{"q": {"FAIL"}, "qi": nil})
	wantQuery(t, got[1], map[string][]string{"q": {"FAIL"}, "qi": {"true"}})
}

func TestLogFollowOffset(t *testing.T) {
	s := newFake(t)
	body := storedLog()
	logRoutes(s, body)
	text := runOK(t, s, map[string]any{"action": "log", "job": 5678, "step": "0", "offset": 40})
	if got := s.only("GET /repos/o/r/actions/jobs/5678/logs").Header.Get("Range"); !strings.HasPrefix(got, "bytes=40-") {
		t.Fatalf("Range = %q", got)
	}
	if !strings.Contains(text, fmt.Sprintf("next_offset: %d", len(body))) {
		t.Fatalf("next_offset missing: %q", text)
	}
}

func TestLogNotExecutedIsNotAnError(t *testing.T) {
	s := newFake(t)
	s.reply("GET /repos/o/r/actions/jobs/5680", 200,
		`{"id":5680,"name":"test","status":"skipped","attempt":1,"task_id":0}`)
	s.reply("GET /repos/o/r/actions/jobs/5680/logs", 404, `{"message":"job has not been executed yet"}`)
	text := runOK(t, s, map[string]any{"action": "log", "job": 5680})
	if !strings.Contains(text, "no log: job has not been executed yet") {
		t.Fatalf("got %q", text)
	}
}

func TestLogValidation(t *testing.T) {
	s := newFake(t)
	for _, tc := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"action": "log"}, "error: invalid: pass job, or run (or number) with name"},
		{map[string]any{"action": "log", "run": 1}, "error: invalid: pass job, or run (or number) with name"},
		{map[string]any{"action": "log", "job": 1, "step": "abc"}, `error: invalid: step "abc"`},
		{map[string]any{"action": "log", "run": 1, "number": 2, "name": "x"}, "error: invalid: give only one of run, number"},
	} {
		got := runErr(t, s, tc.args)
		if !strings.HasPrefix(got, tc.want) {
			t.Errorf("%v: got %q, want prefix %q", tc.args, got, tc.want)
		}
	}
}
