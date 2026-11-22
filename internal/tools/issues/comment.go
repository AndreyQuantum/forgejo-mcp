package issues

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"
	"github.com/AndreyQuantum/forgejo-mcp/internal/format"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
)

// commentParams is the flat parameter struct of the `comment` tool.
type commentParams struct {
	Action  string   `json:"action"`
	Repo    string   `json:"repo,omitempty" jsonschema:"owner/name; default repo when omitted"`
	N       int64    `json:"n,omitempty" jsonschema:"issue or PR number"`
	ID      int64    `json:"id,omitempty" jsonschema:"comment id"`
	Body    string   `json:"body,omitempty" jsonschema:"comment text (markdown)"`
	Last    int      `json:"last,omitempty" jsonschema:"newest N comments, default 10"`
	Since   string   `json:"since,omitempty" jsonschema:"updated after, RFC 3339"`
	FromID  int64    `json:"from_id,omitempty" jsonschema:"only comments with an id above this"`
	Types   []string `json:"types,omitempty" jsonschema:"event types, e.g. label, close, review, merge_pull"`
	Page    int      `json:"page,omitempty"`
	Limit   int      `json:"limit,omitempty"`
	BodyMax *int     `json:"body_max,omitempty" jsonschema:"chars per comment, default 300"`
	Full    bool     `json:"full,omitempty" jsonschema:"lift per-field caps"`
}

func registerComment(r *tools.Registry) {
	t := &commentTool{}
	R, W := tools.Read, tools.Write
	r.Add(tools.Spec{
		Name:        "comment",
		Toolset:     "issues",
		Description: "Issue/PR comments: list, add, edit, delete, timeline, repo-wide feed.",
		Params:      commentParams{},
		RepoScoped:  true,
		Actions: []tools.Action{
			tools.NewAction("list", R, t.list,
				tools.Summary("comments of n: newest 10, or page/limit, or after from_id"),
				tools.Params("n", "last", "since", "from_id", "page", "limit", "body_max", "full"), tools.Required("n"),
				tools.Endpoints("GET /repos/{owner}/{repo}/issues/{index}/comments")),
			tools.NewAction("add", W, t.add,
				tools.Summary("comment on an issue or PR"),
				tools.Params("n", "body"), tools.Required("n", "body"),
				tools.Endpoints("POST /repos/{owner}/{repo}/issues/{index}/comments")),
			tools.NewAction("edit", W, t.edit,
				tools.Summary("edit a comment"),
				tools.Params("id", "body"), tools.Required("id", "body"),
				tools.Endpoints("PATCH /repos/{owner}/{repo}/issues/comments/{id}")),
			tools.NewAction("delete", W, t.delete,
				tools.Summary("delete a plain comment"),
				tools.Params("id"), tools.Required("id"),
				tools.Endpoints("GET /repos/{owner}/{repo}/issues/comments/{id}", "DELETE /repos/{owner}/{repo}/issues/comments/{id}")),
			tools.NewAction("timeline", R, t.timeline,
				tools.Summary("events of n: labels, closes, refs, reviews, comments"),
				tools.Params("n", "types", "page", "limit"), tools.Required("n"),
				tools.Endpoints("GET /repos/{owner}/{repo}/issues/{index}/timeline")),
			tools.NewAction("feed", R, t.feed,
				tools.Summary("newest comments of the whole repo"),
				tools.Params("since", "page", "limit", "body_max", "full"),
				tools.Endpoints("GET /repos/{owner}/{repo}/issues/comments")),
		},
	})
}

type commentTool struct{}

func commentPath(c *tools.Call, id int64) string {
	return forgejo.Path("repos", c.Owner, c.Repo, "issues", "comments", strconv.FormatInt(id, 10))
}

// commentChars is the per-comment cap of list lines.
func commentChars(bodyMax *int, full bool) int {
	switch {
	case full, bodyMax != nil && *bodyMax == 0:
		return hardBodyMax
	case bodyMax != nil && *bodyMax > 0:
		return min(*bodyMax, hardBodyMax)
	}
	return commentMax
}

func (t *commentTool) list(c *tools.Call, p *commentParams) (*tools.Result, error) {
	q := url.Values{}
	since, err := rfc3339(p.Since, "since")
	if err != nil {
		return c.Fail("%s", err), nil
	}
	addIf(q, "since", since)
	var all []*comment
	if _, err := c.Env.Client.GetJSON(c.Ctx, issuePath(c, p.N, "comments"), q, &all); err != nil {
		return nil, err
	}
	all = dropNilComments(all)
	chars := commentChars(p.BodyMax, p.Full)
	var b format.B
	var show []*comment
	switch {
	case p.FromID > 0:
		for _, cm := range all {
			if cm.ID > p.FromID {
				show = append(show, cm)
			}
		}
		b.Raw(fmt.Sprintf("# comments %d/%d after c%d\n", len(show), len(all), p.FromID))
	case p.Page > 0 || p.Limit > 0:
		page, limit := pageArgs(c, p.Page, p.Limit)
		lo := min((page-1)*limit, len(all))
		hi := min(lo+limit, len(all))
		show = all[lo:hi]
		b.Raw(format.ListHeader("comments", forgejo.Page{
			Page: page, Limit: limit, Total: int64(len(all)), Shown: len(show), More: hi < len(all),
		}) + "\n")
	default:
		last := p.Last
		if last <= 0 {
			last = 10
		}
		show = all[max0(len(all)-last):]
		if len(show) < len(all) {
			b.Raw(fmt.Sprintf("# comments %d/%d (newest %d; pass last=N or page=)\n", len(show), len(all), len(show)))
		} else {
			b.Raw(fmt.Sprintf("# comments %d/%d\n", len(show), len(all)))
		}
	}
	for _, cm := range show {
		b.Raw(commentLine(cm, chars))
	}
	return &tools.Result{Text: b.String()}, nil
}

