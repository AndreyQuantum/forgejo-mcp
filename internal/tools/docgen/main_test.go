package main

import (
	"strings"
	"testing"

	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
)

func TestRenderDeterministic(t *testing.T) {
	specs := []tools.Spec{{
		Name: "demo", Toolset: "repos", Description: "Demo | tool.", RepoScoped: true,
		Actions: []tools.Action{
			{Name: "get", Mode: tools.Read, Summary: "Get one", Params: []string{"n", "full"}, Required: []string{"n"}},
			{Name: "delete", Mode: tools.Destructive, Summary: "Remove", Params: []string{"confirm"}},
		},
	}}
	a, b := Render(specs), Render(specs)
	if a != b {
		t.Fatal("output is not deterministic")
	}
	for _, want := range []string{"1 tools, 2 actions", "Demo \\| tool.", "| `get` | read | Get one | `n`*, `full` |", "| `delete` | destructive |"} {
		if !strings.Contains(a, want) {
			t.Errorf("missing %q in:\n%s", want, a)
		}
	}
}

func TestGenerate(t *testing.T) {
	doc, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(doc, "# Tool reference") {
		t.Errorf("unexpected output start: %.40q", doc)
	}
}
