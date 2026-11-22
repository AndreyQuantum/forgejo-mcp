package pulls

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"
	"github.com/AndreyQuantum/forgejo-mcp/internal/format"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools/shared"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools/shared/ci"
)

// prParams are the parameters of the pr tool (section 8.4).
type prParams struct {
	Action string `json:"action"`
	Repo   string `json:"repo,omitempty" jsonschema:"owner/name, default detected repo"`
	N      int64  `json:"n,omitempty" jsonschema:"PR number"`

	State         string   `json:"state,omitempty" jsonschema:"list: open, closed, merged or all; edit: open or closed"`
	Author        string   `json:"author,omitempty" jsonschema:"filter by author login"`
	Base          string   `json:"base,omitempty" jsonschema:"target branch (default: repo default branch)"`
	Head          string   `json:"head,omitempty" jsonschema:"source branch, owner:branch for forks"`
	Labels        []string `json:"labels,omitempty" jsonschema:"label names"`
	Milestone     string   `json:"milestone,omitempty" jsonschema:"milestone title or id; none clears (edit)"`
	Sort          string   `json:"sort,omitempty" jsonschema:"oldest, recentupdate, recentclose, leastupdate, mostcomment, leastcomment, priority"`
	Title         string   `json:"title,omitempty" jsonschema:"PR title"`
	Body          *string  `json:"body,omitempty" jsonschema:"PR description in markdown"`
	Draft         *bool    `json:"draft,omitempty" jsonschema:"draft (WIP title prefix)"`
	Assignees     []string `json:"assignees,omitempty" jsonschema:"assignee logins (edit replaces)"`
	Reviewers     []string `json:"reviewers,omitempty" jsonschema:"reviewer logins to request"`
	TeamReviewers []string `json:"team_reviewers,omitempty" jsonschema:"reviewer team names to request"`
	Due           string   `json:"due,omitempty" jsonschema:"due date YYYY-MM-DD"`
	UnsetDue      bool     `json:"unset_due,omitempty" jsonschema:"remove the due date"`
	AllowMaint    *bool    `json:"allow_maintainer_edit,omitempty" jsonschema:"let maintainers push to the head branch"`

	Method       string `json:"method,omitempty" jsonschema:"merge, rebase, rebase-merge, squash, fast-forward-only, manually-merged"`
	MergeTitle   string `json:"merge_title,omitempty" jsonschema:"merge commit title"`
	MergeMessage string `json:"merge_message,omitempty" jsonschema:"merge commit message"`
	WhenGreen    bool   `json:"when_green,omitempty" jsonschema:"schedule the merge for when checks pass"`
	DeleteBranch *bool  `json:"delete_branch,omitempty" jsonschema:"delete head branch after merge"`
	HeadSHA      string `json:"head_sha,omitempty" jsonschema:"merge only if the head is still this sha"`
	Force        bool   `json:"force,omitempty" jsonschema:"bypass branch protection (admins)"`
	MergeCommit  string `json:"merge_commit_id,omitempty" jsonschema:"manually-merged: commit already on base"`
	Style        string `json:"style,omitempty" jsonschema:"update: merge or rebase"`

	Include    []string `json:"include,omitempty" jsonschema:"sections: body, files, commits, ci, reviews, comments"`
	BodyMax    *int     `json:"body_max,omitempty" jsonschema:"body size in chars, 0 = up to 20000"`
	Paths      []string `json:"paths,omitempty" jsonschema:"only these files (globs)"`
	MaxChars   int      `json:"max_chars,omitempty" jsonschema:"diff size in chars, default 14000, max 100000"`
	Whitespace string   `json:"whitespace,omitempty" jsonschema:"ignore-all, ignore-change, ignore-eol or show-all"`
	Page       int      `json:"page,omitempty"`
	Limit      int      `json:"limit,omitempty"`
	Full       bool     `json:"full,omitempty" jsonschema:"lift per-field size caps"`
	Verbose    bool     `json:"verbose,omitempty" jsonschema:"add shas, lock and maintainer flags"`
}

const (
	diffDefaultChars = 14000
	diffReadMax      = 4 << 20
	filesDefaultPage = 50
)

var includeValid = []string{"body", "files", "commits", "ci", "reviews", "comments"}

type prTool struct{}

