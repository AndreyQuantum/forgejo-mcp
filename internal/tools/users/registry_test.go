package users

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/AndreyQuantum/forgejo-mcp/internal/config"
	"github.com/AndreyQuantum/forgejo-mcp/internal/endpoints"
	"github.com/AndreyQuantum/forgejo-mcp/internal/testutil"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
)

func registry(t testing.TB, mutate func(*config.Config)) *tools.Registry {
	t.Helper()
	cfg := config.Defaults()
	if mutate != nil {
		mutate(cfg)
	}
	r := tools.NewRegistry()
	Register(r)
	if err := r.Build(cfg); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestEndpointsInIndex(t *testing.T) {
	n := 0
	for _, s := range registry(t, nil).Specs() {
		for _, a := range s.Actions {
			if len(a.Endpoints) == 0 {
				t.Errorf("%s.%s has no Endpoints", s.Name, a.Name)
			}
			for _, ep := range a.Endpoints {
				n++
				method, path, _ := strings.Cut(ep, " ")
				if endpoints.Lookup(method, path) == nil {
					t.Errorf("%s.%s: endpoint %q is not in the index", s.Name, a.Name, ep)
				}
			}
		}
	}
	if n == 0 {
		t.Fatal("no endpoints registered")
	}
}

func TestSpecShape(t *testing.T) {
	want := map[string]int{"user": 13, "org": 19, "notify": 4}
	r := registry(t, nil)
	if len(r.Specs()) != len(want) {
		t.Fatalf("%d tools, want %d", len(r.Specs()), len(want))
	}
	for _, s := range r.Specs() {
		if s.Toolset != "users" || s.RepoScoped || s.Single {
			t.Errorf("%s: toolset=%s repoScoped=%v single=%v", s.Name, s.Toolset, s.RepoScoped, s.Single)
		}
		if len(s.Actions) != want[s.Name] {
			t.Errorf("%s has %d actions, want %d", s.Name, len(s.Actions), want[s.Name])
		}
		if n := len([]rune(s.Description)); n > tools.BudgetDescriptionChars {
			t.Errorf("%s description has %d chars", s.Name, n)
		}
		sc, err := tools.BuildInputSchema(s, s.Actions)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(sc)
		if len(b)+len(s.Description) > tools.BudgetToolBytes {
			t.Errorf("%s schema is %d bytes (budget %d)", s.Name, len(b), tools.BudgetToolBytes)
		}
		// every Params/Required name must be a real property
		for _, a := range s.Actions {
			for _, p := range append(append([]string{}, a.Params...), a.Required...) {
				if _, ok := sc.Properties[p]; !ok && p != "repo" {
					t.Errorf("%s.%s names unknown param %q", s.Name, a.Name, p)
				}
			}
		}
	}
}

func TestReadOnlyHidesMutations(t *testing.T) {
	r := registry(t, func(c *config.Config) { c.ReadOnly = true })
	for _, s := range r.Enabled() {
		for _, a := range s.Actions {
			if a.Mode != tools.Read {
				t.Errorf("%s.%s survives read-only mode", s.Name, a.Name)
			}
		}
	}
	env := testutil.NewEnv(t, newFake(nil), testutil.WithConfig(func(c *config.Config) { c.ReadOnly = true }))
	out, isErr := testutil.CallTool(t, env, spec(t, "org"), map[string]any{"action": "create", "name": "x"})
	if !isErr || !strings.Contains(out, "disabled (read-only mode)") {
		t.Errorf("create in read-only mode: err=%v %q", isErr, out)
	}
}

func TestDestructiveAnnotation(t *testing.T) {
	r := registry(t, nil)
	for _, s := range r.Enabled() {
		var destructive bool
		for _, a := range s.Actions {
			if a.Mode == tools.Destructive {
				destructive = true
			}
		}
		if destructive != (s.Name == "org") {
			t.Errorf("%s destructive = %v", s.Name, destructive)
		}
	}
}

func TestHelpGolden(t *testing.T) {
	for _, name := range []string{"user", "org", "notify"} {
		t.Run(name, func(t *testing.T) {
			out, isErr := run(t, newFake(nil), name, map[string]any{"action": "help"})
			if isErr {
				t.Fatal(out)
			}
			testutil.Golden(t, "help_"+name, out)
		})
	}
	out, _ := run(t, newFake(nil), "org", map[string]any{"help": "team_edit"})
	testutil.Golden(t, "help_org_team_edit", out)
}
