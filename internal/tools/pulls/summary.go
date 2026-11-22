package pulls

import (
	"fmt"
	"sort"
	"strings"

	"github.com/AndreyQuantum/forgejo-mcp/internal/tools/shared"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools/shared/ci"
)

// reviewSummary is the latest verdict per reviewer plus pending requests.
type reviewSummary struct {
	line      string   // `reviews: alice approve | bob request_changes | pending: carol`
	approvals int      // official, non-dismissed approvals
	changes   []string // reviewers whose latest decision is request_changes
}

// summarizeReviews reduces all reviews of a PR to the latest decision per
// reviewer (approve / request_changes, dismissed ones skipped). Comment-only
// reviewers are listed as `comment`; requested reviewers (users from the PR
// object, teams from REQUEST_REVIEW rows) end up under `pending:`.
func summarizeReviews(rs []shared.Review, requested []string) reviewSummary {
	sorted := append([]shared.Review(nil), rs...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })

	var order []string
	decision := map[string]shared.Review{}
	var commenters []string
	seenComment := map[string]bool{}
	var teams []string
	for _, r := range sorted {
		switch r.State {
		case "REQUEST_REVIEW":
			if r.User == "" && r.Team != "" {
				teams = appendUnique(teams, "team:"+r.Team)
			}
		case "APPROVED", "REQUEST_CHANGES":
			if r.Dismissed || r.User == "" {
				continue
			}
			if _, ok := decision[r.User]; !ok {
				order = append(order, r.User)
			}
			decision[r.User] = r
		case "COMMENT":
			if r.User != "" && !seenComment[r.User] {
				seenComment[r.User] = true
				commenters = append(commenters, r.User)
			}
		}
	}

	var sum reviewSummary
	var parts []string
	for _, u := range order {
		r := decision[u]
		s := u + " " + reviewState(r.State)
		if r.Stale {
			s += " (stale)"
		}
		parts = append(parts, s)
		switch r.State {
		case "APPROVED":
			if r.Official {
				sum.approvals++
			}
		case "REQUEST_CHANGES":
			sum.changes = append(sum.changes, u)
		}
	}
	for _, u := range commenters {
		if _, ok := decision[u]; !ok {
			parts = append(parts, u+" comment")
		}
	}
	var pending []string
	for _, u := range requested {
		pending = appendUnique(pending, u)
	}
	for _, tm := range teams {
		pending = appendUnique(pending, tm)
	}
	if len(pending) > 0 {
		parts = append(parts, "pending: "+strings.Join(pending, ","))
	}
	if len(parts) == 0 {
		sum.line = "reviews: none"
	} else {
		sum.line = "reviews: " + strings.Join(parts, " | ")
	}
	return sum
}

func appendUnique(xs []string, x string) []string {
	for _, y := range xs {
		if y == x {
			return xs
		}
	}
	return append(xs, x)
}

// ciText renders the `ci:` line of `pr get`.
func ciText(cs *ci.Combined) string {
	switch cs.State {
	case "none":
		return "ci: none"
	}
	s := fmt.Sprintf("ci: %s %d/%d", cs.State, cs.OK, cs.Total)
	if len(cs.Failing) > 0 {
		var fs []string
		for i, f := range cs.Failing {
			if i == 5 {
				fs = append(fs, fmt.Sprintf("+%d more", len(cs.Failing)-5))
				break
			}
			e := f.Context
			if f.Description != "" {
				e += fmt.Sprintf(" %q", f.Description)
			}
			fs = append(fs, e)
		}
		s += " | failing: " + strings.Join(fs, ", ")
	}
	if len(cs.Pending) > 0 {
		pend := cs.Pending
		more := ""
		if len(pend) > 5 {
			more = fmt.Sprintf(",+%d more", len(pend)-5)
			pend = pend[:5]
		}
		s += " | pending: " + strings.Join(pend, ",") + more
	}
	return s
}