func registerPR(r *tools.Registry) {
	t := &prTool{}
	r.Add(tools.Spec{
		Name:        "pr",
		Toolset:     "pulls",
		Description: "Pull requests: list, get (with CI and review summary), create, edit, merge, update branch, files, diff, commits.",
		Params:      prParams{},
		RepoScoped:  true,
		Actions: []tools.Action{
			tools.NewAction("list", tools.Read, t.list,
				tools.Summary("list PRs (state=open by default)"),
				tools.Params("state", "author", "base", "head", "labels", "milestone", "sort", "page", "limit"),
				tools.Endpoints("GET /repos/{owner}/{repo}/pulls"),
				tools.Hint500("label filter with a non-numeric id"),
			),
			tools.NewAction("get", tools.Read, t.get,
				tools.Summary("one PR with CI, reviews and blocked_by; n, head or current branch"),
				tools.Params("n", "head", "base", "include", "body_max", "full", "verbose"),
				tools.Endpoints("GET /repos/{owner}/{repo}/pulls/{index}", "GET /repos/{owner}/{repo}/pulls/{base}/{head}", "GET /repos/{owner}/{repo}/branches/{branch}", "GET /repos/{owner}/{repo}/pulls/{index}/files", "GET /repos/{owner}/{repo}/pulls/{index}/commits", "GET /repos/{owner}/{repo}/pulls/{index}/reviews", "GET /repos/{owner}/{repo}/issues/{index}/comments"),
			),
			tools.NewAction("create", tools.Write, t.create,
				tools.Summary("open a PR (head defaults to the current git branch)"),
				tools.Params("head", "title", "base", "body", "draft", "labels", "assignees", "reviewers", "team_reviewers", "milestone", "due"),
				tools.Required("title"),
				tools.Endpoints("POST /repos/{owner}/{repo}/pulls", "POST /repos/{owner}/{repo}/pulls/{index}/requested_reviewers"),
				tools.Needs("write access"),
			),
			tools.NewAction("edit", tools.Write, t.edit,
				tools.Summary("change title, body, base, state, draft, labels, assignees, milestone, due"),
				tools.Params("n", "title", "body", "base", "state", "draft", "labels", "assignees", "milestone", "due", "unset_due", "allow_maintainer_edit"),
				tools.Required("n"),
				tools.Endpoints("PATCH /repos/{owner}/{repo}/pulls/{index}", "GET /repos/{owner}/{repo}/pulls/{index}", "PATCH /repos/{owner}/{repo}/issues/{index}"),
				tools.Needs("write access or PR author"),
			),
			tools.NewAction("merge", tools.Write, t.merge,
				tools.Summary("merge now, or when_green=true to merge when checks pass"),
				tools.Params("n", "method", "merge_title", "merge_message", "when_green", "delete_branch", "head_sha", "force", "merge_commit_id"),
				tools.Required("n"),
				tools.Endpoints("POST /repos/{owner}/{repo}/pulls/{index}/merge", "GET /repos/{owner}/{repo}/pulls/{index}"),
				tools.Needs("write access or merge whitelist"),
				tools.Hint500("merged but the head branch could not be deleted; manually-merged with an unknown commit"),
			),
			tools.NewAction("automerge_cancel", tools.Write, t.automergeCancel,
				tools.Summary("cancel a scheduled auto-merge"),
				tools.Params("n"),
				tools.Required("n"),
				tools.Endpoints("DELETE /repos/{owner}/{repo}/pulls/{index}/merge"),
				tools.Needs("write access"),
			),
			tools.NewAction("update", tools.Write, t.update,
				tools.Summary("merge or rebase the base branch into the head branch"),
				tools.Params("n", "style"),
				tools.Required("n"),
				tools.Endpoints("POST /repos/{owner}/{repo}/pulls/{index}/update"),
				tools.Needs("write access"),
			),
			tools.NewAction("files", tools.Read, t.files,
				tools.Summary("changed files with +/- counts"),
				tools.Params("n", "whitespace", "page", "limit"),
				tools.Required("n"),
				tools.Endpoints("GET /repos/{owner}/{repo}/pulls/{index}/files"),
			),
			tools.NewAction("diff", tools.Read, t.diff,
				tools.Summary("unified diff, filtered by paths and capped by max_chars"),
				tools.Params("n", "paths", "max_chars"),
				tools.Required("n"),
				tools.Endpoints("GET /repos/{owner}/{repo}/pulls/{index}.{diffType}"),
			),
			tools.NewAction("commits", tools.Read, t.commits,
				tools.Summary("commits of the PR"),
				tools.Params("n", "page", "limit"),
				tools.Required("n"),
				tools.Endpoints("GET /repos/{owner}/{repo}/pulls/{index}/commits"),
			),
		},
	})
}

func (t *prTool) fetch(c *tools.Call, n int64) (*pullReq, error) {
	var pr pullReq
	if _, err := c.Env.Client.GetJSON(c.Ctx, pullPath(c, n), nil, &pr); err != nil {
		return nil, err
	}
	return &pr, nil
}

// ---- list ---------------------------------------------------------------

