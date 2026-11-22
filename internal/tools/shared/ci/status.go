package ci

import (
	"context"
	"net/url"
	"strconv"

	"github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"
)

// Check is one failing commit status.
type Check struct {
	Context     string
	State       string
	Description string
	TargetURL   string
}

// Combined is the combined commit status over all pages.
type Combined struct {
	State   string // success|warning|pending|failure|error|none
	Total   int
	OK      int
	Failing []Check
	Pending []string
}

const maxStatusPages = 5

// CombinedStatus reads `GET R/commits/{ref}/status?limit=<server max>`, fetches
// further pages (at most 5) when a page is full and recomputes the state
// client-side as the worst one (failure|error > pending > warning > success,
// latest status per context wins).
func CombinedStatus(ctx context.Context, c *forgejo.Client, owner, repo, ref string) (*Combined, error) {
	_, limit := c.Limits(ctx)
	path := forgejo.Path("repos", owner, repo, "commits", ref, "status")
	var order []string
	latest := map[string]forgejo.Status{}
	for page := 1; page <= maxStatusPages; page++ {
		var body struct {
			Statuses []forgejo.Status `json:"statuses"`
		}
		q := url.Values{"limit": {strconv.Itoa(limit)}, "page": {strconv.Itoa(page)}}
		if _, err := c.GetJSON(ctx, path, q, &body); err != nil {
			return nil, err
		}
		for _, s := range body.Statuses {
			cur, seen := latest[s.Context]
			if !seen {
				order = append(order, s.Context)
				latest[s.Context] = s
				continue
			}
			if s.ID > cur.ID || (s.ID == cur.ID && s.CreatedAt >= cur.CreatedAt) {
				latest[s.Context] = s
			}
		}
		if len(body.Statuses) < limit {
			break
		}
	}

	out := &Combined{State: "none", Total: len(order)}
	hasFailure, hasError, hasPending, hasWarning := false, false, false, false
	for _, name := range order {
		s := latest[name]
		switch s.Status {
		case "failure":
			hasFailure = true
			out.Failing = append(out.Failing, Check{Context: name, State: s.Status, Description: s.Description, TargetURL: s.TargetURL})
		case "error":
			hasError = true
			out.Failing = append(out.Failing, Check{Context: name, State: s.Status, Description: s.Description, TargetURL: s.TargetURL})
		case "pending":
			hasPending = true
			out.Pending = append(out.Pending, name)
		case "warning":
			hasWarning = true
			out.OK++
		default: // success, skipped
			out.OK++
		}
	}
	switch {
	case out.Total == 0:
	case hasFailure:
		out.State = "failure"
	case hasError:
		out.State = "error"
	case hasPending:
		out.State = "pending"
	case hasWarning:
		out.State = "warning"
	default:
		out.State = "success"
	}
	return out, nil
}
