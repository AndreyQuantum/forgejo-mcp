package ci

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/AndreyQuantum/forgejo-mcp/internal/testutil"
)

const jobJSON = `{"id":5678,"name":"build","status":"failure","attempt":1,"task_id":9,"steps":[
 {"number":0,"name":"Set up job","status":"success"},
 {"number":1,"name":"Checkout","status":"success"},
 {"number":2,"name":"Run tests","status":"failure","started":"2026-11-22T09:50:00Z","stopped":"2026-11-22T09:50:12Z"},
 {"number":3,"name":"Complete job","status":"skipped"}]}`

// logBody builds a stored-format log: timestamps, ANSI, CR progress, groups
// and repeated lines.
func logBody() string {
	var b strings.Builder
	n := 0
	add := func(s string) {
		fmt.Fprintf(&b, "2026-11-22T09:50:%02d.1234567Z %s\n", n%60, s)
		n++
	}
	add("::group::Set up")
	add("\x1b[36mcloning\x1b[0m")
	add("::endgroup::")
	for range 5 {
		add("downloading")
	}
	add("10%\r50%\r100%")
	for i := range 40 {
		add(fmt.Sprintf("ok pkg%02d 0.01s", i))
	}
	add("FAIL TestSessionRefresh (0.02s)")
	add("    session_test.go:44: expected 200, got 500")
	add("exit status 1")
	return b.String()
}

// logServer serves the job and its log with Range support.
func logServer(t *testing.T, body string, status int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/actions/jobs/5678"):
			writeJSON(w, mustRaw(jobJSON))
		case strings.HasSuffix(r.URL.Path, "/actions/jobs/5678/logs"):
			if status != 0 {
				w.WriteHeader(status)
				fmt.Fprint(w, `{"message":"logs have expired"}`)
				return
			}
			if q := r.URL.Query().Get("q"); q != "" {
				for _, l := range strings.Split(strings.TrimRight(body, "\n"), "\n") {
					if strings.Contains(strings.ToLower(l), strings.ToLower(q)) {
						fmt.Fprintln(w, l)
					}
				}
				return
			}
			http.ServeContent(w, r, "job.log", time.Time{}, bytesReader(body))
		default:
			t.Errorf("unexpected %s", r.URL.RequestURI())
			http.NotFound(w, r)
		}
	}
}

func TestFetchJobLogTail(t *testing.T) {
	c, rl := newClient(t, 50, logServer(t, logBody(), 0))
	// small budget: the suffix range starts inside a line
	l, err := FetchJobLog(t.Context(), c, LogOpts{Owner: "o", Repo: "r", Job: 5678, Tail: 6, MaxChars: 300})
	if err != nil {
		t.Fatal(err)
	}
	testutil.Golden(t, "log_tail", l.Render())
	reqs := rl.all()
	if last := reqs[len(reqs)-1]; !strings.Contains(last, "step=2") {
		t.Fatalf("a failed job must select its failing step 2: %v", reqs)
	}
	if l.StepNumber != 2 || l.StepName != "Run tests" || l.NextOffset == 0 {
		t.Fatalf("%+v", l)
	}
}

func TestFetchJobLogRangeHeader(t *testing.T) {
	var rng string
	c, _ := newClient(t, 50, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/logs") {
			rng = r.Header.Get("Range")
			fmt.Fprint(w, "x\n")
			return
		}
		writeJSON(w, mustRaw(jobJSON))
	})
	if _, err := FetchJobLog(t.Context(), c, LogOpts{Owner: "o", Repo: "r", Job: 5678, MaxChars: 1000}); err != nil {
		t.Fatal(err)
	}
	if rng != "bytes=-2000" {
		t.Fatalf("range %q", rng)
	}
}

func TestFetchJobLogWholeSmall(t *testing.T) {
	c, _ := newClient(t, 50, logServer(t, logBody(), 0))
	l, err := FetchJobLog(t.Context(), c, LogOpts{Owner: "o", Repo: "r", Job: 5678, Step: "0", Tail: 500, MaxChars: 20000})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(l.Text, "[truncated") {
		t.Fatalf("whole log must not be truncated:\n%s", l.Text)
	}
	testutil.Golden(t, "log_whole", l.Render())
}