func (t *prTool) list(c *tools.Call, p *prParams) (*tools.Result, error) {
	cl := c.Env.Client
	state := p.State
	if state == "" {
		state = "open"
	}
	q := url.Values{}
	switch state {
	case "open", "closed", "all":
		q.Set("state", state)
	case "merged":
		q.Set("state", "closed")
	default:
		return c.Fail("invalid: state %q; valid: open,closed,merged,all", state), nil
	}
	if p.Sort != "" {
		q.Set("sort", p.Sort)
	}
	if p.Author != "" {
		q.Set("poster", p.Author)
	}
	if p.Base != "" {
		q.Set("base", shared.NormalizeRef(p.Base))
	}
	if p.Head != "" {
		head := shared.NormalizeRef(p.Head)
		if _, br, ok := strings.Cut(head, ":"); ok {
			// the server filters by plain branch name only
			head = br
		}
		q.Set("head", head)
	}
	if len(p.Labels) > 0 {
		ids, err := shared.ResolveLabels(c.Ctx, cl, c.Owner, c.Repo, p.Labels)
		if err != nil {
			return c.Fail("%s", err.Error()), nil
		}
		for _, id := range ids {
			q.Add("labels", itoa(id))
		}
	}
	if p.Milestone != "" {
		id, err := shared.ResolveMilestone(c.Ctx, cl, c.Owner, c.Repo, p.Milestone)
		if err != nil {
			return c.Fail("%s", err.Error()), nil
		}
		q.Set("milestone", itoa(id))
	}

	page, limit := pageArgs(c, p.Page, p.Limit, 0)
	var kept []*pullReq
	hidden := 0
	var pg forgejo.Page
	rawLen := 0
	for attempt := 0; ; attempt++ {
		var items []*pullReq
		var err error
		pg, err = cl.List(c.Ctx, repoPath(c, "pulls"), q, page, limit, &items)
		if err != nil {
			return nil, err
		}
		rawLen = len(items)
		kept = kept[:0]
		hidden = 0
		for _, it := range items {
			switch {
			case it == nil:
				hidden++
			case state == "merged" && !it.Merged:
			default:
				kept = append(kept, it)
			}
		}
		// A merged-only view can come back empty from a full raw page: look
		// one page further (at most 3 extra requests).
		if state != "merged" || len(kept) > 0 || !pg.More || attempt >= 3 {
			break
		}
		page++
	}
	pg.Shown = len(kept)
	if state == "merged" {
		// client-side post-filter: the server total counts all closed PRs
		pg = forgejo.PageUnknownTotal(pg.Page, pg.Limit, rawLen, len(kept))
	}
	var b format.B
	hdr := format.ListHeader("prs", pg)
	if hidden > 0 {
		hdr += fmt.Sprintf(" (%d hidden)", hidden)
	}
	b.Raw(hdr + "\n")
	for _, it := range kept {
		b.Raw(prLine(it) + "\n")
	}
	return &tools.Result{Text: strings.TrimRight(b.String(), "\n")}, nil
}

// ---- get ----------------------------------------------------------------

// byHead finds a PR by head branch (section 8.4 `get`).
func (t *prTool) byHead(c *tools.Call, p *prParams) (*pullReq, error) {
	cl := c.Env.Client
	head := strings.TrimSpace(p.Head)
	base := shared.NormalizeRef(p.Base)
	if base == "" && strings.Contains(head, ":") {
		meta, err := shared.RepoMeta(c.Ctx, cl, c.Owner, c.Repo)
		if err != nil {
			return nil, err
		}
		base = meta.DefaultBranch
	}
	if base != "" {
		if badRefPath(head) || badRefPath(base) {
			return nil, errInvalidRef
		}
		var pr pullReq
		path := repoPath(c, "pulls", base, forgejo.Wild(head))
		if _, err := cl.GetJSON(c.Ctx, path, nil, &pr); err != nil {
			if apiStatus(err) == http.StatusNotFound {
				return nil, nil
			}
			return nil, err
		}
		return &pr, nil
	}
	var items []*pullReq
	q := url.Values{"state": {"all"}, "head": {shared.NormalizeRef(head)}}
	if _, err := cl.List(c.Ctx, repoPath(c, "pulls"), q, 1, 5, &items); err != nil {
		return nil, err
	}
	for _, it := range items {
		if it != nil {
			return it, nil
		}
	}
	return nil, nil
}

func includeSet(p *prParams, pr *pullReq) (map[string]bool, error) {
	set := map[string]bool{}
	if len(p.Include) == 0 {
		set["body"] = true
		if pr.isOpen() {
			set["ci"], set["reviews"] = true, true
		}
		return set, nil
	}
	for _, s := range p.Include {
		s = strings.ToLower(strings.TrimSpace(s))
		if !slices.Contains(includeValid, s) {
			return nil, fmt.Errorf("invalid: include %q; valid: %s", s, strings.Join(includeValid, ","))
		}
		set[s] = true
	}
	return set, nil
}

