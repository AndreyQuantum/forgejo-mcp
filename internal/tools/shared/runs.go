package shared

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"
)

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// RunSelector selects one Actions run: exactly one of Run (id), Number (the
// per-repo index), PR, Ref or SHA, or none (target rule).
type RunSelector struct {
	Run    int64
	Number int64
	PR     int64
	Ref    string
	SHA    string
}

// RunRef identifies a resolved run.
type RunRef struct {
	ID     int64
	Number int64
	SHA    string
	Status string
}

type runItem struct {
	ID     int64  `json:"id"`
	Index  int64  `json:"index_in_repo"`
	SHA    string `json:"commit_sha"`
	Status string `json:"status"`
}

func (r runItem) ref() RunRef { return RunRef{ID: r.ID, Number: r.Index, SHA: r.SHA, Status: r.Status} }

// FullRunRef normalises a `ref` filter of the run endpoints (section 8.6):
// `main` -> `refs/heads/main`, `pr:12` -> `refs/pull/12/head`, `refs/...`
// passes through. `#12` is rejected: use pr=12.
func FullRunRef(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	switch {
	case ref == "":
		return "", errors.New("invalid: empty ref")
	case strings.HasPrefix(ref, "#"):
		return "", errors.New("invalid: use pr=12")
	case strings.HasPrefix(ref, "refs/"):
		return ref, nil
	}
	if n, ok := strings.CutPrefix(ref, "pr:"); ok {
		v, err := strconv.ParseInt(strings.TrimSpace(n), 10, 64)
		if err != nil || v < 1 {
			return "", fmt.Errorf("invalid: bad pull request number in %q", ref)
		}
		return PullRef(v), nil
	}
	return "refs/heads/" + ref, nil
}

// PullRef returns the ref of a pull request head (`refs/pull/N/head`).
func PullRef(n int64) string { return "refs/pull/" + itoa(n) + "/head" }

var hexRe = regexp.MustCompile(`^[0-9a-fA-F]{4,40}$`)

// ExpandSHA returns the full 40-char sha for a short one (section 8.6).
func ExpandSHA(ctx context.Context, c *forgejo.Client, owner, repo, sha string) (string, error) {
	sha = strings.TrimSpace(sha)
	if !hexRe.MatchString(sha) {
		return "", fmt.Errorf("invalid: bad sha %q", sha)
	}
	if len(sha) == 40 {
		return strings.ToLower(sha), nil
	}
	var out struct {
		SHA string `json:"sha"`
	}
	if _, err := c.GetJSON(ctx, repoPath(owner, repo)+"/git/commits/"+forgejo.Seg(sha), nil, &out); err != nil {
		return "", err
	}
	if out.SHA == "" {
		return "", fmt.Errorf("commit %s not found", sha)
	}
	return out.SHA, nil
}

// newestRun lists runs with the filter and returns the newest one (highest id).
func newestRun(ctx context.Context, c *forgejo.Client, owner, repo string, q url.Values) (*runItem, error) {
	q.Set("page", "1")
	q.Set("limit", "10")
	var body struct {
		Runs []runItem `json:"workflow_runs"`
	}
	if _, err := c.GetJSON(ctx, repoPath(owner, repo)+"/actions/runs", q, &body); err != nil {
		var ae *forgejo.APIError
		if errors.As(err, &ae) && ae.Message == "empty response" {
			return nil, nil
		}
		return nil, err
	}
	var best *runItem
	for i := range body.Runs {
		if best == nil || body.Runs[i].ID > best.ID {
			best = &body.Runs[i]
		}
	}
	return best, nil
}

// ResolveRun resolves a RunSelector to the newest matching run. With no
// selector it takes the newest run of the repo's default branch.
func ResolveRun(ctx context.Context, c *forgejo.Client, owner, repo string, sel RunSelector) (RunRef, error) {
	if err := checkOwnerRepo(owner, repo); err != nil {
		return RunRef{}, err
	}
	set := 0
	for _, b := range []bool{sel.Run != 0, sel.Number != 0, sel.PR != 0, sel.Ref != "", sel.SHA != ""} {
		if b {
			set++
		}
	}
	if set > 1 {
		return RunRef{}, errors.New("invalid: give only one of run, number, pr, ref, sha")
	}
	switch {
	case sel.Run != 0:
		var r runItem
		if _, err := c.GetJSON(ctx, repoPath(owner, repo)+"/actions/runs/"+itoa(sel.Run), nil, &r); err != nil {
			return RunRef{}, err
		}
		if r.ID == 0 {
			r.ID = sel.Run
		}
		return r.ref(), nil
	case sel.Number != 0:
		r, err := newestRun(ctx, c, owner, repo, url.Values{"run_number": {itoa(sel.Number)}})
		if err != nil {
			return RunRef{}, err
		}
		if r == nil {
			return RunRef{}, fmt.Errorf("no run with number no.%d", sel.Number)
		}
		return r.ref(), nil
	case sel.SHA != "":
		sha, err := ExpandSHA(ctx, c, owner, repo, sel.SHA)
		if err != nil {
			return RunRef{}, err
		}
		r, err := newestRun(ctx, c, owner, repo, url.Values{"head_sha": {sha}})
		if err != nil {
			return RunRef{}, err
		}
		if r == nil {
			return RunRef{}, fmt.Errorf("no runs for sha %s", sel.SHA)
		}
		return r.ref(), nil
	}

	var full, bare string
	switch {
	case sel.PR != 0:
		full = PullRef(sel.PR)
	case sel.Ref != "":
		f, err := FullRunRef(sel.Ref)
		if err != nil {
			return RunRef{}, err
		}
		full = f
		if f == "refs/heads/"+strings.TrimSpace(sel.Ref) {
			bare = strings.TrimSpace(sel.Ref)
		}
	default:
		meta, err := RepoMeta(ctx, c, owner, repo)
		if err != nil {
			return RunRef{}, err
		}
		full = "refs/heads/" + meta.DefaultBranch
	}
	r, err := newestRun(ctx, c, owner, repo, url.Values{"ref": {full}})
	if err != nil {
		return RunRef{}, err
	}
	if r == nil && bare != "" {
		// a bare name that has no runs as a branch is retried once as a tag
		if r, err = newestRun(ctx, c, owner, repo, url.Values{"ref": {"refs/tags/" + bare}}); err != nil {
			return RunRef{}, err
		}
	}
	if r == nil {
		return RunRef{}, fmt.Errorf("no runs for %s", full)
	}
	return r.ref(), nil
}
