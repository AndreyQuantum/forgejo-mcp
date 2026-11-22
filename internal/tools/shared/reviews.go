package shared

import (
	"context"
	"encoding/json"

	"github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"
)

// Review is the projection of a PR review.
type Review struct {
	ID          int64  `json:"id"`
	User        string `json:"-"`
	Team        string `json:"-"` // team.name of a team review request
	State       string `json:"state"`
	Body        string `json:"body"`
	CommitID    string `json:"commit_id"`
	Stale       bool   `json:"stale"`
	Official    bool   `json:"official"`
	Dismissed   bool   `json:"dismissed"`
	CommentsCnt int    `json:"comments_count"`
	SubmittedAt string `json:"submitted_at"`
}

// UnmarshalJSON fills User from the nested `user.login`.
func (r *Review) UnmarshalJSON(b []byte) error {
	type plain Review
	aux := struct {
		*plain
		User *struct {
			Login string `json:"login"`
		} `json:"user"`
		Team *struct {
			Name string `json:"name"`
		} `json:"team"`
	}{plain: (*plain)(r)}
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	if aux.User != nil {
		r.User = aux.User.Login
	}
	if aux.Team != nil {
		r.Team = aux.Team.Name
	}
	return nil
}

// AllReviews reads `GET R/pulls/{n}/reviews` with limit=max over all pages
// until an empty page (pending reviews of other users are dropped server-side
// after counting, so pages can be short).
func AllReviews(ctx context.Context, c *forgejo.Client, owner, repo string, n int64) ([]Review, error) {
	if err := checkOwnerRepo(owner, repo); err != nil {
		return nil, err
	}
	p := repoPath(owner, repo) + "/pulls/" + itoa(n) + "/reviews"
	var all []Review
	for page := 1; page <= maxPages; page++ {
		var items []Review
		if _, err := c.List(ctx, p, nil, page, 1<<20, &items); err != nil {
			return nil, err
		}
		if len(items) == 0 {
			break
		}
		all = append(all, items...)
	}
	return all, nil
}