func (t *prTool) get(c *tools.Call, p *prParams) (*tools.Result, error) {
	cl := c.Env.Client
	var b format.B
	var pr *pullReq
	var err error
	switch {
	case p.N > 0:
		pr, err = t.fetch(c, p.N)
	case strings.TrimSpace(p.Head) != "":
		pr, err = t.byHead(c, p)
		if errors.Is(err, errInvalidRef) {
			return c.Fail("invalid: head"), nil
		}
		if err == nil && pr == nil {
			return c.Fail("no pr found for head %s", p.Head), nil
		}
	default:
		tg, terr := shared.ResolveTarget(c.Ctx, cl, c.Env.Repo, c.Owner, c.Repo)
		if terr != nil {
			return nil, terr
		}
		if tg.Kind != "pr" {
			return c.Fail("no open pr for %s; pass n or head\n%s", tg.Branch, tg.Label), nil
		}
		b.Raw(tg.Label + "\n")
		pr, err = t.fetch(c, tg.PR)
	}
	if err != nil {
		return nil, err
	}
	inc, ierr := includeSet(p, pr)
	if ierr != nil {
		return c.Fail("%s", ierr.Error()), nil
	}

	// header
	b.Raw(fmt.Sprintf("pr #%d %s | %s\n", pr.Number, pr.stateWord(), format.OneLine(pr.Title, titleMax)))
	headSHA := format.SHA(pr.Head.SHA, 8)
	if p.Verbose {
		headSHA = pr.Head.SHA
	}
	hb := pr.headName() + "->" + pr.baseName()
	if headSHA != "" {
		hb += " (" + headSHA + ")"
	}
	parts := []string{"by " + pr.author(), hb, format.Tags(pr.labelNames()), format.Logins(pr.assigneeLogins())}
	if m := pr.milestoneTitle(); m != "" {
		parts = append(parts, "ms:"+m)
	}
	if f := pr.forkRepo(); f != "" {
		parts = append(parts, "fork:"+f)
	}
	b.Line(parts...)
	stats := ""
	if pr.Additions != 0 || pr.Deletions != 0 || pr.ChangedFiles != 0 {
		stats = fmt.Sprintf("+%d -%d files:%d", pr.Additions, pr.Deletions, pr.ChangedFiles)
	}
	cnt := ""
	if pr.Comments > 0 {
		cnt = fmt.Sprintf("c:%d", pr.Comments)
	}
	due := ""
	if pr.DueDate != "" {
		due = "due:" + datePart(format.T(pr.DueDate))
	}
	b.Line("created "+format.T(pr.CreatedAt), "updated "+format.T(pr.UpdatedAt), stats, cnt, due)
	b.KV("url", pr.HTMLURL)
	if p.Verbose {
		b.KV("base_sha", pr.Base.SHA)
		if pr.IsLocked {
			b.KV("locked", "yes")
		}
		if pr.AllowMaintainer {
			b.KV("allow_maintainer_edit", "yes")
		}
	}
	switch {
	case pr.Merged:
		by := ""
		if pr.MergedBy != nil {
			by = "by " + pr.MergedBy.Login + " "
		}
		b.KV("merged", strings.TrimSpace(by+format.T(pr.MergedAt)+" as "+format.SHA(pr.MergeCommitSHA, 8)))
	case pr.State == "closed":
		b.KV("closed", format.T(pr.ClosedAt))
	default:
		if pr.Mergeable {
			b.KV("mergeable", "yes")
		} else {
			if pr.Draft {
				b.KV("mergeable", "no (draft)")
			} else {
				b.KV("mergeable", "no (conflict or check running)")
			}
		}
	}
	if len(pr.RequestedReviewers) > 0 && !inc["reviews"] {
		b.KV("requested", format.Logins(pr.requestedLogins()))
	}

	// sections (soft failures: a failing section never fails the call)
	var (
		cs       *ci.Combined
		ciLine   string
		revSum   reviewSummary
		revLine  string
		required int64
		files    []changedFile
		filesPG  forgejo.Page
		filesErr error
		commits  []commitItem
		comPG    forgejo.Page
		comErr   error
		cmts     []issueComment
		cmtErr   error
	)
	var fns []func()
	if inc["ci"] {
		fns = append(fns, func() {
			s, err := ci.CombinedStatus(c.Ctx, cl, c.Owner, c.Repo, pr.Head.SHA)
			if err != nil {
				ciLine = "ci: unavailable (" + oneLineErr(err) + ")"
				return
			}
			cs, ciLine = s, ciText(s)
		})
	}
	if inc["reviews"] {
		fns = append(fns, func() {
			rs, err := shared.AllReviews(c.Ctx, cl, c.Owner, c.Repo, pr.Number)
			if err != nil {
				revLine = "reviews: unavailable (" + oneLineErr(err) + ")"
				return
			}

			revSum = summarizeReviews(rs, pr.requestedLogins())
			revLine = revSum.line
		}, func() {
			var bi branchInfo
			if badRefPath(pr.Base.Ref) {
				return
			}
			path := repoPath(c, "branches", forgejo.Wild(pr.Base.Ref))
			if _, err := cl.GetJSON(c.Ctx, path, nil, &bi); err == nil {
				required = bi.RequiredApprovals
			}
		})
	}
	if inc["files"] {
		fns = append(fns, func() {
			filesPG, filesErr = cl.List(c.Ctx, pullPath(c, pr.Number, "files"), nil, 1, filesDefaultPage, &files)
		})
	}
	if inc["commits"] {
		fns = append(fns, func() {
			q := url.Values{"verification": {"false"}, "files": {"false"}}
			comPG, comErr = cl.List(c.Ctx, pullPath(c, pr.Number, "commits"), q, 1, 20, &commits)
		})
	}
	if inc["comments"] {
		fns = append(fns, func() {
			_, cmtErr = cl.GetJSON(c.Ctx, repoPath(c, "issues", itoa(pr.Number), "comments"), nil, &cmts)
		})
	}
	parallel(fns...)

	if ciLine != "" {
		b.Raw(ciLine + "\n")
	}
	if revLine != "" {
		b.Raw(revLine + "\n")
	}
	if pr.isOpen() && (cs != nil || revLine != "") {
		var blocked []string
		if pr.Draft {
			blocked = append(blocked, "draft")
		}
		if !pr.Mergeable && !pr.Draft {
			blocked = append(blocked, "not mergeable")
		}
		if cs != nil && (cs.State == "failure" || cs.State == "error" || cs.State == "pending") {
			blocked = append(blocked, "ci:"+cs.State)
		}
		for _, u := range revSum.changes {
			blocked = append(blocked, "request_changes:"+u)
		}
		if required > 0 && int64(revSum.approvals) < required {
			blocked = append(blocked, fmt.Sprintf("approvals:%d/%d", revSum.approvals, required))
		}
		switch {
		case len(blocked) > 0:
			b.KV("blocked_by", strings.Join(blocked, ", "))
		case cs != nil && inc["reviews"] && revLine != "" && !strings.Contains(revLine, "unavailable"):
			b.KV("ready", "yes")
		}
	}
	if inc["files"] {
		switch {
		case filesErr != nil:
			b.Raw("files: unavailable (" + oneLineErr(filesErr) + ")\n")
		default:
			b.Raw(fmt.Sprintf("files: %d\n", max(int(filesPG.Total), len(files))))
			for _, f := range files {
				b.Raw("  " + fileLine(f) + "\n")
			}
			if filesPG.More {
				b.Raw(fmt.Sprintf("  ... more; pr files n=%d page=2\n", pr.Number))
			}
		}
	}
	if inc["commits"] {
		switch {
		case comErr != nil:
			b.Raw("commits: unavailable (" + oneLineErr(comErr) + ")\n")
		default:
			b.Raw(fmt.Sprintf("commits: %d\n", max(int(comPG.Total), len(commits))))
			for _, cm := range commits {
				b.Raw("  " + commitLine(cm) + "\n")
			}
			if comPG.More {
				b.Raw(fmt.Sprintf("  ... more; pr commits n=%d page=2\n", pr.Number))
			}
		}
	}
	if inc["comments"] {
		switch {
		case cmtErr != nil:
			b.Raw("comments: unavailable (" + oneLineErr(cmtErr) + ")\n")
		default:
			shown := cmts
			if len(shown) > 5 {
				shown = shown[len(shown)-5:]
			}
			b.Raw(fmt.Sprintf("comments: %d (last %d)\n", len(cmts), len(shown)))
			for _, cm := range shown {
				b.Raw("  " + commentLine(cm) + "\n")
			}
		}
	}
	if inc["body"] && strings.TrimSpace(pr.Body) != "" {
		b.Raw("body:\n  " + strings.ReplaceAll(bodyText(pr.Body, p.BodyMax, p.Full), "\n", "\n  ") + "\n")
	}
	return &tools.Result{Text: strings.TrimRight(b.String(), "\n")}, nil
}

