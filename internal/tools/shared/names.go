package shared

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"
)

const knownShown = 30

func knownList(names []string) string {
	if len(names) > knownShown {
		return strings.Join(names[:knownShown], ",") + fmt.Sprintf(" (first %d of %d)", knownShown, len(names))
	}
	return strings.Join(names, ",")
}

func repoLabels(ctx context.Context, c *forgejo.Client, owner, repo string) ([]forgejo.Label, error) {
	v, err := c.Cache().Get(c.CacheKey("labels:"+owner+"/"+repo), listTTL, func() (any, error) {
		labels, err := listAll[forgejo.Label](ctx, c, forgejo.Path("repos", owner, repo, "labels"), nil)
		if err != nil {
			return nil, err
		}
		// Org labels exist only when the owner is an organization; for a user
		// the endpoint answers 404 and that is not an error.
		orgLabels, err := listAll[forgejo.Label](ctx, c, forgejo.Path("orgs", owner, "labels"), nil)
		var ae *forgejo.APIError
		switch {
		case err == nil:
			labels = append(labels, orgLabels...)
		case errors.As(err, &ae) && (ae.Status == 404 || ae.Status == 403):
		default:
			return nil, err
		}
		return labels, nil
	})
	if err != nil {
		return nil, err
	}
	return v.([]forgejo.Label), nil
}

// ResolveLabels maps label names to ids via the cached label list (repo
// labels plus org labels when owner is an org). Unknown name -> error
// `unknown label "x"; known: a,b,c`.
func ResolveLabels(ctx context.Context, c *forgejo.Client, owner, repo string, names []string) ([]int64, error) {
	if err := checkOwnerRepo(owner, repo); err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(names))
	if len(names) == 0 {
		return ids, nil
	}
	labels, err := repoLabels(ctx, c, owner, repo)
	if err != nil {
		return nil, err
	}
	for _, want := range names {
		want = strings.TrimSpace(want)
		var id int64
		for _, l := range labels {
			if l.Name == want {
				id = l.ID
				break
			}
		}
		if id == 0 {
			for _, l := range labels {
				if strings.EqualFold(l.Name, want) {
					id = l.ID
					break
				}
			}
		}
		if id == 0 {
			known := make([]string, len(labels))
			for i, l := range labels {
				known[i] = l.Name
			}
			return nil, fmt.Errorf("unknown label %q; known: %s", want, knownList(known))
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// ResolveMilestone maps a milestone title or numeric id to its id.
func ResolveMilestone(ctx context.Context, c *forgejo.Client, owner, repo, titleOrID string) (int64, error) {
	if err := checkOwnerRepo(owner, repo); err != nil {
		return 0, err
	}
	want := strings.TrimSpace(titleOrID)
	v, err := c.Cache().Get(c.CacheKey("milestones:"+owner+"/"+repo), listTTL, func() (any, error) {
		return listAll[forgejo.Milestone](ctx, c, forgejo.Path("repos", owner, repo, "milestones"),
			url.Values{"state": {"all"}})
	})
	if err != nil {
		return 0, err
	}
	ms := v.([]forgejo.Milestone)
	if n, perr := strconv.ParseInt(want, 10, 64); perr == nil {
		for _, m := range ms {
			if m.ID == n {
				return m.ID, nil
			}
		}
	}
	for _, m := range ms {
		if m.Title == want {
			return m.ID, nil
		}
	}
	for _, m := range ms {
		if strings.EqualFold(m.Title, want) {
			return m.ID, nil
		}
	}
	known := make([]string, len(ms))
	for i, m := range ms {
		known[i] = m.Title
	}
	return 0, fmt.Errorf("unknown milestone %q; known: %s", want, knownList(known))
}

// InvalidateLabels drops the cached label list (call after a label mutation).
func InvalidateLabels(c *forgejo.Client, owner, repo string) {
	c.Cache().Invalidate(c.CacheKey("labels:" + owner + "/" + repo))
}

// InvalidateMilestones drops the cached milestone list (call after a mutation).
func InvalidateMilestones(c *forgejo.Client, owner, repo string) {
	c.Cache().Invalidate(c.CacheKey("milestones:" + owner + "/" + repo))
}