func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

func (t *commentTool) add(c *tools.Call, p *commentParams) (*tools.Result, error) {
	var out comment
	if _, err := c.Env.Client.SendJSON(c.Ctx, http.MethodPost, issuePath(c, p.N, "comments"), nil, map[string]string{"body": p.Body}, &out); err != nil {
		return nil, err
	}
	return &tools.Result{Text: fmt.Sprintf("ok: comment c%d on #%d", out.ID, p.N)}, nil
}

func (t *commentTool) edit(c *tools.Call, p *commentParams) (*tools.Result, error) {
	resp, err := c.Env.Client.SendJSON(c.Ctx, http.MethodPatch, commentPath(c, p.ID), nil, map[string]string{"body": p.Body}, nil)
	if err != nil {
		return nil, err
	}
	if resp.Status == http.StatusNoContent {
		return c.Fail("not an editable comment"), nil
	}
	return &tools.Result{Text: fmt.Sprintf("ok: edited comment c%d", p.ID)}, nil
}

func (t *commentTool) delete(c *tools.Call, p *commentParams) (*tools.Result, error) {
	cl := c.Env.Client
	notPlain := c.Fail("not a plain comment; use review.comment_delete")
	resp, err := cl.Do(c.Ctx, forgejo.Request{Method: http.MethodGet, Path: commentPath(c, p.ID)})
	if apiStatus(err) == http.StatusNotFound {
		return c.Fail("404 comment not found"), nil
	}
	if err != nil {
		return nil, err
	}
	if resp.Status == http.StatusNoContent {
		return notPlain, nil
	}
	if _, err := cl.Do(c.Ctx, forgejo.Request{Method: http.MethodDelete, Path: commentPath(c, p.ID)}); err != nil {
		return nil, err
	}
	// The server answers 204 without deleting some comment types.
	after, err := cl.Do(c.Ctx, forgejo.Request{Method: http.MethodGet, Path: commentPath(c, p.ID)})
	switch {
	case err == nil && after.Status == http.StatusOK:
		return notPlain, nil
	case apiStatus(err) == http.StatusNotFound:
	case err != nil:
		return nil, fmt.Errorf("deletion of comment c%d could not be verified: %w", p.ID, err)
	}
	return &tools.Result{Text: fmt.Sprintf("ok: deleted comment c%d", p.ID)}, nil
}

// timelineText fetches one page of the timeline (the total is unknown, 6.7),
// drops null and code items, filters by types and renders header + lines.
func timelineText(c *tools.Call, n int64, types []string, page, limit int) (string, error) {
	var b format.B
	var lines format.B
	var pg forgejo.Page
	for extra := 0; ; extra++ {
		var raw []*timelineEvent
		got, err := c.Env.Client.List(c.Ctx, issuePath(c, n, "timeline"), nil, page, limit, &raw)
		if err != nil {
			return "", err
		}
		hidden, kept := 0, 0
		for _, e := range raw {
			if e == nil || e.Type == "code" {
				hidden++
				continue
			}
			if len(types) > 0 && !slices.Contains(types, e.Type) {
				continue
			}
			kept++
			lines.Raw(eventLine(c.Owner+"/"+c.Repo, e))
		}
		pg = forgejo.PageUnknownTotal(got.Page, got.Limit, got.Shown, kept)
		// A filtered-empty page of a full raw page may hide later events.
		if kept == 0 && pg.More && extra < 3 {
			page = got.Page + 1
			continue
		}
		b.Raw(format.ListHeader("timeline", pg))
		if hidden > 0 {
			b.Raw(fmt.Sprintf(" (%d hidden)", hidden))
		}
		b.Raw("\n")
		break
	}
	b.Raw(lines.String())
	return b.String(), nil
}

func (t *commentTool) timeline(c *tools.Call, p *commentParams) (*tools.Result, error) {
	page, limit := pageArgs(c, p.Page, p.Limit)
	out, err := timelineText(c, p.N, p.Types, page, limit)
	if err != nil {
		return nil, err
	}
	return &tools.Result{Text: out}, nil
}

func (t *commentTool) feed(c *tools.Call, p *commentParams) (*tools.Result, error) {
	q := url.Values{}
	since, err := rfc3339(p.Since, "since")
	if err != nil {
		return c.Fail("%s", err), nil
	}
	addIf(q, "since", since)
	page, limit := pageArgs(c, p.Page, p.Limit)
	var items []*comment
	pg, err := c.Env.Client.List(c.Ctx, repoBase(c)+"/issues/comments", q, page, limit, &items)
	if err != nil {
		return nil, err
	}
	var b format.B
	b.Raw(format.ListHeader("comments", pg) + "\n")
	chars := commentChars(p.BodyMax, p.Full)
	for _, cm := range dropNilComments(items) {
		b.Raw(feedLine(cm, chars))
	}
	return &tools.Result{Text: strings.TrimRight(b.String(), "\n") + "\n"}, nil
}