// ---- create -------------------------------------------------------------

var reWIP = regexp.MustCompile(`(?i)^\s*(\[WIP\]|WIP:)\s*`)

func stripWIP(title string) string { return reWIP.ReplaceAllString(title, "") }

func isWIP(title string) bool { return reWIP.MatchString(title) }

func withWIP(title string) string { return "WIP: " + stripWIP(title) }

// currentBranch is the git branch of the working directory when the call's
// repo is the git-detected repository.
func currentBranch(c *tools.Call) string {
	if c.Env.Repo == nil {
		return ""
	}
	o, n, ok := c.Env.Repo.GitRepo()
	if !ok || !strings.EqualFold(o, c.Owner) || !strings.EqualFold(n, c.Repo) {
		return ""
	}
	return c.Env.Repo.CurrentBranch()
}

type createBody struct {
	Head      string   `json:"head"`
	Base      string   `json:"base"`
	Title     string   `json:"title"`
	Body      string   `json:"body,omitempty"`
	Assignees []string `json:"assignees,omitempty"`
	Milestone int64    `json:"milestone,omitempty"`
	Labels    []int64  `json:"labels,omitempty"`
	DueDate   string   `json:"due_date,omitempty"`
}

func (t *prTool) create(c *tools.Call, p *prParams) (*tools.Result, error) {
	cl := c.Env.Client
	meta, err := shared.RepoMeta(c.Ctx, cl, c.Owner, c.Repo)
	if err != nil {
		return nil, err
	}
	head := shared.NormalizeRef(p.Head)
	if head == "" {
		head = currentBranch(c)
	}
	if head == "" {
		return c.Fail("invalid: missing param head (no git branch detected for this repo)"), nil
	}
	base := shared.NormalizeRef(p.Base)
	if base == "" {
		base = meta.DefaultBranch
	}
	if head == base {
		return c.Fail("invalid: head and base are both %s", base), nil
	}
	title := strings.TrimSpace(p.Title)
	if title == "" {
		return c.Fail("invalid: title is empty"), nil
	}
	if p.Draft != nil && *p.Draft {
		title = withWIP(title)
	}
	body := createBody{Head: head, Base: base, Title: title, Assignees: p.Assignees}
	if p.Body != nil {
		body.Body = *p.Body
	}
	if len(p.Labels) > 0 {
		ids, lerr := shared.ResolveLabels(c.Ctx, cl, c.Owner, c.Repo, p.Labels)
		if lerr != nil {
			return c.Fail("%s", lerr.Error()), nil
		}
		body.Labels = ids
	}
	if p.Milestone != "" && p.Milestone != "none" {
		id, merr := shared.ResolveMilestone(c.Ctx, cl, c.Owner, c.Repo, p.Milestone)
		if merr != nil {
			return c.Fail("%s", merr.Error()), nil
		}
		body.Milestone = id
	}
	if p.Due != "" {
		d, derr := parseDue(p.Due)
		if derr != nil {
			return c.Fail("%s", derr.Error()), nil
		}
		body.DueDate = d
	}

	var pr pullReq
	if _, err := cl.SendJSON(c.Ctx, http.MethodPost, repoPath(c, "pulls"), nil, body, &pr); err != nil {
		if apiStatus(err) == http.StatusConflict {
			if n := t.existing(c, head, base); n > 0 {
				return c.Fail("PR already exists: #%d", n), nil
			}
		}
		return nil, err
	}
	out := fmt.Sprintf("ok: created pr #%d %s->%s  %s", pr.Number, head, base, pr.HTMLURL)
	if len(p.Reviewers) > 0 || len(p.TeamReviewers) > 0 {
		rb := map[string]any{}
		if len(p.Reviewers) > 0 {
			rb["reviewers"] = p.Reviewers
		}
		if len(p.TeamReviewers) > 0 {
			rb["team_reviewers"] = p.TeamReviewers
		}
		if _, err := cl.SendJSON(c.Ctx, http.MethodPost, pullPath(c, pr.Number, "requested_reviewers"), nil, rb, nil); err != nil {
			out += "\nwarn: reviewers not requested: " + oneLineErr(err)
		}
	}
	return &tools.Result{Text: strings.TrimRight(out, " ")}, nil
}

