package meta

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/AndreyQuantum/forgejo-mcp/internal/endpoints"
	"github.com/AndreyQuantum/forgejo-mcp/internal/format"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
)

// endpointsParams are the parameters of forgejo_endpoints (section 9.5).
type endpointsParams struct {
	Q      string `json:"q,omitempty" jsonschema:"space-separated terms; all must match"`
	Tag    string `json:"tag,omitempty" jsonschema:"activitypub, admin, issue, miscellaneous, notification, organization, package, repository, settings or user"`
	Method string `json:"method,omitempty" jsonschema:"GET, POST, PUT, PATCH or DELETE"`
	Detail bool   `json:"detail,omitempty" jsonschema:"add params and token scope for every result"`
	Limit  int    `json:"limit,omitempty" jsonschema:"results per page, default 15, max 50"`
	Page   int    `json:"page,omitempty" jsonschema:"page number from 1"`
}

const (
	endpointsDefaultLimit = 15
	endpointsMaxLimit     = 50
	endpointsInlineParams = 5
)

// registerEndpoints registers forgejo_endpoints.
func registerEndpoints(r *tools.Registry) {
	e := &endpointsTool{reg: r}
	r.Add(tools.Spec{
		Name:        "forgejo_endpoints",
		Toolset:     "meta",
		Description: "Search the Forgejo API index (method, path, params) to find the right forgejo_api call.",
		Params:      endpointsParams{},
		Single:      true,
		Actions: []tools.Action{
			tools.NewAction("run", tools.Read, e.run,
				tools.Summary("search the endpoint index"),
				tools.Params("q", "tag", "method", "detail", "limit", "page"),
			),
		},
	})
}

type endpointsTool struct{ reg *tools.Registry }

func (e *endpointsTool) run(c *tools.Call, p *endpointsParams) (*tools.Result, error) {
	limit := p.Limit
	if limit <= 0 {
		limit = endpointsDefaultLimit
	}
	limit = min(limit, endpointsMaxLimit)
	page := max(p.Page, 1)
	ops, total := endpoints.Search(p.Q, strings.ToLower(strings.TrimSpace(p.Tag)), strings.TrimSpace(p.Method), limit, page)

	pg := format.Page{Page: page, Limit: limit, Total: int64(total), Shown: len(ops), More: page*limit < total}
	hdr := format.ListHeader("endpoints", pg)
	if p.Q != "" {
		hdr += fmt.Sprintf(" (q=%q)", p.Q)
	}
	var b format.B
	b.Raw(hdr + "\n")
	inline := p.Detail || (len(ops) > 0 && len(ops) <= endpointsInlineParams)
	for i := range ops {
		op := &ops[i]
		cover := "-"
		if e.reg != nil {
			if by := e.reg.CoveredBy(op.Key()); by != "" {
				cover = by
			}
		}
		line := []string{op.M + " " + op.P, op.ID, op.S, "tool: " + cover}
		if op.Dep {
			line = append(line, "deprecated")
		}
		b.Raw(strings.Join(line, " | ") + "\n")
		if !inline {
			continue
		}
		if s := queryLine(op); s != "" {
			b.Raw("  query: " + s + "\n")
		}
		if s := bodyLine(op); s != "" {
			b.Raw("  body: " + s + "\n")
		}
		if p.Detail && op.SC != "" {
			b.Raw("  scope: " + scopeText(op) + "\n")
		}
	}
	if len(ops) == 0 {
		b.Raw("no matching endpoint; try fewer terms or another tag\n")
	}
	return &tools.Result{Text: c.Cap(strings.TrimRight(b.String(), "\n"))}, nil
}

// queryLine renders `visible(b) page(i) state(open|closed|all)`; required
// parameters carry a `*` after the name.
func queryLine(op *endpoints.Op) string {
	parts := make([]string, 0, len(op.Q))
	for _, q := range op.Q {
		if len(q) < 2 {
			continue
		}
		name, typ := q[0], q[1]
		note := ""
		if len(q) > 2 {
			note = q[2]
		}
		req := strings.HasSuffix(note, "*")
		note = strings.TrimSuffix(note, "*")
		s := name
		if req {
			s += "*"
		}
		switch {
		case typ == "e" && note != "":
			s += "(" + note + ")"
		case typ != "s" && typ != "":
			s += "(" + typ + ")"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " ")
}

// bodyLine renders `name* description ephemeral(b)`.
func bodyLine(op *endpoints.Op) string {
	parts := make([]string, 0, len(op.B))
	for _, f := range op.B {
		if len(f) < 3 {
			continue
		}
		name, _ := f[0].(string)
		typ, _ := f[1].(string)
		s := name
		if req, _ := f[2].(float64); req == 1 {
			s += "*"
		}
		if typ != "s" && typ != "" {
			s += "(" + typ + ")"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " ")
}

// scopeText renders the token scope: read:<cat> for GET/HEAD, write:<cat>
// otherwise; "a|b" categories list both.
func scopeText(op *endpoints.Op) string {
	lvl := "write:"
	if op.M == http.MethodGet || op.M == http.MethodHead {
		lvl = "read:"
	}
	cats := strings.Split(op.SC, "|")
	for i, c := range cats {
		cats[i] = lvl + c
	}
	return strings.Join(cats, "|")
}
