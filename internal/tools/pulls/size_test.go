package pulls

import (
	"encoding/json"
	"testing"

	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
)

func TestSchemaSize(t *testing.T) {
	for _, name := range []string{"pr", "review"} {
		s := specOf(t, name)
		sc, err := tools.BuildInputSchema(s, s.Actions)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(sc)
		n := len(b) + len(s.Description) + len(name)
		t.Logf("%s: %d bytes", name, n)
		if n > tools.BudgetToolBytes {
			t.Errorf("%s schema %d bytes > budget %d", name, n, tools.BudgetToolBytes)
		}
	}
}
