package repos

import (
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"

	"github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"
	"github.com/AndreyQuantum/forgejo-mcp/internal/format"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
)

// wikiParams are the parameters of the wiki tool (section 8.2).
type wikiParams struct {
	Action   string `json:"action"`
	Repo     string `json:"repo,omitempty" jsonschema:"owner/name; default: detected repo"`
	PageName string `json:"page_name,omitempty" jsonschema:"sub_url from list"`
	Title    string `json:"title,omitempty" jsonschema:"page title; a new title renames on edit"`
	Content  string `json:"content,omitempty" jsonschema:"page text (markdown)"`
	Message  string `json:"message,omitempty" jsonschema:"commit message"`
	BodyMax  *int   `json:"body_max,omitempty" jsonschema:"max chars of the page, default 6000; 0 = up to 20000"`
	Page     int    `json:"page,omitempty" jsonschema:"page from 1"`
	Limit    int    `json:"limit,omitempty" jsonschema:"items per page, max 100"`
}

const (
	wikiDefaultBody = 6000
	wikiHardBody    = 20000
	// wikiRevisionsPage is the fixed page size of the revisions endpoint
	// (setting.Git.CommitsRangeSize default); the server ignores limit.
	wikiRevisionsPage = 50
)

type wikiTool struct{}

func registerWiki(r *tools.Registry) {
	t := &wikiTool{}
	R := "/repos/{owner}/{repo}"
	r.Add(tools.Spec{
		Name:        "wiki",
		Toolset:     "repos",
		Description: "Wiki pages: list, get, create, edit, delete, history.",
		Params:      wikiParams{},
		RepoScoped:  true,
		Actions: []tools.Action{
			tools.NewAction("list", tools.Read, t.list,
				tools.Summary("list wiki pages"), tools.Params("page", "limit"),
				tools.Endpoints("GET "+R+"/wiki/pages")),
			tools.NewAction("get", tools.Read, t.get,
				tools.Summary("read a page"), tools.Params("body_max"), tools.Required("page_name"),
				tools.Endpoints("GET "+R+"/wiki/page/{pageName}")),
			tools.NewAction("create", tools.Write, t.create,
				tools.Summary("create a page"), tools.Params("message"), tools.Required("title", "content"),
				tools.Needs("wiki write access"),
				tools.Endpoints("POST "+R+"/wiki/new")),
			tools.NewAction("edit", tools.Write, t.edit,
				tools.Summary("edit or rename a page"), tools.Params("title", "content", "message"),
				tools.Required("page_name"), tools.Needs("wiki write access"),
				tools.Endpoints("GET "+R+"/wiki/page/{pageName}", "PATCH "+R+"/wiki/page/{pageName}")),
			tools.NewAction("delete", tools.Write, t.del,
				tools.Summary("delete a page"), tools.Required("page_name"), tools.Needs("wiki write access"),
				tools.Endpoints("DELETE "+R+"/wiki/page/{pageName}")),
			tools.NewAction("history", tools.Read, t.history,
				tools.Summary("revisions of a page"), tools.Params("page"), tools.Required("page_name"),
				tools.Endpoints("GET "+R+"/wiki/revisions/{pageName}")),
		},
	})
}

// wikiPagePath escapes the page name as ONE path segment while keeping valid
// %XX sequences (the sub_url of a page is already escaped).
func wikiPagePath(c *tools.Call, kind, name string) string {
	seg := strings.ReplaceAll(forgejo.EscapeKeepPct(name), "/", "%2F")
	return repoPath(c, "wiki", kind) + "/" + seg
}

func (t *wikiTool) list(c *tools.Call, p *wikiParams) (*tools.Result, error) {
	var items []wikiMeta
	// ListWikiPages treats limit <= 1 as "use the default page size", so ask
	// for at least 2 and cut client-side for limit=1.
	limit := limitOf(c, p.Limit)
	pg, err := c.Env.Client.List(c.Ctx, repoPath(c, "wiki", "pages"), nil, pageOf(p.Page), max(limit, 2), &items)
	if err != nil {
		return nil, err
	}
	if limit == 1 {
		if len(items) > 1 {
			items = items[:1]
		}
		pg.Limit, pg.Shown = 1, len(items)
		if pg.Total >= 0 {
			pg.More = int64(pg.Page) < pg.Total
		} else {
			pg.More = len(items) == 1
		}
	}
	var b strings.Builder
	b.WriteString(format.ListHeader("wiki pages", pg) + "\n")
	for i := range items {
		b.WriteString(wikiMetaLine(&items[i]))
	}
	return text(b.String()), nil
}

func decodeB64(s string) (string, error) {
	s = strings.Join(strings.Fields(s), "")
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return string(b), nil
		}
	}
	return "", base64.CorruptInputError(0)
}

// bodyLimit resolves body_max (nil = default, 0 = hard cap).
func bodyLimit(n *int) int {
	switch {
	case n == nil:
		return wikiDefaultBody
	case *n <= 0:
		return wikiHardBody
	}
	return min(*n, wikiHardBody)
}

