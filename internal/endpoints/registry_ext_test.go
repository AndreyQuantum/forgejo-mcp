package endpoints_test

import (
	"strings"
	"testing"

	"github.com/AndreyQuantum/forgejo-mcp/internal/endpoints"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools/all"
)

// TestActionEndpointsExistInIndex guards against typos in Action.Endpoints:
// a misspelt entry would silently disable the forgejo_api deny/toolset rules
// and the side-effect GET list for that endpoint.
func TestActionEndpointsExistInIndex(t *testing.T) {
	r := tools.NewRegistry()
	all.RegisterAll(r)
	for _, s := range r.Specs() {
		for _, a := range s.Actions {
			for _, ep := range a.Endpoints {
				method, tmpl, ok := strings.Cut(ep, " ")
				if !ok || endpoints.Lookup(method, tmpl) == nil {
					t.Errorf("%s.%s: endpoint %q is not in the index", s.Name, a.Name, ep)
				}
			}
		}
	}
}

func TestUncoveredReport(t *testing.T) {
	r := tools.NewRegistry()
	all.RegisterAll(r)
	uncovered := 0
	for _, op := range endpoints.All() {
		if r.CoveredBy(op.Key()) == "" {
			uncovered++
		}
	}
	t.Logf("index: %d operations, %d covered by no action", len(endpoints.All()), uncovered)
}
