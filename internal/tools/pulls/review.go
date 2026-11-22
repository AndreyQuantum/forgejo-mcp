package pulls

import (
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"
	"github.com/AndreyQuantum/forgejo-mcp/internal/format"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools/shared"
)

// inlineComment is one inline comment of `review create`.
type inlineComment struct {
	Path       string `json:"path" jsonschema:"file path"`
	Line       int    `json:"line" jsonschema:"line number on the chosen side"`
	Side       string `json:"side,omitempty" jsonschema:"new (default) or old"`
	Body       string `json:"body" jsonschema:"comment text"`
	ExtraLines int    `json:"extra_lines,omitempty" jsonschema:"extra lines above for a range"`
}

// reviewParams are the parameters of the review tool (section 8.4).
type reviewParams struct {
	Action         string          `json:"action"`
	Repo           string          `json:"repo,omitempty" jsonschema:"owner/name, default detected repo"`
	N              int64           `json:"n,omitempty" jsonschema:"PR number"`
	ReviewID       int64           `json:"review_id,omitempty" jsonschema:"review id"`
	ID             int64           `json:"id,omitempty" jsonschema:"review comment id"`
	Event          string          `json:"event,omitempty" jsonschema:"approve, request_changes, comment or pending"`
	Body           string          `json:"body,omitempty" jsonschema:"review text"`
	Comments       []inlineComment `json:"comments,omitempty" jsonschema:"inline comments"`
	CommitID       string          `json:"commit_id,omitempty" jsonschema:"commit to review (default: PR head)"`
	Message        string          `json:"message,omitempty" jsonschema:"dismissal reason"`
	Reviewers      []string        `json:"reviewers,omitempty" jsonschema:"user logins"`
	TeamReviewers  []string        `json:"team_reviewers,omitempty" jsonschema:"team names"`
	OnlyUnresolved bool            `json:"only_unresolved,omitempty" jsonschema:"hide resolved inline comments"`
	WithComments   *bool           `json:"with_comments,omitempty" jsonschema:"include inline comments, default true"`
	Verbose        bool            `json:"verbose,omitempty" jsonschema:"show the diff hunk of inline comments"`
	Page           int             `json:"page,omitempty"`
	Limit          int             `json:"limit,omitempty"`
}

type reviewTool struct{}

func registerReview(r *tools.Registry) {
	t := &reviewTool{}
	rev := func(path string) string { return "/repos/{owner}/{repo}/pulls/{index}/" + path }
	r.Add(tools.Spec{
		Name:        "review",
		Toolset:     "pulls",
		Description: "PR reviews: list with inline comments, create, submit, dismiss, delete, request/unrequest reviewers.",
		Params:      reviewParams{},
		RepoScoped:  true,
		Actions: []tools.Action{
			tools.NewAction("list", tools.Read, t.list,
				tools.Summary("reviews with inline comments; requested reviewers on one line"),
				tools.Params("n", "with_comments", "only_unresolved", "verbose", "page", "limit"),
				tools.Required("n"),
				tools.Endpoints("GET "+rev("reviews"), "GET "+rev("reviews/{id}/comments")),
			),
			tools.NewAction("create", tools.Write, t.create,
				tools.Summary("review the PR: event approve|request_changes|comment|pending, optional inline comments"),
				tools.Params("n", "event", "body", "comments", "commit_id"),
				tools.Required("n", "event"),
				tools.Endpoints("POST "+rev("reviews")),
				tools.Needs("write access (not the PR author for approve/request_changes)"),
			),
			tools.NewAction("submit", tools.Write, t.submit,
				tools.Summary("submit a pending review"),
				tools.Params("n", "review_id", "event", "body"),
				tools.Required("n", "review_id", "event"),
				tools.Endpoints("POST "+rev("reviews/{id}")),
			),
			tools.NewAction("dismiss", tools.Write, t.dismiss("dismissals"),
				tools.Summary("dismiss an approval or change request"),
				tools.Params("n", "review_id", "message"),
				tools.Required("n", "review_id"),
				tools.Endpoints("POST "+rev("reviews/{id}/dismissals")),
				tools.Needs("repo admin"),
			),
			tools.NewAction("undismiss", tools.Write, t.dismiss("undismissals"),
				tools.Summary("restore a dismissed review"),
				tools.Params("n", "review_id"),
				tools.Required("n", "review_id"),
				tools.Endpoints("POST "+rev("reviews/{id}/undismissals")),
				tools.Needs("repo admin"),
			),
			tools.NewAction("delete", tools.Write, t.delete,
				tools.Summary("delete a review"),
				tools.Params("n", "review_id"),
				tools.Required("n", "review_id"),
				tools.Endpoints("DELETE "+rev("reviews/{id}")),
				tools.Needs("review author or site admin"),
			),
			tools.NewAction("request", tools.Write, t.request(http.MethodPost),
				tools.Summary("request reviews from users or teams"),
				tools.Params("n", "reviewers", "team_reviewers"),
				tools.Required("n"),
				tools.Endpoints("POST "+rev("requested_reviewers")),
				tools.Needs("write access"),
			),
			tools.NewAction("unrequest", tools.Write, t.request(http.MethodDelete),
				tools.Summary("withdraw review requests"),
				tools.Params("n", "reviewers", "team_reviewers"),
				tools.Required("n"),
				tools.Endpoints("DELETE "+rev("requested_reviewers")),
				tools.Needs("write access"),
			),
			tools.NewAction("comment_delete", tools.Write, t.commentDelete,
				tools.Summary("delete an inline review comment"),
				tools.Params("n", "review_id", "id"),
				tools.Required("n", "review_id", "id"),
				tools.Endpoints("DELETE "+rev("reviews/{id}/comments/{comment}")),
				tools.Needs("comment author or repo admin"),
			),
		},
	})
}