func (t *wikiTool) get(c *tools.Call, p *wikiParams) (*tools.Result, error) {
	var w wikiPage
	if _, err := c.Env.Client.GetJSON(c.Ctx, wikiPagePath(c, "page", p.PageName), nil, &w); err != nil {
		return nil, err
	}
	content, err := decodeB64(w.ContentB64)
	if err != nil {
		return c.Fail("invalid: page content is not valid base64"), nil
	}
	var b format.B
	b.Raw("wiki " + w.Title + "\n")
	b.KV("sub_url", w.SubURL)
	if w.LastCommit != nil && w.LastCommit.Committer != nil {
		b.KV("updated", format.T(w.LastCommit.Committer.Date))
	}
	b.KV("commits", w.CommitCount)
	want := bodyLimit(p.BodyMax)
	budget := c.Chars(want, wikiDefaultBody)
	how := "pass body_max=0 for up to 20000 chars"
	if want >= wikiHardBody {
		how = "raise max_chars to see more"
	}
	if budget < want {
		how = "raise max_chars to see more"
	}
	shown := format.Trunc(content, budget, how)
	marker := ""
	if i := strings.LastIndex(shown, "\n[truncated:"); i >= 0 {
		shown, marker = shown[:i], shown[i+1:]
	}
	if strings.TrimSpace(shown) == "" {
		b.KV("body", "(empty)")
	} else {
		b.Raw("body:\n  " + strings.ReplaceAll(strings.TrimRight(shown, "\n"), "\n", "\n  ") + "\n")
	}
	out := b.String()
	if marker != "" {
		out += marker + "\n"
	}
	return text(out), nil
}

func wikiBody(title, content, message string) map[string]any {
	body := map[string]any{"content_base64": base64.StdEncoding.EncodeToString([]byte(content))}
	setStr(body, "title", title)
	setStr(body, "message", message)
	return body
}

func (t *wikiTool) create(c *tools.Call, p *wikiParams) (*tools.Result, error) {
	var w wikiPage
	if _, err := send(c.Ctx, c.Env.Client, http.MethodPost, repoPath(c, "wiki", "new"), nil, wikiBody(p.Title, p.Content, p.Message), &w, 0); err != nil {
		return nil, addHint(err, http.StatusBadRequest, "the title may be empty, reserved or already used (use wiki.edit)")
	}
	return okf("created wiki page %s (sub_url %s)", w.Title, w.SubURL), nil
}

func (t *wikiTool) edit(c *tools.Call, p *wikiParams) (*tools.Result, error) {
	cl := c.Env.Client
	if p.Title == "" && p.Content == "" {
		return c.Fail("invalid: pass content and/or title"), nil
	}
	// the server derives the new page name from the title and turns an empty
	// title into "unnamed", so the title is always sent; a missing title or
	// content is read from the current page
	title, content := p.Title, p.Content
	if title == "" || content == "" {
		var cur wikiPage
		if _, err := cl.GetJSON(c.Ctx, wikiPagePath(c, "page", p.PageName), nil, &cur); err != nil {
			return nil, err
		}
		if title == "" {
			title = cur.Title
		}
		if content == "" {
			dec, err := decodeB64(cur.ContentB64)
			if err != nil {
				return c.Fail("invalid: current page content is not valid base64"), nil
			}
			content = dec
		}
	}
	var w wikiPage
	if _, err := send(c.Ctx, cl, http.MethodPatch, wikiPagePath(c, "page", p.PageName), nil, wikiBody(title, content, p.Message), &w, 0); err != nil {
		return nil, err
	}
	return okf("edited wiki page %s (sub_url %s)", w.Title, w.SubURL), nil
}

func (t *wikiTool) del(c *tools.Call, p *wikiParams) (*tools.Result, error) {
	if _, err := send(c.Ctx, c.Env.Client, http.MethodDelete, wikiPagePath(c, "page", p.PageName), nil, nil, nil, 0); err != nil {
		return nil, err
	}
	return okf("deleted wiki page %s", p.PageName), nil
}

func (t *wikiTool) history(c *tools.Call, p *wikiParams) (*tools.Result, error) {
	page := pageOf(p.Page)
	var rev wikiRevisions
	if _, err := c.Env.Client.GetJSON(c.Ctx, wikiPagePath(c, "revisions", p.PageName), url.Values{"page": {itoa(page)}}, &rev); err != nil {
		return nil, err
	}
	shown := len(rev.Commits)
	pg := forgejo.Page{
		Page: page, Limit: wikiRevisionsPage, Total: int64(rev.Count), Shown: shown,
		More: int64((page-1)*wikiRevisionsPage+shown) < int64(rev.Count) && shown > 0,
	}
	var b strings.Builder
	b.WriteString(format.ListHeader("revisions", pg) + "\n")
	for _, cm := range rev.Commits {
		who, when := "", ""
		if cm.Author != nil {
			who, when = cm.Author.Name, format.T(cm.Author.Date)
		}
		b.WriteString(strings.TrimSpace(format.SHA(cm.SHA, 8)+" "+when+" "+who) + " | " + format.OneLine(firstLine(cm.Message), 100) + "\n")
	}
	return text(b.String()), nil
}