func TestFetchJobLogGrepFirstOffset(t *testing.T) {
	c, rl := newClient(t, 50, logServer(t, logBody(), 0))
	g, err := FetchJobLog(t.Context(), c, LogOpts{Owner: "o", Repo: "r", Job: 5678, Grep: "FAIL", IgnoreCase: true, MaxChars: 4000})
	if err != nil {
		t.Fatal(err)
	}
	testutil.Golden(t, "log_grep", g.Render())
	if q := rl.all()[len(rl.all())-1]; !strings.Contains(q, "q=FAIL") || !strings.Contains(q, "qi=true") {
		t.Fatalf("grep request %s", q)
	}

	f, err := FetchJobLog(t.Context(), c, LogOpts{Owner: "o", Repo: "r", Job: 5678, Step: "1", First: 4, MaxChars: 4000})
	if err != nil {
		t.Fatal(err)
	}
	testutil.Golden(t, "log_first", f.Render())

	total := int64(len(logBody()))
	off := total - 120
	o, err := FetchJobLog(t.Context(), c, LogOpts{Owner: "o", Repo: "r", Job: 5678, Step: "1", Offset: &off, MaxChars: 4000})
	if err != nil {
		t.Fatal(err)
	}
	if o.NextOffset != total || !strings.Contains(o.Render(), fmt.Sprintf("next_offset: %d", total)) {
		t.Fatalf("offset result %+v", o)
	}
	// nothing new: 416
	n, err := FetchJobLog(t.Context(), c, LogOpts{Owner: "o", Repo: "r", Job: 5678, Step: "1", Offset: &total, MaxChars: 4000})
	if err != nil {
		t.Fatal(err)
	}
	if n.Text != "no new output" || n.NextOffset != total {
		t.Fatalf("no new output: %+v", n)
	}
}

func TestFetchJobLogNoLog(t *testing.T) {
	c, _ := newClient(t, 50, logServer(t, "", 404))
	l, err := FetchJobLog(t.Context(), c, LogOpts{Owner: "o", Repo: "r", Job: 5678, MaxChars: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if l.NoLog != "no log: logs have expired" {
		t.Fatalf("NoLog %q", l.NoLog)
	}
}

func TestFetchJobLogByName(t *testing.T) {
	c, _ := newClient(t, 50, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/runs/9/jobs") {
			writeJSON(w, []any{mustRaw(`{"id":5677,"name":"lint","status":"success"}`), mustRaw(`{"id":5678,"name":"build","status":"failure"}`)})
			return
		}
		logServer(t, logBody(), 0)(w, r)
	})
	l, err := FetchJobLog(t.Context(), c, LogOpts{Owner: "o", Repo: "r", Run: 9, Name: "Bui", MaxChars: 2000})
	if err != nil {
		t.Fatal(err)
	}
	if l.Job.ID != 5678 {
		t.Fatalf("job %+v", l.Job)
	}
	if _, err := FetchJobLog(t.Context(), c, LogOpts{Owner: "o", Repo: "r", MaxChars: 2000}); err == nil {
		t.Fatal("want error without job")
	}
}

func TestSelectStep(t *testing.T) {
	job := &Job{Status: "success", Steps: []Step{{Number: 0, Status: "success"}, {Number: 1, Status: "success"}, {Number: 2, Status: "skipped"}}}
	if _, ok, _ := SelectStep(job, ""); ok {
		t.Fatal("successful job reads the whole log")
	}
	if s, ok, _ := SelectStep(job, "failed"); !ok || s.Number != 1 {
		t.Fatalf("last step that ran: %+v %v", s, ok)
	}
	if _, _, err := SelectStep(job, "x"); err == nil {
		t.Fatal("invalid step must fail")
	}
	job.Status = "failure"
	job.Steps[1].Status = "failure"
	if s, ok, _ := SelectStep(job, ""); !ok || s.Number != 1 {
		t.Fatalf("failed job selects its failing step: %+v", s)
	}
}

func TestFetchJobLogFollowLargeBody(t *testing.T) {
	var sb strings.Builder
	for i := 0; sb.Len() < 5<<20; i++ {
		fmt.Fprintf(&sb, "line %07d\n", i)
	}
	body := sb.String()
	c, _ := newClient(t, 50, logServer(t, body, 0))
	off := int64(0)
	next := 0
	for range 3 {
		l, err := FetchJobLog(t.Context(), c, LogOpts{Owner: "o", Repo: "r", Job: 5678, Step: "1", Offset: &off, MaxChars: 4000})
		if err != nil {
			t.Fatal(err)
		}
		if l.NextOffset <= off || l.NextOffset > off+8000 || l.NextOffset >= int64(len(body)) {
			t.Fatalf("offset %d -> next %d (total %d)", off, l.NextOffset, len(body))
		}
		// the head of the chunk is shown, and the chunk ends on a line boundary
		if !strings.Contains(l.Text, fmt.Sprintf("line %07d", next)) || !strings.Contains(l.Text, "[truncated:") {
			t.Fatalf("chunk at %d:\n%.200s", off, l.Text)
		}
		if body[l.NextOffset-1] != '\n' {
			t.Fatalf("chunk does not end on a line: %d", l.NextOffset)
		}
		next = int(l.NextOffset / 13)
		off = l.NextOffset
	}
}
