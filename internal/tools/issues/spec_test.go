package issues

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/AndreyQuantum/forgejo-mcp/internal/config"
	"github.com/AndreyQuantum/forgejo-mcp/internal/endpoints"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
)

func newReg(t *testing.T) *tools.Registry {
	t.Helper()
	r := tools.NewRegistry()
	Register(r)
	if err := r.Build(nil); err != nil {
		t.Fatalf("build: %v", err)
	}
	return r
}

func TestSpecsRegisterFiveTools(t *testing.T) {
	var names []string
	for _, s := range newReg(t).Enabled() {
		names = append(names, s.Name)
		if s.Toolset != "issues" || !s.RepoScoped || len(s.Description) > 160 {
			t.Errorf("%s: bad spec (toolset %q, repo-scoped %v, %d chars)", s.Name, s.Toolset, s.RepoScoped, len(s.Description))
		}
	}
	eq(t, "tools", strings.Join(names, ","), "issue,comment,label,milestone,attach")
}

func TestSchemasWithinBudget(t *testing.T) {
	for _, s := range newReg(t).Enabled() {
		sc, err := tools.BuildInputSchema(s, s.Actions)
		if err != nil {
			t.Fatalf("%s: %v", s.Name, err)
		}
		raw, _ := json.Marshal(sc)
		t.Logf("%s schema: %d bytes, %d actions", s.Name, len(raw), len(s.Actions))
		if len(raw) > tools.BudgetToolBytes {
			t.Errorf("%s schema is %d bytes (budget %d)", s.Name, len(raw), tools.BudgetToolBytes)
		}
	}
}

func TestEveryEndpointIsInTheIndex(t *testing.T) {
	for _, s := range newReg(t).Specs() {
		for _, a := range s.Actions {
			if len(a.Endpoints) == 0 {
				t.Errorf("%s.%s has no Endpoints", s.Name, a.Name)
			}
			for _, ep := range a.Endpoints {
				method, path, _ := strings.Cut(ep, " ")
				if endpoints.Lookup(method, path) == nil {
					t.Errorf("%s.%s: endpoint %q is not in the index", s.Name, a.Name, ep)
				}
			}
		}
	}
}

func TestParamsListedForActionsExistInSchema(t *testing.T) {
	for _, s := range newReg(t).Specs() {
		sc, err := tools.BuildInputSchema(s, s.Actions)
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range s.Actions {
			for _, p := range append(append([]string{}, a.Params...), a.Required...) {
				if _, ok := sc.Properties[p]; !ok {
					t.Errorf("%s.%s: param %q is not in the schema", s.Name, a.Name, p)
				}
			}
		}
	}
}

func TestHelpListsEveryAction(t *testing.T) {
	f := newFake(t, nil)
	got := mustRun(t, f, "issue", map[string]any{"action": "help"})
	for _, a := range spec(t, "issue").Actions {
		if !strings.Contains(got, a.Name+" [") {
			t.Errorf("help lacks action %s", a.Name)
		}
	}
	contains(t, got, "delete [D]")
}

func TestReadOnlyKeepsOnlyReadActions(t *testing.T) {
	r := tools.NewRegistry()
	Register(r)
	cfg := config.Defaults()
	cfg.ReadOnly = true
	if err := r.Build(cfg); err != nil {
		t.Fatal(err)
	}
	if len(r.Enabled()) == 0 {
		t.Fatal("read actions must stay registered")
	}
	for _, s := range r.Enabled() {
		for _, a := range s.Actions {
			if a.Mode != tools.Read {
				t.Errorf("%s.%s survives read-only mode", s.Name, a.Name)
			}
		}
	}
}
