package tools_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/AndreyQuantum/forgejo-mcp/internal/config"
	"github.com/AndreyQuantum/forgejo-mcp/internal/testutil"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools/all"
)

// stubParam is one property of a stub params struct.
type stubParam struct {
	name string
	typ  reflect.Type
	desc string
}

var (
	tStr   = reflect.TypeOf("")
	tInt   = reflect.TypeOf(0)
	tBool  = reflect.TypeOf(false)
	tPBool = reflect.TypeOf((*bool)(nil))
	tStrs  = reflect.TypeOf([]string{})
	tMap   = reflect.TypeOf(map[string]any{})
)

// stubVocabulary mimics the widest real params structs (pr, issue, repo, run):
// about 45 properties, descriptions of up to 12 words.
var stubVocabulary = []stubParam{
	{"action", tStr, ""},
	{"repo", tStr, "owner/name, default detected repo"},
	{"n", tInt, "issue or PR number"},
	{"id", tInt, "numeric id of the object"},
	{"name", tStr, "branch, tag, label or repo name"},
	{"ref", tStr, "branch, tag or commit sha"},
	{"sha", tStr, "commit sha"},
	{"path", tStr, "file path inside the repo"},
	{"q", tStr, "search text"},
	{"state", tStr, "open, closed or all"},
	{"page", tInt, ""},
	{"limit", tInt, ""},
	{"body", tStr, "text body in markdown"},
	{"title", tStr, "short title"},
	{"labels", tStrs, "label names"},
	{"assignees", tStrs, "user logins"},
	{"milestone", tStr, "milestone title or id"},
	{"full", tBool, "lift per-field caps"},
	{"verbose", tBool, "add second-tier fields"},
	{"body_max", tInt, "max body chars, 0 = all"},
	{"confirm", tStr, "exact target name for destructive actions"},
	{"scope", tStr, "repo, org, user or admin level"},
	{"timeout", tInt, "wait seconds"},
	{"max_chars", tInt, "output size in chars"},
	{"include", tStrs, "extra sections to add"},
	{"first", tInt, "head lines of a log"},
	{"all", tBool, "all pages or all items"},
	{"head", tStr, "head branch, owner:branch for forks"},
	{"base", tStr, "target branch"},
	{"style", tStr, "merge style: merge, rebase, squash"},
	{"draft", tPBool, "create as draft"},
	{"when_green", tBool, "merge when checks pass"},
	{"delete_branch", tPBool, "delete head branch after merge"},
	{"event", tStr, "approve, comment or request_changes"},
	{"reviewers", tStrs, "user logins to request"},
	{"set", tMap, "fields to change"},
	{"job", tInt, "job id"},
	{"step", tInt, "step number"},
	{"grep", tStr, "regex filter for log lines"},
	{"tail", tInt, "last N lines"},
	{"inputs", tMap, "workflow dispatch inputs"},
	{"workflow", tStr, "workflow file name"},
	{"sort", tStr, "sort key"},
	{"order", tStr, "asc or desc"},
	{"which", tStr, "list selector"},
}

// stubSpec builds a multi-action tool over the vocabulary with deterministic
// per-action param subsets of 8+extra params.
func stubSpec(name string, actions []string, extra int) tools.Spec {
	fields := make([]reflect.StructField, 0, len(stubVocabulary))
	for _, p := range stubVocabulary {
		tag := fmt.Sprintf(`json:"%s,omitempty"`, p.name)
		if p.desc != "" {
			tag += fmt.Sprintf(` jsonschema:"%s"`, p.desc)
		}
		fields = append(fields, reflect.StructField{
			Name: "F" + strings.ToUpper(p.name[:1]) + p.name[1:], Type: p.typ, Tag: reflect.StructTag(tag),
		})
	}
	typ := reflect.StructOf(fields)
	spec := tools.Spec{
		Name: name, Toolset: "pulls", Description: "Stub tool of about one hundred and twenty characters to mimic a real description text here.",
		Params: reflect.New(typ).Elem().Interface(), RepoScoped: true,
	}
	for i, an := range actions {
		var params []string
		for k := 0; k < 8+extra; k++ {
			params = append(params, stubVocabulary[2+(i*7+k*3)%(len(stubVocabulary)-2)].name)
		}
		spec.Actions = append(spec.Actions, tools.Action{
			Name: an, Mode: tools.Read, Params: params, Required: params[:1], Summary: "one line summary of the action",
			Run: func(*tools.Call, any) (*tools.Result, error) { return nil, nil },
		})
	}
	return spec
}

