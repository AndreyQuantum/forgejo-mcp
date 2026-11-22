package actions

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/AndreyQuantum/forgejo-mcp/internal/config"
	"github.com/AndreyQuantum/forgejo-mcp/internal/testutil"
)

const dispatchPath = "POST /repos/o/r/actions/workflows/manual.yml/dispatches"

func TestDispatchBodyAndOutput(t *testing.T) {
	s := newFake(t)
	s.reply(dispatchPath, 201, `{"id":1235,"run_number":57,"jobs":["test","build"]}`)
	text := runOK(t, s, map[string]any{
		"action": "dispatch", "workflow": "manual.yml", "ref": "refs/heads/dev",
		"inputs": map[string]any{"env": "prod", "debug": true, "count": 3, "ratio": 1.5, "none": nil},
	})
	if text != "ok: dispatched manual.yml on dev: run 1235 no.57 jobs: build,test" {
		t.Fatalf("got %q", text)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(s.only(dispatchPath).Body), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"ref":             "dev",
		"return_run_info": true,
		"inputs":          map[string]any{"env": "prod", "debug": "true", "count": "3", "ratio": "1.5", "none": ""},
	}
	gj, _ := json.Marshal(got)
	wj, _ := json.Marshal(want)
	if string(gj) != string(wj) {
		t.Fatalf("body\n got  %s\n want %s", gj, wj)
	}
	// no repo meta lookup when ref is given
	if n := len(s.seen("GET /repos/o/r")); n != 0 {
		t.Errorf("repo meta read although ref was given")
	}
}

func TestDispatchDefaultRefAndNoInputs(t *testing.T) {
	s := newFake(t)
	s.reply("POST /repos/o/r/actions/workflows/ci.yml/dispatches", 201, `{"id":9,"run_number":3,"jobs":[]}`)
	text := runOK(t, s, map[string]any{"action": "dispatch", "workflow": "ci.yml"})
	if text != "ok: dispatched ci.yml on main: run 9 no.3" {
		t.Fatalf("got %q", text)
	}
	body := s.only("POST /repos/o/r/actions/workflows/ci.yml/dispatches").Body
	if body != `{"ref":"main","return_run_info":true}` {
		t.Fatalf("body %s", body)
	}
}

func TestDispatchServerErrorVerbatim(t *testing.T) {
	s := newFake(t)
	s.reply(dispatchPath, 400, `{"message":"input required for 'Target environment'"}`)
	text := runErr(t, s, map[string]any{"action": "dispatch", "workflow": "manual.yml", "ref": "main"})
	if !strings.Contains(text, "400") || !strings.Contains(text, "input required for 'Target environment'") {
		t.Fatalf("got %q", text)
	}
	s2 := newFake(t)
	s2.reply(dispatchPath, 404, `{"message":"workflow not found"}`)
	text = runErr(t, s2, map[string]any{"action": "dispatch", "workflow": "manual.yml", "ref": "main"})
	if !strings.Contains(text, "workflow not found") {
		t.Fatalf("got %q", text)
	}
}

func TestDispatch204LocatesNewRun(t *testing.T) {
	s := newFake(t)
	s.reply("GET /version", 200, `{"version":"15.0.1"}`)
	posted := false
	s.on("GET /repos/o/r/actions/runs", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		old := runJSON(100, 50, "success", "manual.yml", "old", "workflow_dispatch", "main", sha1, "alice", "2026-11-22T09:00:00Z", 1e9)
		if !posted {
			_, _ = w.Write([]byte(runsBody(1, old)))
			return
		}
		fresh := runJSON(101, 51, "waiting", "manual.yml", "new", "workflow_dispatch", "main", sha1, "alice", "2026-11-22T10:00:00Z", 0)
		_, _ = w.Write([]byte(runsBody(2, fresh, old)))
	})
	s.on(dispatchPath, func(w http.ResponseWriter, _ *http.Request) {
		posted = true
		w.WriteHeader(204)
	})
	text := runOK(t, s, map[string]any{"action": "dispatch", "workflow": "manual.yml", "ref": "main"})
	if text != "ok: dispatched manual.yml on main: run 101 no.51" {
		t.Fatalf("got %q", text)
	}
	reqs := s.seen("GET /repos/o/r/actions/runs")
	if len(reqs) != 2 {
		t.Fatalf("want snapshot + one poll, got %d", len(reqs))
	}
	wantQuery(t, reqs[1], map[string][]string{"workflow_id": {"manual.yml"}, "event": {"workflow_dispatch"}, "limit": {"3"}})
}

