package pulls

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"
	"github.com/AndreyQuantum/forgejo-mcp/internal/format"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools/shared"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools/shared/ci"
)

// mergeBody is the body of `POST R/pulls/{n}/merge`. The key casing is
// exactly the one of the server's MergePullRequestOption (mixed on purpose).
type mergeBody struct {
	Do                     string `json:"Do"`
	MergeTitleField        string `json:"MergeTitleField,omitempty"`
	MergeMessageField      string `json:"MergeMessageField,omitempty"`
	MergeCommitID          string `json:"MergeCommitID,omitempty"`
	HeadCommitID           string `json:"head_commit_id,omitempty"`
	ForceMerge             bool   `json:"force_merge,omitempty"`
	MergeWhenChecksSucceed bool   `json:"merge_when_checks_succeed,omitempty"`
	DeleteBranchAfterMerge bool   `json:"delete_branch_after_merge,omitempty"`
}

// mergeRetryDelays are the waits before retrying a 405 "Please try again
// later" (the conflict check is still running).
var mergeRetryDelays = []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}

var mergeStyles = []string{"merge", "rebase", "rebase-merge", "squash", "fast-forward-only", "manually-merged"}

// allowedStyles lists the merge styles the repo enables.
func allowedStyles(r *forgejo.Repo) []string {
	var out []string
	for _, s := range []struct {
		on   bool
		name string
	}{
		{r.AllowMergeCommits, "merge"},
		{r.AllowRebase, "rebase"},
		{r.AllowRebaseExplicit, "rebase-merge"},
		{r.AllowSquashMerge, "squash"},
		{r.AllowFastForwardOnlyMerge, "fast-forward-only"},
	} {
		if s.on {
			out = append(out, s.name)
		}
	}
	return out
}

// defaultStyle maps the repo's default_merge_style to our enum.
func defaultStyle(r *forgejo.Repo) string {
	switch s := r.DefaultMergeStyle; s {
	case "":
		return "merge"
	case "rebase-update-only":
		return "rebase"
	default:
		return s
	}
}

func styleAllowed(r *forgejo.Repo, style string) bool {
	switch style {
	case "merge":
		return r.AllowMergeCommits
	case "rebase":
		return r.AllowRebase
	case "rebase-merge":
		return r.AllowRebaseExplicit
	case "squash":
		return r.AllowSquashMerge
	case "fast-forward-only":
		return r.AllowFastForwardOnlyMerge
	}
	return true // manually-merged is never pre-checked (section 10.3)
}

// merge implements the flow of section 10.3.
func (t *prTool) merge(c *tools.Call, p *prParams) (*tools.Result, error) {
	cl := c.Env.Client
	n := p.N
	pr, err := t.fetch(c, n)
	if err != nil {
		return nil, err
	}
	switch {
	case pr.Merged:
		return c.Fail("pr #%d is already merged (merge_commit %s)", n, format.SHA(pr.MergeCommitSHA, 8)), nil
	case pr.State == "closed":
		return c.Fail("pr #%d is closed, not merged; reopen with pr edit state=open", n), nil
	case pr.Draft:
		return c.Fail("pr #%d is a draft (title prefix WIP); pr edit draft=false", n), nil
	case p.HeadSHA != "" && !strings.HasPrefix(pr.Head.SHA, p.HeadSHA) && !strings.HasPrefix(p.HeadSHA, pr.Head.SHA):
		return c.Fail("head moved (%s); re-check and retry", format.SHA(pr.Head.SHA, 8)), nil
	}
	meta, err := shared.RepoMeta(c.Ctx, cl, c.Owner, c.Repo)
	if err != nil {
		return nil, err
	}
	method := strings.TrimSpace(p.Method)
	if method == "" {
		method = defaultStyle(meta)
	}
	known := false
	for _, s := range mergeStyles {
		known = known || s == method
	}
	if !known {
		return c.Fail("invalid: method %q; valid: %s", method, strings.Join(mergeStyles, ",")), nil
	}
	if !styleAllowed(meta, method) {
		return c.Fail("style %s disabled in this repo; allowed: %s", method, strings.Join(allowedStyles(meta), ",")), nil
	}
	deleteBranch := meta.DefaultDeleteBranchAfterMerge
	if p.DeleteBranch != nil {
		deleteBranch = *p.DeleteBranch
	}
	if method == "manually-merged" {
		// the server returns right after marking the PR merged and never
		// deletes the head branch for this style
		deleteBranch = false
	}
	headID := ""
	if p.HeadSHA != "" {
		headID = pr.Head.SHA // the server compares the full commit id
	}
	body := mergeBody{
		Do:                     method,
		MergeTitleField:        p.MergeTitle,
		MergeMessageField:      p.MergeMessage,
		MergeCommitID:          p.MergeCommit,
		HeadCommitID:           headID,
		ForceMerge:             p.Force,
		MergeWhenChecksSucceed: p.WhenGreen,
		DeleteBranchAfterMerge: deleteBranch,
	}

	var resp *forgejo.Response
	for i := 0; ; i++ {
		resp, err = cl.Do(c.Ctx, forgejo.Request{Method: http.MethodPost, Path: pullPath(c, n, "merge"), JSON: body})
		ae := apiError(err)
		if err != nil && ae != nil && ae.IsMergeNotReady() && i < len(mergeRetryDelays) {
			if c.Progress != nil {
				c.Progress(fmt.Sprintf("pr #%d: conflict check still running, retrying", n), float64(i+1), float64(len(mergeRetryDelays)))
			}
			if serr := sleep(c, mergeRetryDelays[i]); serr != nil {
				return nil, serr
			}
			continue
		}
		break
	}
	if err != nil {
		if ae := apiError(err); ae != nil && ae.IsMergeNotReady() {
			return c.Fail("405 merge check still running (or the PR conflicts: mergeable=no); retry later or pr update"), nil
		}
		return t.mergeError(c, p, pr, method, deleteBranch, err)
	}

	if resp.Status == http.StatusCreated {
		c.Env.Memo.Set(automergeKey(c, n))
		out := fmt.Sprintf("ok: auto-merge scheduled for pr #%d (merges when checks succeed; cancel with pr automerge_cancel)", n)
		// Forgejo schedules even when the head has no checks at all (verified
		// on 17.0.2); the merge then waits for a status that may never come.
		if cs, cerr := ci.CombinedStatus(c.Ctx, cl, c.Owner, c.Repo, pr.Head.SHA); cerr == nil && cs.Total == 0 {
			out += "\nnote: no checks exist for this head, so nothing will trigger the merge; automerge_cancel and merge directly if none is expected"
		}
		return &tools.Result{Text: out}, nil
	}
	return t.mergedResult(c, n, method, deleteBranch, ""), nil
}