// existing finds the open PR for head/base after a 409 (0 when none found).
func (t *prTool) existing(c *tools.Call, head, base string) int64 {
	_, branch, found := strings.Cut(head, ":")
	if !found {
		branch = head
	}
	q := url.Values{"state": {"open"}, "head": {branch}, "base": {base}}
	var items []*pullReq
	if _, err := c.Env.Client.List(c.Ctx, repoPath(c, "pulls"), q, 1, 5, &items); err != nil {
		return 0
	}
	for _, it := range items {
		if it != nil {
			return it.Number
		}
	}
	return 0
}

// ---- edit ---------------------------------------------------------------

func (t *prTool) edit(c *tools.Call, p *prParams) (*tools.Result, error) {
	cl := c.Env.Client
	body := map[string]any{}
	var changed []string
	add := func(name string) { changed = append(changed, name) }

	title := strings.TrimSpace(p.Title)
	if p.Draft != nil || title != "" {
		cur := ""
		if p.Draft != nil && title == "" || p.Draft == nil && title != "" {
			pr, err := t.fetch(c, p.N)
			switch {
			case err == nil:
				cur = pr.Title
			case p.Draft != nil:
				return nil, err
			}
			// a failed read only costs the draft-preserving check of a plain rename
		}
		newTitle := title
		if newTitle == "" {
			newTitle = cur
		}
		switch {
		case p.Draft != nil && *p.Draft:
			newTitle = withWIP(newTitle)
		case p.Draft != nil:
			newTitle = stripWIP(newTitle)
		case isWIP(cur) && !isWIP(newTitle):
			newTitle = withWIP(newTitle) // keep the draft state when only the title changes
		}
		if title != "" || newTitle != cur {
			body["title"] = newTitle
			if title != "" {
				add("title")
			}
		}
		if p.Draft != nil {
			add("draft")
		}
	}
	if p.Body != nil {
		body["body"] = *p.Body
		add("body")
	}
	if p.Base != "" {
		body["base"] = shared.NormalizeRef(p.Base)
		add("base")
	}
	if p.State != "" {
		if p.State != "open" && p.State != "closed" {
			return c.Fail("invalid: state %q; valid: open,closed", p.State), nil
		}
		body["state"] = p.State
		add("state")
	}
	if p.Labels != nil {
		ids, err := shared.ResolveLabels(c.Ctx, cl, c.Owner, c.Repo, p.Labels)
		if err != nil {
			return c.Fail("%s", err.Error()), nil
		}
		body["labels"] = ids
		add("labels")
	}
	if p.Assignees != nil {
		body["assignees"] = p.Assignees
		add("assignees")
	}
	clearMilestone := false
	if p.Milestone != "" {
		if strings.EqualFold(p.Milestone, "none") {
			clearMilestone = true
		} else {
			id, err := shared.ResolveMilestone(c.Ctx, cl, c.Owner, c.Repo, p.Milestone)
			if err != nil {
				return c.Fail("%s", err.Error()), nil
			}
			body["milestone"] = id
		}
		add("milestone")
	}
	switch {
	case p.UnsetDue:
		body["unset_due_date"] = true
		add("due")
	case p.Due != "":
		d, err := parseDue(p.Due)
		if err != nil {
			return c.Fail("%s", err.Error()), nil
		}
		body["due_date"] = d
		add("due")
	}
	if p.AllowMaint != nil {
		body["allow_maintainer_edit"] = *p.AllowMaint
		add("allow_maintainer_edit")
	}
	if len(changed) == 0 {
		return c.Fail("invalid: nothing to edit; pass title, body, base, state, draft, labels, assignees, milestone, due or allow_maintainer_edit"), nil
	}
	if len(body) > 0 {
		// the server answers 201 here
		if _, err := cl.SendJSON(c.Ctx, http.MethodPatch, pullPath(c, p.N), nil, body, nil); err != nil {
			return nil, err
		}
	}
	if clearMilestone {
		// the pulls PATCH ignores milestone 0; the issue route clears it
		ib := map[string]any{"milestone": 0}
		if _, err := cl.SendJSON(c.Ctx, http.MethodPatch, repoPath(c, "issues", itoa(p.N)), nil, ib, nil); err != nil {
			return nil, err
		}
	}
	if p.Labels != nil || p.Milestone != "" {
		shared.InvalidateLabels(cl, c.Owner, c.Repo)
	}
	return &tools.Result{Text: fmt.Sprintf("ok: edited pr #%d: %s", p.N, strings.Join(changed, ","))}, nil
}

