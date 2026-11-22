package repos

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/AndreyQuantum/forgejo-mcp/internal/endpoints"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
)

func allSpecs(t *testing.T) []tools.Spec {
	t.Helper()
	r := tools.NewRegistry()
	Register(r)
	specs := r.Specs()
	if len(specs) != 4 {
		t.Fatalf("expected 4 tools (repo, branch, tag, wiki), got %d", len(specs))
	}
	return specs
}

func TestEndpointsExist(t *testing.T) {
	for _, s := range allSpecs(t) {
		for _, a := range s.Actions {
			if len(a.Endpoints) == 0 {
				t.Errorf("%s.%s has no Endpoints", s.Name, a.Name)
			}
			for _, ep := range a.Endpoints {
				m, p, ok := strings.Cut(ep, " ")
				if !ok || endpoints.Lookup(m, p) == nil {
					t.Errorf("%s.%s: endpoint %q is not in the index", s.Name, a.Name, ep)
				}
			}
		}
	}
}

func TestSpecsAreWellFormed(t *testing.T) {
	for _, s := range allSpecs(t) {
		if s.Toolset != "repos" || !s.RepoScoped {
			t.Errorf("%s: toolset %q repoScoped %v", s.Name, s.Toolset, s.RepoScoped)
		}
		if n := utf8.RuneCountInString(s.Description); n > tools.BudgetDescriptionChars {
			t.Errorf("%s: description has %d chars", s.Name, n)
		}
		props := schemaProps(t, s)
		for _, a := range s.Actions {
			if a.Summary == "" {
				t.Errorf("%s.%s: no summary", s.Name, a.Name)
			}
			for _, n := range append(slices.Clone(a.Params), a.Required...) {
				if n != "repo" && props[n] == nil {
					t.Errorf("%s.%s: param %q is not a property", s.Name, a.Name, n)
				}
			}
		}
		// every property must be read by some action, or it is dead weight in the schema
		used := map[string]bool{"action": true, "repo": true, "help": true}
		for _, a := range s.Actions {
			for _, n := range append(slices.Clone(a.Params), a.Required...) {
				used[n] = true
			}
		}
		for n := range props {
			if !used[n] {
				t.Errorf("%s: property %q is read by no action", s.Name, n)
			}
		}
	}
}

func schemaProps(t *testing.T, s tools.Spec) map[string]any {
	t.Helper()
	sc, err := tools.BuildInputSchema(s, s.Actions)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(sc)
	var m struct {
		Properties map[string]any `json:"properties"`
	}
	_ = json.Unmarshal(raw, &m)
	return m.Properties
}

func TestSchemaWithinBudget(t *testing.T) {
	total := 0
	for _, s := range allSpecs(t) {
		sc, err := tools.BuildInputSchema(s, s.Actions)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(sc)
		n := len(raw) + len(s.Name) + len(s.Description)
		total += n
		t.Logf("%s: %d bytes, %d actions", s.Name, n, len(s.Actions))
		if n > tools.BudgetToolBytes {
			t.Errorf("%s schema is %d bytes (budget %d)", s.Name, n, tools.BudgetToolBytes)
		}
	}
	t.Logf("repos area total: %d bytes", total)
}

func TestModesAndConfirm(t *testing.T) {
	for _, s := range allSpecs(t) {
		for _, a := range s.Actions {
			wantD := s.Name == "repo" && a.Name == "delete"
			if (a.Mode == tools.Destructive) != wantD {
				t.Errorf("%s.%s: mode %v", s.Name, a.Name, a.Mode)
			}
			isRead := strings.HasSuffix(a.Name, "list") || slices.Contains([]string{"get", "search", "forks", "topics", "languages", "fork_status", "history", "protect_get"}, a.Name)
			if isRead != (a.Mode == tools.Read) {
				t.Errorf("%s.%s: mode %v, read-looking %v", s.Name, a.Name, a.Mode, isRead)
			}
		}
	}
}

func TestReadOnlyModeHidesWrites(t *testing.T) {
	r := tools.NewRegistry()
	Register(r)
	cfgRO := newConfig(true)
	if err := r.Build(cfgRO); err != nil {
		t.Fatal(err)
	}
	for _, s := range r.Enabled() {
		for _, a := range s.Actions {
			if a.Mode != tools.Read {
				t.Errorf("read-only mode kept %s.%s", s.Name, a.Name)
			}
		}
	}
}
