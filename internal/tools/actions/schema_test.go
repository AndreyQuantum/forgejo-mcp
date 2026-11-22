package actions

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/AndreyQuantum/forgejo-mcp/internal/testutil"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
)

func listRunTool(t *testing.T, readOnly bool) *mcp.Tool {
	t.Helper()
	env := testutil.NewEnv(t, newFake(t))
	env.Cfg.ReadOnly = readOnly
	reg := tools.NewRegistry()
	Register(reg)
	if err := reg.Build(env.Cfg); err != nil {
		t.Fatal(err)
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "t"}, nil)
	if err := reg.Install(srv, env); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "t"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range res.Tools {
		if tl.Name == "run" {
			return tl
		}
	}
	return nil
}

func TestSchemaWithinBudget(t *testing.T) {
	tl := listRunTool(t, false)
	if tl == nil {
		t.Fatal("run is not registered")
	}
	raw, err := json.Marshal(tl)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > tools.BudgetToolBytes {
		t.Errorf("run schema is %d bytes, budget %d", len(raw), tools.BudgetToolBytes)
	}
	t.Logf("run tools/list entry: %d bytes", len(raw))
	if n := utf8.RuneCountInString(tl.Description); n > tools.BudgetDescriptionChars {
		t.Errorf("description has %d chars", n)
	}
	if tl.Annotations == nil || tl.Annotations.ReadOnlyHint || tl.Annotations.DestructiveHint == nil || *tl.Annotations.DestructiveHint {
		t.Errorf("run has Write actions and no Destructive one: %+v", tl.Annotations)
	}
	s := string(raw)
	if !strings.Contains(s, "cancel") || !strings.Contains(s, "artifact_download") {
		t.Errorf("action enum incomplete: %s", s)
	}
}

func TestReadOnlyKeepsOnlyReadActions(t *testing.T) {
	tl := listRunTool(t, true)
	if tl == nil {
		t.Fatal("run must stay registered in read-only mode")
	}
	raw, _ := json.Marshal(tl)
	s := string(raw)
	for _, gone := range []string{`"cancel"`, `"rerun"`, `"rerun_job"`, `"delete"`, `"dispatch"`, `"artifact_delete"`} {
		if strings.Contains(s, gone) {
			t.Errorf("read-only schema still lists %s", gone)
		}
	}
	for _, kept := range []string{`"list"`, `"get"`, `"log"`, `"workflows"`, `"artifact_download"`} {
		if !strings.Contains(s, kept) {
			t.Errorf("read-only schema lost %s", kept)
		}
	}
	if !tl.Annotations.ReadOnlyHint {
		t.Errorf("a tool with only Read actions is ReadOnlyHint")
	}
}

func TestStepArgDecoding(t *testing.T) {
	for in, want := range map[string]string{`"failed"`: "failed", `" 3 "`: "3", `2`: "2", `null`: ""} {
		var s stepArg
		if err := json.Unmarshal([]byte(in), &s); err != nil || string(s) != want {
			t.Errorf("%s -> %q, %v", in, s, err)
		}
	}
	var s stepArg
	if err := json.Unmarshal([]byte(`true`), &s); err == nil {
		t.Errorf("a bool must be rejected")
	}
	s2 := newFake(t)
	got := runErr(t, s2, map[string]any{"action": "log", "job": 1, "step": true})
	if !strings.HasPrefix(got, "error: invalid: param step: expected") {
		t.Errorf("got %q", got)
	}
}