// ---- automerge_cancel / update -------------------------------------------

func (t *prTool) automergeCancel(c *tools.Call, p *prParams) (*tools.Result, error) {
	_, err := c.Env.Client.Do(c.Ctx, forgejo.Request{Method: http.MethodDelete, Path: pullPath(c, p.N, "merge")})
	if err != nil {
		if apiStatus(err) == http.StatusNotFound {
			return c.Fail("no auto-merge was scheduled for pr #%d (or the pr does not exist)", p.N), nil
		}
		return nil, err
	}
	return &tools.Result{Text: fmt.Sprintf("ok: auto-merge cancelled for pr #%d", p.N)}, nil
}

func (t *prTool) update(c *tools.Call, p *prParams) (*tools.Result, error) {
	cl := c.Env.Client
	style := p.Style
	switch style {
	case "":
		style = "merge"
	case "merge", "rebase":
	default:
		return c.Fail("invalid: style %q; valid: merge,rebase", style), nil
	}
	before := ""
	if pr, err := t.fetch(c, p.N); err == nil {
		before = pr.Head.SHA
	}
	q := url.Values{"style": {style}}
	if _, err := cl.Do(c.Ctx, forgejo.Request{Method: http.MethodPost, Path: pullPath(c, p.N, "update"), Query: q}); err != nil {
		return nil, err
	}
	if before != "" {
		if pr, err := t.fetch(c, p.N); err == nil && pr.Head.SHA == before {
			return &tools.Result{Text: fmt.Sprintf("ok: pr #%d already up to date", p.N)}, nil
		}
	}
	return &tools.Result{Text: fmt.Sprintf("ok: branch updated (%s) for pr #%d", style, p.N)}, nil
}

