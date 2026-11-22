// Package shared holds helpers used by several tool areas (DESIGN.md sections
// 1, 3.1, 5.6, 5.7, 10.5). It never imports package tools: helpers take a
// *forgejo.Client and plain arguments. WP0 defines signatures; WP6 fills them.
package shared

import (
	"context"

	"github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"
	"github.com/AndreyQuantum/forgejo-mcp/internal/gitremote"
)

// ParseOwnerRepo validates and splits "owner/name" (each part must match
// ^[A-Za-z0-9._-]+$).
func ParseOwnerRepo(s string) (owner, name string, err error) { panic("WP6") }

// RepoMeta returns the cached (5 min) `GET /repos/{o}/{r}` object.
func RepoMeta(ctx context.Context, c *forgejo.Client, owner, repo string) (*forgejo.Repo, error) {
	panic("WP6")
}

// ResolveLabels maps label names to ids via the cached label list (repo
// labels plus org labels when owner is an org). Unknown name -> error
// `unknown label "x"; known: a,b,c`.
func ResolveLabels(ctx context.Context, c *forgejo.Client, owner, repo string, names []string) ([]int64, error) {
	panic("WP6")
}

// ResolveMilestone maps a milestone title or numeric id to its id.
func ResolveMilestone(ctx context.Context, c *forgejo.Client, owner, repo, titleOrID string) (int64, error) {
	panic("WP6")
}

// NormalizeRef normalises a user-supplied branch/tag/sha reference
// (strips refs/heads/, refs/tags/ and surrounding whitespace).
func NormalizeRef(ref string) string { panic("WP6") }

// Target is the result of the default target rule of section 3.1 step 4.
type Target struct {
	Kind   string // "pr" | "branch"
	PR     int64  // set when Kind == "pr"
	Branch string
	Label  string // first output line, e.g. "target: pr #42 (branch fix/login)"
}

// ResolveTarget applies the target rule: the open PR of the current branch,
// else the branch head, else the default branch.
func ResolveTarget(ctx context.Context, c *forgejo.Client, det *gitremote.Detector, owner, repo string) (Target, error) {
	panic("WP6")
}

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

// ResolveRun resolves a RunSelector to the newest matching run.
func ResolveRun(ctx context.Context, c *forgejo.Client, owner, repo string, sel RunSelector) (RunRef, error) {
	panic("WP6")
}

// Review is the projection of a PR review.
type Review struct {
	ID          int64  `json:"id"`
	User        string `json:"-"`
	State       string `json:"state"`
	Body        string `json:"body"`
	CommitID    string `json:"commit_id"`
	Stale       bool   `json:"stale"`
	Official    bool   `json:"official"`
	Dismissed   bool   `json:"dismissed"`
	CommentsCnt int    `json:"comments_count"`
	SubmittedAt string `json:"submitted_at"`
}

// AllReviews reads `GET R/pulls/{n}/reviews` with limit=max over all pages
// until an empty page (pending reviews of other users are dropped server-side
// after counting, so pages can be short).
func AllReviews(ctx context.Context, c *forgejo.Client, owner, repo string, n int64) ([]Review, error) {
	panic("WP6")
}

// AssetPath turns a browser_download_url into the request path on the
// instance (section 5.7): only the URL path is used, minus the ROOT_URL
// sub-path. ok is false for external assets that must not be downloaded.
func AssetPath(browserURL, rootSubPath string) (path string, ok bool) { panic("WP6") }
