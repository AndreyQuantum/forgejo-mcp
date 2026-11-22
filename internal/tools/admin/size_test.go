package admin

import (
	"encoding/json"
	"testing"

	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
)

// TestSchemaWithinToolBudget keeps the admin tool under the per-tool schema
// budget (it is excluded from the total because its toolset is off by default).
func TestSchemaWithinToolBudget(t *testing.T) {
	s := spec(t)
	sc, err := tools.BuildInputSchema(s, s.Actions)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"name": s.Name, "description": s.Description, "inputSchema": sc})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("admin tool schema: %d bytes", len(raw))
	if len(raw) > tools.BudgetToolBytes {
		t.Fatalf("admin schema %d bytes > %d", len(raw), tools.BudgetToolBytes)
	}
	if n := len([]rune(s.Description)); n > tools.BudgetDescriptionChars {
		t.Fatalf("description %d chars", n)
	}
}