func toolJSONSize(t testing.TB, s tools.Spec) int {
	t.Helper()
	r := tools.NewRegistry()
	r.Add(s)
	if err := r.Build(config.Defaults()); err != nil {
		t.Fatal(err)
	}
	list := toolList(t, r, testutil.NewEnv(t, nil))
	raw, err := json.Marshal(list[0])
	if err != nil {
		t.Fatal(err)
	}
	return len(raw)
}

// TestMeasureStubSchemas is the measurement behind budget.go: it prints the
// size of the widest realistic schemas and checks they fit the frozen budget.
func TestMeasureStubSchemas(t *testing.T) {
	specs := map[string][]string{
		"pr":    {"list", "get", "create", "edit", "merge", "update", "files", "diff", "commits", "comments", "status"},
		"issue": {"list", "search", "get", "create", "edit", "delete", "lock", "unlock", "pin", "unpin", "link", "unlink", "react", "unreact", "subscribe", "unsubscribe", "deps", "time"},
		"repo":  {"get", "list", "search", "create", "edit", "delete", "fork", "transfer", "migrate", "topics", "mirror_list", "mirror_add", "mirror_sync", "mirror_delete", "star", "unstar", "watch", "unwatch", "template", "archive"},
		"run":   {"list", "get", "job", "log", "cancel", "rerun", "delete", "dispatch", "workflows", "artifacts", "artifact_get", "approve"},
	}
	widest := 0
	for name, acts := range specs {
		n := toolJSONSize(t, stubSpec(name, acts, 0))
		n4 := toolJSONSize(t, stubSpec(name, acts, 4))
		t.Logf("stub %-6s with 12 params/action: %5d bytes", name, n4)
		t.Logf("stub %-6s %2d actions: %5d bytes", name, len(acts), n)
		widest = max(widest, n)
	}
	if widest > tools.BudgetToolBytes {
		t.Errorf("widest stub schema %d bytes exceeds BudgetToolBytes %d", widest, tools.BudgetToolBytes)
	}
}

// TestSchemaBudget enforces the frozen budget on every registered non-admin
// tool (passes vacuously until the area packages register their specs).
func TestSchemaBudget(t *testing.T) {
	r := tools.NewRegistry()
	all.RegisterAll(r)
	if err := r.Build(config.Defaults()); err != nil {
		t.Fatal(err)
	}
	list := toolList(t, r, testutil.NewEnv(t, nil))
	total := 0
	for _, tl := range list {
		raw, err := json.Marshal(tl)
		if err != nil {
			t.Fatal(err)
		}
		total += len(raw)
		if len(raw) > tools.BudgetToolBytes {
			t.Errorf("tool %s: %d bytes > %d", tl.Name, len(raw), tools.BudgetToolBytes)
		}
		if n := utf8.RuneCountInString(tl.Description); n > tools.BudgetDescriptionChars {
			t.Errorf("tool %s: description %d chars > %d", tl.Name, n, tools.BudgetDescriptionChars)
		}
	}
	if total > tools.BudgetTotalBytes {
		t.Errorf("tools/list is %d bytes > %d", total, tools.BudgetTotalBytes)
	}
	t.Logf("%d tools, %d bytes", len(list), total)
}