func TestDispatch204RunNeverAppears(t *testing.T) {
	s := newFake(t)
	s.reply("GET /version", 200, `{"version":"15.0.1"}`)
	s.reply("GET /repos/o/r/actions/runs", 200, runsBody(0))
	s.reply(dispatchPath, 204, "")
	env := testutil.NewEnv(t, s, testutil.WithRepo("o/r"))
	// the fake clock sleeps only when advanced: drive it from a goroutine
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fc := env.Clock.(*testutil.FakeClock)
		for i := 0; i < locateAttempts-1; i++ {
			if !fc.BlockUntil(ctx, 1) {
				return
			}
			fc.Advance(time.Second)
		}
	}()
	text, isErr := testutil.CallTool(t, env, runSpec(), map[string]any{"action": "dispatch", "workflow": "manual.yml", "ref": "main", "wait": true})
	<-done
	if isErr || !strings.HasPrefix(text, "ok: dispatched manual.yml on main; run id not available yet") || !strings.Contains(text, "not waiting") {
		t.Fatalf("got %q err=%v", text, isErr)
	}
}

func TestDispatchWait(t *testing.T) {
	s := newFake(t)
	s.reply(dispatchPath, 201, `{"id":1235,"run_number":57,"jobs":["build"]}`)
	s.reply("GET /repos/o/r/actions/runs/1235", 200,
		runJSON(1235, 57, "success", "manual.yml", "t", "workflow_dispatch", "main", sha1, "alice", "2026-11-22T10:00:00Z", 12e9))
	s.reply("GET /repos/o/r/actions/runs/1235/jobs", 200,
		`[{"id":7001,"run_id":1235,"name":"build","status":"success","attempt":1,"task_id":5}]`)
	text := runOK(t, s, map[string]any{"action": "dispatch", "workflow": "manual.yml", "ref": "main", "wait": true, "timeout": 20})
	testutil.Golden(t, "dispatch_wait", text)
}

func TestDispatchWaitTimeoutClamped(t *testing.T) {
	s := newFake(t)
	s.reply(dispatchPath, 201, `{"id":1235,"run_number":57,"jobs":["build"]}`)
	s.reply("GET /repos/o/r/actions/runs/1235", 200,
		runJSON(1235, 57, "success", "manual.yml", "t", "workflow_dispatch", "main", sha1, "alice", "2026-11-22T10:00:00Z", 12e9))
	s.reply("GET /repos/o/r/actions/runs/1235/jobs", 200, `[]`)
	text := runOK(t, s, map[string]any{"action": "dispatch", "workflow": "manual.yml", "ref": "main", "wait": true, "timeout": 100000},
		testutil.WithConfig(func(c *config.Config) { c.WaitMax = 30e9 }))
	if !strings.Contains(text, "verdict: success") {
		t.Fatalf("got %q", text)
	}
}

func TestStringifyInputsNilAndEmpty(t *testing.T) {
	if got := stringifyInputs(nil); got != nil {
		t.Fatalf("got %v", got)
	}
	got := stringifyInputs(map[string]any{"list": []any{"a", 1.0}})
	if got["list"] != `["a",1]` {
		t.Fatalf("got %v", got)
	}
}

func TestDispatch204LocateErrorIsNotAToolError(t *testing.T) {
	s := newFake(t)
	s.reply("GET /version", 200, `{"version":"15.0.1"}`)
	s.reply("GET /repos/o/r/actions/runs", 404, `{"message":"runs not available"}`)
	s.reply(dispatchPath, 204, "")
	text := runOK(t, s, map[string]any{"action": "dispatch", "workflow": "manual.yml", "ref": "main", "wait": true})
	if !strings.HasPrefix(text, "ok: dispatched manual.yml on main; run id not available yet") ||
		!strings.Contains(text, "note: could not look up the run") {
		t.Fatalf("got %q", text)
	}
}
