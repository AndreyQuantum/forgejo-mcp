package ci

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AndreyQuantum/forgejo-mcp/internal/clock"
	"github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"
	"github.com/AndreyQuantum/forgejo-mcp/internal/testutil"
)

// reqLog records the requests a fake server saw.
type reqLog struct {
	mu   sync.Mutex
	list []string
}

func (r *reqLog) add(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.list = append(r.list, s)
}

func (r *reqLog) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.list...)
}

// newClient starts a fake server; /settings/api answers max items = maxItems.
func newClient(t *testing.T, maxItems int, h http.HandlerFunc) (*forgejo.Client, *reqLog) {
	t.Helper()
	rl := &reqLog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/settings/api" {
			fmt.Fprintf(w, `{"default_paging_num":20,"max_response_items":%d}`, maxItems)
			return
		}
		rl.add(r.Method + " " + r.URL.RequestURI())
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	c := forgejo.New(forgejo.Config{BaseURL: srv.URL, Token: "tok", HTTPClient: srv.Client(), Clock: clock.Real(), Timeout: 5 * time.Second})
	return c, rl
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func mustRaw(s string) any {
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		panic(err)
	}
	return v
}

func bytesReader(s string) *bytes.Reader { return bytes.NewReader([]byte(s)) }

func TestRenderGolden(t *testing.T) {
	var b strings.Builder
	runs := []string{
		`{"id":1234,"index_in_repo":56,"workflow_id":"ci.yml","status":"failure","event":"push","prettyref":"main","commit_sha":"8a1b2c3d4e5f60718293","trigger_user":{"login":"alice","id":3},"duration":63000000000,"created":"2026-11-22T09:50:12Z"}`,
		`{"id":1240,"index_in_repo":12,"workflow_id":"lint.yml","status":"waiting","event":"pull_request","prettyref":"#42","trigger_user":{"login":"bob"},"need_approval":true,"created":"2026-11-22T09:51:00Z"}`,
		`{"id":7,"status":"running"}`,
	}
	for _, r := range runs {
		var run Run
		if err := json.Unmarshal([]byte(r), &run); err != nil {
			t.Fatal(err)
		}
		b.WriteString(RunLine(run) + "\n")
	}
	b.WriteString(JobLine(Job{ID: 5678, Name: "build", Status: "failure", Attempt: 1, Needs: []string{"lint"}, RunsOn: []string{"docker"}}) + "\n")
	b.WriteString(JobLine(Job{ID: 5679, Name: "lint", Status: "waiting"}) + "\n")
	b.WriteString(StepLine(Step{Number: 3, Name: "Run tests", Status: "failure", Started: "2026-11-22T09:50:00Z", Stopped: "2026-11-22T09:50:12Z"}) + "\n")
	b.WriteString(StepLine(Step{Number: 4, Name: "Post", Status: "waiting"}) + "\n")
	b.WriteString(JobSummary([]Job{{Name: "build", Status: "failure"}, {Name: "lint", Status: "success"}, {Name: "test", Status: "skipped"}, {Name: "e2e", Status: "running"}}) + "\n")
	testutil.Golden(t, "render", b.String())
}

func TestCombinedStatusPages(t *testing.T) {
	// max 2 per page: pages of 2, 2, 1 statuses; the server's own `state` is
	// per page and must be ignored.
	pages := map[string][]forgejo.Status{
		"1": {{ID: 1, Status: "success", Context: "a"}, {ID: 2, Status: "success", Context: "b"}},
		"2": {{ID: 3, Status: "failure", Context: "c", Description: "Failing after 12s", TargetURL: "http://x/c"}, {ID: 4, Status: "pending", Context: "d"}},
		"3": {{ID: 5, Status: "warning", Context: "e"}},
	}
	c, rl := newClient(t, 2, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/api/v1/repos/o/r/commits/feat%2Fx/status" {
			t.Errorf("path %s", r.URL.EscapedPath())
		}
		writeJSON(w, map[string]any{"state": "success", "statuses": pages[r.URL.Query().Get("page")]})
	})
	got, err := CombinedStatus(t.Context(), c, "o", "r", "feat/x")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "failure" || got.Total != 5 || got.OK != 3 {
		t.Fatalf("got %+v", got)
	}
	if len(got.Failing) != 1 || got.Failing[0].Context != "c" || got.Failing[0].TargetURL != "http://x/c" {
		t.Fatalf("failing %+v", got.Failing)
	}
	if len(got.Pending) != 1 || got.Pending[0] != "d" {
		t.Fatalf("pending %+v", got.Pending)
	}
	reqs := rl.all()
	if len(reqs) != 3 || !strings.Contains(reqs[0], "limit=2") || !strings.Contains(reqs[2], "page=3") {
		t.Fatalf("requests %v", reqs)
	}
}