func reviewPath(c *tools.Call, n int64, rest ...string) string {
	return pullPath(c, n, append([]string{"reviews"}, rest...)...)
}

// ---- list ---------------------------------------------------------------

type reviewRow struct {
	rev      shared.Review
	comments []reviewComment
	cerr     error
}

func (t *reviewTool) list(c *tools.Call, p *reviewParams) (*tools.Result, error) {
	cl := c.Env.Client
	all, err := shared.AllReviews(c.Ctx, cl, c.Owner, c.Repo, p.N)
	if err != nil {
		return nil, err
	}
	me := ""
	if cl.Token() != "" {
		if u, merr := cl.Me(c.Ctx); merr == nil {
			me = u.Login
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].ID < all[j].ID })
	var requested []string
	var rows []*reviewRow
	for _, r := range all {
		switch {
		case r.State == "REQUEST_REVIEW":
			who := r.User
			if who == "" && r.Team != "" {
				who = "team:" + r.Team
			}
			if who != "" {
				requested = appendUnique(requested, who)
			}
		case r.State == "PENDING" && !strings.EqualFold(r.User, me):
			// other users' pending drafts are not ours to show
		default:
			rows = append(rows, &reviewRow{rev: r})
		}
	}

	page, limit := pageArgs(c, p.Page, p.Limit, 0)
	start := min((page-1)*limit, len(rows))
	end := min(start+limit, len(rows))
	pg := forgejo.Page{Page: page, Limit: limit, Total: int64(len(rows)), Shown: end - start, More: end < len(rows)}

	withComments := p.WithComments == nil || *p.WithComments
	if withComments {
		var fns []func()
		for _, row := range rows[start:end] {
			if row.rev.CommentsCnt <= 0 {
				continue
			}
			fns = append(fns, func() {
				row.cerr = func() error {
					_, err := cl.GetJSON(c.Ctx, reviewPath(c, p.N, itoa(row.rev.ID), "comments"), nil, &row.comments)
					return err
				}()
			})
		}
		parallel(fns...)
	}

	var b format.B
	b.Raw(format.ListHeader("reviews", pg) + "\n")
	if len(requested) > 0 {
		b.Raw("requested: " + strings.Join(requested, ",") + "\n")
	}
	for _, row := range rows[start:end] {
		b.Raw(reviewLine(row.rev) + "\n")
		if row.cerr != nil {
			b.Raw("  comments unavailable (" + oneLineErr(row.cerr) + ")\n")
			continue
		}
		for _, rc := range row.comments {
			if p.OnlyUnresolved && rc.Resolver != nil && rc.Resolver.Login != "" {
				continue
			}
			b.Raw("  " + strings.ReplaceAll(reviewCommentLine(rc, p.Verbose), "\n", "\n  ") + "\n")
		}
	}
	return &tools.Result{Text: strings.TrimRight(b.String(), "\n")}, nil
}

