package shared

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"
	"github.com/AndreyQuantum/forgejo-mcp/internal/gitremote"
)

// Target is the result of the default target rule of section 3.1 step 4.
type Target struct {
	Kind   string // "pr" | "branch"
	PR     int64  // set when Kind == "pr"
	Branch string
	Label  string // first output line, e.g. "target: pr #42 (branch fix/login)"
}

type pullHead struct {
	Number int64 `json:"number"`
	Head   struct {
		Ref  string `json:"ref"`
		Repo *struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"head"`
}

// ResolveTarget applies the target rule: the open PR of the current branch,
// else the branch head, else the default branch. The current branch is used
// only when owner/repo is the git-detected repository.
func ResolveTarget(ctx context.Context, c *forgejo.Client, det *gitremote.Detector, owner, repo string) (Target, error) {
	meta, err := RepoMeta(ctx, c, owner, repo)
	if err != nil {
		return Target{}, err
	}
	def := Target{Kind: "branch", Branch: meta.DefaultBranch, Label: "target: branch " + meta.DefaultBranch + " (default)"}
	if det == nil {
		return def, nil
	}
	gOwner, gName, ok := det.GitRepo()
	if !ok || !strings.EqualFold(gOwner, owner) || !strings.EqualFold(gName, repo) {
		return def, nil
	}
	branch := det.CurrentBranch()
	if branch == "" || branch == meta.DefaultBranch {
		return def, nil
	}
	// The current branch counts only when it exists on the server.
	exists, err := branchExists(ctx, c, owner, repo, branch)
	if err != nil {
		return Target{}, err
	}
	if !exists {
		return def, nil
	}
	var prs []pullHead
	q := url.Values{"state": {"open"}, "head": {branch}}
	if _, err := c.List(ctx, repoPath(owner, repo)+"/pulls", q, 1, 5, &prs); err != nil {
		var ae *forgejo.APIError
		if !errors.As(err, &ae) || (ae.Status != http.StatusNotFound && ae.Status != http.StatusForbidden) {
			return Target{}, err
		}
		prs = nil // pulls unit disabled or hidden: no PR
	}
	full := strings.ToLower(owner + "/" + repo)
	for _, p := range prs {
		if p.Head.Repo != nil && strings.ToLower(p.Head.Repo.FullName) == full && p.Head.Ref == branch {
			return Target{
				Kind: "pr", PR: p.Number, Branch: branch,
				Label: "target: pr #" + itoa(p.Number) + " (branch " + branch + ")",
			}, nil
		}
	}
	return Target{Kind: "branch", Branch: branch, Label: "target: branch " + branch + " (current)"}, nil
}

// branchExists reports whether the branch exists on the server (404 = no).
func branchExists(ctx context.Context, c *forgejo.Client, owner, repo, branch string) (bool, error) {
	var br struct {
		Name string `json:"name"`
	}
	_, err := c.GetJSON(ctx, forgejo.Path("repos", owner, repo, "branches", forgejo.Wild(branch)), nil, &br)
	if err == nil {
		return true, nil
	}
	var ae *forgejo.APIError
	if errors.As(err, &ae) && ae.Status == http.StatusNotFound {
		return false, nil
	}
	return false, err
}