func TestCombinedStatusStates(t *testing.T) {
	for _, tc := range []struct {
		name     string
		statuses []forgejo.Status
		want     string
	}{
		{"none", nil, "none"},
		{"success", []forgejo.Status{{ID: 1, Status: "success", Context: "a"}, {ID: 2, Status: "skipped", Context: "b"}}, "success"},
		{"warning", []forgejo.Status{{ID: 1, Status: "success", Context: "a"}, {ID: 2, Status: "warning", Context: "b"}}, "warning"},
		{"pending", []forgejo.Status{{ID: 1, Status: "warning", Context: "a"}, {ID: 2, Status: "pending", Context: "b"}}, "pending"},
		{"error", []forgejo.Status{{ID: 1, Status: "pending", Context: "a"}, {ID: 2, Status: "error", Context: "b"}}, "error"},
		{"failure", []forgejo.Status{{ID: 1, Status: "error", Context: "a"}, {ID: 2, Status: "failure", Context: "b"}}, "failure"},
		{"latest per context", []forgejo.Status{{ID: 1, Status: "failure", Context: "a"}, {ID: 2, Status: "success", Context: "a"}}, "success"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newClient(t, 50, func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, map[string]any{"statuses": tc.statuses})
			})
			got, err := CombinedStatus(t.Context(), c, "o", "r", "main")
			if err != nil {
				t.Fatal(err)
			}
			if got.State != tc.want {
				t.Fatalf("state %q want %q", got.State, tc.want)
			}
		})
	}
}

func TestEmptyCombinedStatus(t *testing.T) {
	c, _ := newClient(t, 50, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{}`)) })
	got, err := CombinedStatus(t.Context(), c, "o", "r", "main")
	if err != nil || got.State != "none" || got.Total != 0 {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestNewestPerGroup(t *testing.T) {
	runs := []Run{
		{ID: 9, WorkflowID: "ci.yml", Event: "push"},
		{ID: 7, WorkflowID: "ci.yml", Event: "push"},
		{ID: 8, WorkflowID: "ci.yml", Event: "pull_request"},
		{ID: 5, WorkflowID: "lint.yml", Event: "push"},
	}
	var ids []int64
	for _, r := range NewestPerGroup(runs) {
		ids = append(ids, r.ID)
	}
	if fmt.Sprint(ids) != "[5 8 9]" {
		t.Fatalf("ids %v", ids)
	}
}

func TestPickJob(t *testing.T) {
	jobs := []Job{{ID: 1, Name: "build (linux)"}, {ID: 2, Name: "build (mac)"}, {ID: 3, Name: "Lint"}}
	if j, err := PickJob(jobs, "lint"); err != nil || j.ID != 3 {
		t.Fatalf("ci match %v %v", j, err)
	}
	if j, err := PickJob(jobs, "build (m"); err != nil || j.ID != 2 {
		t.Fatalf("prefix %v %v", j, err)
	}
	if _, err := PickJob(jobs, "build"); err == nil || !strings.Contains(err.Error(), "ambiguous") || !strings.Contains(err.Error(), "1 build (linux)") {
		t.Fatalf("ambiguous: %v", err)
	}
	if _, err := PickJob(jobs, "zzz"); err == nil || !strings.Contains(err.Error(), "jobs: build (linux)") {
		t.Fatalf("missing: %v", err)
	}
}