// ---- create / submit ------------------------------------------------------

var eventNames = map[string]string{
	"approve":         "APPROVED",
	"approved":        "APPROVED",
	"request_changes": "REQUEST_CHANGES",
	"comment":         "COMMENT",
	"pending":         "PENDING",
}

// apiEvent maps our event word to the API enum.
func apiEvent(s string) (string, bool) {
	e, ok := eventNames[strings.ToLower(strings.TrimSpace(s))]
	return e, ok
}

type reviewCommentBody struct {
	Path        string `json:"path"`
	Body        string `json:"body"`
	NewPosition int    `json:"new_position,omitempty"`
	OldPosition int    `json:"old_position,omitempty"`
	ExtraLines  int    `json:"extra_lines_count,omitempty"`
}

type reviewBody struct {
	Event    string              `json:"event"`
	Body     string              `json:"body,omitempty"`
	CommitID string              `json:"commit_id,omitempty"`
	Comments []reviewCommentBody `json:"comments,omitempty"`
}

type reviewResult struct {
	ID    int64  `json:"id"`
	State string `json:"state"`
}

func reviewOK(r reviewResult, n int64) string {
	out := fmt.Sprintf("ok: review r%d %s", r.ID, r.State)
	if r.State == "PENDING" {
		out += fmt.Sprintf(" (draft; publish with review submit n=%d review_id=%d event=...)", n, r.ID)
	}
	return out
}

func (t *reviewTool) create(c *tools.Call, p *reviewParams) (*tools.Result, error) {
	cl := c.Env.Client
	ev, ok := apiEvent(p.Event)
	if !ok {
		return c.Fail("invalid: event %q; valid: approve,request_changes,comment,pending", p.Event), nil
	}
	if ev == "REQUEST_CHANGES" && strings.TrimSpace(p.Body) == "" {
		return c.Fail("invalid: request_changes needs body"), nil
	}
	if ev == "COMMENT" && strings.TrimSpace(p.Body) == "" && len(p.Comments) == 0 {
		return c.Fail("invalid: comment needs body or inline comments"), nil
	}
	body := reviewBody{Event: ev, Body: p.Body, CommitID: p.CommitID}
	for i, cm := range p.Comments {
		if strings.TrimSpace(cm.Path) == "" || strings.TrimSpace(cm.Body) == "" || cm.Line < 1 {
			return c.Fail("invalid: comments[%d] needs path, line >= 1 and body", i), nil
		}
		rb := reviewCommentBody{Path: cm.Path, Body: cm.Body, ExtraLines: cm.ExtraLines}
		switch strings.ToLower(cm.Side) {
		case "", "new":
			rb.NewPosition = cm.Line
		case "old":
			rb.OldPosition = cm.Line
		default:
			return c.Fail("invalid: comments[%d].side %q; valid: new,old", i, cm.Side), nil
		}
		body.Comments = append(body.Comments, rb)
	}
	if ev == "APPROVED" || ev == "REQUEST_CHANGES" {
		// the server answers 422 on your own PR; say so before the request
		if me, err := cl.Me(c.Ctx); err == nil {
			if pr, perr := (&prTool{}).fetch(c, p.N); perr == nil && pr.author() != "" && strings.EqualFold(pr.author(), me.Login) {
				return c.Fail("invalid: you are the author of pr #%d; approve and request_changes are not allowed on your own PR (use event=comment)", p.N), nil
			}
		}
	}
	var res reviewResult
	if _, err := cl.SendJSON(c.Ctx, http.MethodPost, reviewPath(c, p.N), nil, body, &res); err != nil {
		return nil, err
	}
	return &tools.Result{Text: reviewOK(res, p.N)}, nil
}