// ---- files / diff / commits -----------------------------------------------

func (t *prTool) files(c *tools.Call, p *prParams) (*tools.Result, error) {
	q := url.Values{}
	if p.Whitespace != "" {
		q.Set("whitespace", p.Whitespace)
	}
	page, limit := pageArgs(c, p.Page, p.Limit, filesDefaultPage)
	var items []changedFile
	pg, err := c.Env.Client.List(c.Ctx, pullPath(c, p.N, "files"), q, page, limit, &items)
	if err != nil {
		return nil, err
	}
	add, del := 0, 0
	for _, f := range items {
		add += f.Additions
		del += f.Deletions
	}
	hdr := format.ListHeader("files", pg)
	if !pg.More && pg.Page == 1 {
		hdr += fmt.Sprintf(" | +%d -%d in %d files", add, del, len(items))
	} else {
		hdr += fmt.Sprintf(" | +%d -%d on this page", add, del)
	}
	var b format.B
	b.Raw(hdr + "\n")
	for _, f := range items {
		b.Raw(fileLine(f) + "\n")
	}
	return &tools.Result{Text: strings.TrimRight(b.String(), "\n")}, nil
}

func (t *prTool) diff(c *tools.Call, p *prParams) (*tools.Result, error) {
	resp, err := c.Env.Client.Do(c.Ctx, forgejo.Request{
		Method:  http.MethodGet,
		Path:    repoPath(c, "pulls", itoa(p.N)+".diff"),
		MaxBody: diffReadMax,
	})
	if err != nil {
		return nil, err
	}
	all := format.SplitDiff(string(resp.Body))
	files := format.FilterDiff(all, p.Paths)
	add, del := 0, 0
	for _, f := range files {
		add += f.Added
		del += f.Removed
	}
	head := fmt.Sprintf("diff pr #%d: %d files +%d -%d", p.N, len(files), add, del)
	if len(files) == 0 {
		if len(all) == 0 {
			return &tools.Result{Text: fmt.Sprintf("diff pr #%d: empty", p.N)}, nil
		}
		names := make([]string, 0, len(all))
		for _, f := range all {
			names = append(names, f.Path)
		}
		more := ""
		if len(names) > 15 {
			more = fmt.Sprintf(", +%d more", len(names)-15)
			names = names[:15]
		}
		return &tools.Result{Text: fmt.Sprintf("%s; no file matches paths %v; files: %s%s", head, p.Paths, strings.Join(names, ", "), more)}, nil
	}
	budget := c.Chars(p.MaxChars, diffDefaultChars)
	out := head + "\n" + format.RenderDiff(files, budget)
	if resp.Truncated {
		out += "\n[truncated: the diff was read only up to 4 MiB; pass paths=[...] to narrow]"
	}
	return &tools.Result{Text: out}, nil
}

func (t *prTool) commits(c *tools.Call, p *prParams) (*tools.Result, error) {
	q := url.Values{"verification": {"false"}, "files": {"false"}}
	page, limit := pageArgs(c, p.Page, p.Limit, 0)
	var items []commitItem
	pg, err := c.Env.Client.List(c.Ctx, pullPath(c, p.N, "commits"), q, page, limit, &items)
	if err != nil {
		return nil, err
	}
	var b format.B
	b.Raw(format.ListHeader("commits", pg) + "\n")
	for _, cm := range items {
		b.Raw(commitLine(cm) + "\n")
	}
	return &tools.Result{Text: strings.TrimRight(b.String(), "\n")}, nil
}

func automergeKey(c *tools.Call, n int64) string {
	return "automerge:" + c.Owner + "/" + c.Repo + "#" + strconv.FormatInt(n, 10)
}

var errInvalidRef = errors.New("invalid ref")

// badRefPath reports an empty ref or one with "." / ".." path segments, which
// must never reach a URL path.
func badRefPath(ref string) bool {
	if ref == "" {
		return true
	}
	for _, s := range strings.Split(ref, "/") {
		if s == "." || s == ".." {
			return true
		}
	}
	return false
}
