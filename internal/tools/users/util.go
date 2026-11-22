package users

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"
	"github.com/AndreyQuantum/forgejo-mcp/internal/format"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
)

// renderList renders a list header plus one line per item.
func renderList[T any](kind string, pg forgejo.Page, items []T, line func(T) string) string {
	var b strings.Builder
	b.WriteString(format.ListHeader(kind, pg))
	for _, it := range items {
		b.WriteString("\n")
		b.WriteString(line(it))
	}
	return b.String()
}

// listPage fetches one page of a plain JSON-array list and renders it.
func listPage[T any](c *tools.Call, path string, q url.Values, page, limit int, kind string, line func(T) string) (*tools.Result, error) {
	var items []T
	pg, err := c.Env.Client.List(c.Ctx, path, q, page, defLimit(c, limit), &items)
	if err != nil {
		return nil, err
	}
	return &tools.Result{Text: c.Cap(renderList(kind, pg, items, line))}, nil
}

// defLimit applies FORGEJO_DEFAULT_LIMIT when no limit was requested.
func defLimit(c *tools.Call, n int) int {
	if n <= 0 && c.Env.Cfg != nil && c.Env.Cfg.DefaultLimit > 0 {
		return c.Env.Cfg.DefaultLimit
	}
	return n
}

// userOrMe returns u, or the login of the authenticated user when u is empty.
func userOrMe(c *tools.Call, u string) (string, error) {
	if u = strings.TrimSpace(u); u != "" {
		return u, nil
	}
	me, err := c.Env.Client.Me(c.Ctx)
	if err != nil {
		return "", err
	}
	return me.Login, nil
}

// ok renders a mutation result line.
func ok(format string, a ...any) *tools.Result {
	return &tools.Result{Text: "ok: " + fmt.Sprintf(format, a...)}
}