// mergedResult re-reads the PR after a successful merge for its merge commit.
func (t *prTool) mergedResult(c *tools.Call, n int64, method string, deleted bool, branchNote string) *tools.Result {
	sha := ""
	if pr, err := t.fetch(c, n); err == nil {
		sha = format.SHA(pr.MergeCommitSHA, 8)
	}
	out := fmt.Sprintf("ok: merged pr #%d", n)
	if sha != "" {
		out += " as " + sha
	}
	var extra []string
	if method != "" {
		extra = append(extra, method)
	}
	if deleted && branchNote == "" {
		extra = append(extra, "branch deleted")
	}
	if len(extra) > 0 {
		out += " (" + strings.Join(extra, ", ") + ")"
	}
	if branchNote != "" {
		out += "; branch not deleted (" + branchNote + ")"
	}
	return &tools.Result{Text: out}
}

var reConflictFile = regexp.MustCompile(`CONFLICT \([^)]*\): Merge conflict in ([^\s"\\]+)`)

// hasMessage reports whether an error body carries a server `message`.
func hasMessage(ae *forgejo.APIError) bool {
	b := strings.TrimSpace(ae.Body)
	if b == "" || b == "{}" {
		return false
	}
	return strings.Contains(b, `"message"`) && !strings.Contains(b, `"message":""`)
}

// mergeError resolves the ambiguous failures of the merge POST (10.3 step 5).
func (t *prTool) mergeError(c *tools.Call, p *prParams, pr *pullReq, method string, deleteBranch bool, err error) (*tools.Result, error) {
	ae := apiError(err)
	if ae == nil {
		return nil, err
	}
	n := pr.Number
	switch {
	case deleteBranch && (ae.Status == http.StatusForbidden || ae.Status == http.StatusInternalServerError):
		// the merge may have happened and only the branch deletion failed
		if now, gerr := t.fetch(c, n); gerr == nil && now.Merged {
			reason := ae.Message
			if reason == "" {
				reason = "server error"
			}
			return t.mergedResult(c, n, method, true, format.OneLine(reason, 120)), nil
		}
	case ae.Status == http.StatusConflict && !hasMessage(ae):
		if method == "manually-merged" {
			return c.Fail("409 merge_commit_id does not match a commit on the base branch"), nil
		}
		if now, gerr := t.fetch(c, n); gerr == nil && now.Merged {
			return &tools.Result{Text: fmt.Sprintf("ok: pr #%d is already merged as %s", n, format.SHA(now.MergeCommitSHA, 8))}, nil
		}
		var files []string
		seen := map[string]bool{}
		for _, m := range reConflictFile.FindAllStringSubmatch(ae.Body, -1) {
			if !seen[m[1]] && len(files) < 10 {
				seen[m[1]] = true
				files = append(files, m[1])
			}
		}
		if len(files) > 0 {
			return c.Fail("merge conflict in %s; update the branch (pr update) or resolve locally", strings.Join(files, ", ")), nil
		}
		if b := strings.TrimSpace(ae.Body); b != "" && b != "{}" {
			return c.Fail("409 conflict: %s", format.OneLine(b, 300)), nil
		}
		return c.Fail("409 merge conflict; update the branch (pr update) or resolve locally"), nil
	}
	return nil, err
}