func (t *reviewTool) submit(c *tools.Call, p *reviewParams) (*tools.Result, error) {
	ev, ok := apiEvent(p.Event)
	if !ok || ev == "PENDING" {
		return c.Fail("invalid: event %q; valid: approve,request_changes,comment", p.Event), nil
	}
	if ev == "REQUEST_CHANGES" && strings.TrimSpace(p.Body) == "" {
		return c.Fail("invalid: request_changes needs body"), nil
	}
	body := map[string]any{"event": ev}
	if p.Body != "" {
		body["body"] = p.Body
	}
	var res reviewResult
	if _, err := c.Env.Client.SendJSON(c.Ctx, http.MethodPost, reviewPath(c, p.N, itoa(p.ReviewID)), nil, body, &res); err != nil {
		return nil, err
	}
	return &tools.Result{Text: reviewOK(res, p.N)}, nil
}

// ---- dismiss / delete / request ---------------------------------------------

func (t *reviewTool) dismiss(kind string) func(*tools.Call, *reviewParams) (*tools.Result, error) {
	return func(c *tools.Call, p *reviewParams) (*tools.Result, error) {
		var body any
		if kind == "dismissals" {
			body = map[string]any{"message": p.Message}
		} else {
			body = map[string]any{}
		}
		var res reviewResult
		if _, err := c.Env.Client.SendJSON(c.Ctx, http.MethodPost, reviewPath(c, p.N, itoa(p.ReviewID), kind), nil, body, &res); err != nil {
			return nil, err
		}
		verb := "dismissed"
		if kind == "undismissals" {
			verb = "restored"
		}
		return &tools.Result{Text: fmt.Sprintf("ok: %s review r%d", verb, p.ReviewID)}, nil
	}
}

func (t *reviewTool) delete(c *tools.Call, p *reviewParams) (*tools.Result, error) {
	if _, err := c.Env.Client.Do(c.Ctx, forgejo.Request{Method: http.MethodDelete, Path: reviewPath(c, p.N, itoa(p.ReviewID))}); err != nil {
		return nil, err
	}
	return &tools.Result{Text: fmt.Sprintf("ok: deleted review r%d", p.ReviewID)}, nil
}

func (t *reviewTool) request(method string) func(*tools.Call, *reviewParams) (*tools.Result, error) {
	return func(c *tools.Call, p *reviewParams) (*tools.Result, error) {
		if len(p.Reviewers) == 0 && len(p.TeamReviewers) == 0 {
			return c.Fail("invalid: pass reviewers or team_reviewers"), nil
		}
		body := map[string]any{}
		if len(p.Reviewers) > 0 {
			body["reviewers"] = p.Reviewers
		}
		if len(p.TeamReviewers) > 0 {
			body["team_reviewers"] = p.TeamReviewers
		}
		if _, err := c.Env.Client.Do(c.Ctx, forgejo.Request{Method: method, Path: pullPath(c, p.N, "requested_reviewers"), JSON: body}); err != nil {
			return nil, err
		}
		who := slices.Clone(p.Reviewers)
		for _, tm := range p.TeamReviewers {
			who = append(who, "team:"+tm)
		}
		verb := "requested"
		if method == http.MethodDelete {
			verb = "unrequested"
		}
		return &tools.Result{Text: "ok: " + verb + " " + strings.Join(who, ",")}, nil
	}
}

func (t *reviewTool) commentDelete(c *tools.Call, p *reviewParams) (*tools.Result, error) {
	path := reviewPath(c, p.N, itoa(p.ReviewID), "comments", itoa(p.ID))
	if _, err := c.Env.Client.Do(c.Ctx, forgejo.Request{Method: http.MethodDelete, Path: path}); err != nil {
		return nil, err
	}
	return &tools.Result{Text: fmt.Sprintf("ok: deleted review comment rc%d", p.ID)}, nil
}
