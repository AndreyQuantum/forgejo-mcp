package issues

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"
	"github.com/AndreyQuantum/forgejo-mcp/internal/format"
)

func labelNames(ls []forgejo.Label) []string {
	out := make([]string, 0, len(ls))
	for _, l := range ls {
		out = append(out, l.Name)
	}
	return out
}

func assigneeLogins(as []login) []string {
	out := make([]string, 0, len(as))
	for _, a := range as {
		out = append(out, a.Login)
	}
	return out
}

func kindWord(i *issue) string {
	if i.isPR() {
		return "pr"
	}
	return "issue"
}

func stateWord(i *issue) string {
	if i.PullRequest != nil && i.PullRequest.Merged {
		return "merged"
	}
	return i.State
}

// issueLine renders one list line (6.9). search lines carry owner/repo#N.
func issueLine(i *issue, search bool) string {
	var b format.B
	id := "#" + strconv.FormatInt(i.Number, 10)
	if search && i.Repository != nil && i.Repository.FullName != "" {
		id = i.Repository.FullName + id
	}
	head := id + " " + stateWord(i) + " " + kindWord(i)
	if i.PullRequest != nil && i.PullRequest.Draft {
		head += " draft"
	}
	ms := ""
	if i.Milestone != nil {
		ms = "ms:" + i.Milestone.Title
	}
	by := ""
	if a := i.author(); a != "" {
		by = "by " + a
	}
	cc := ""
	if i.Comments > 0 {
		cc = "c:" + strconv.Itoa(i.Comments)
	}
	upd := ""
	if t := format.T(i.UpdatedAt); t != "" {
		upd = "upd " + t
	}
	b.Line(head, format.OneLine(i.Title, titleMax), by, format.Tags(labelNames(i.Labels)),
		format.Logins(assigneeLogins(i.Assignees)), ms, cc, upd)
	return b.String()
}

func assetLine(a asset) string {
	return fmt.Sprintf("%d:%s(%s)", a.ID, a.Name, format.Size(a.Size))
}

// renderIssue renders the single-object view (6.3).
func renderIssue(i *issue, verbose bool, body string) string {
	var b format.B
	b.Raw(fmt.Sprintf("%s #%d\n", kindWord(i), i.Number))
	state := stateWord(i)
	if i.PullRequest != nil && i.PullRequest.Draft {
		state += " draft"
	}
	b.KV("state", state)
	b.KV("title", format.OneLine(i.Title, titleMax))
	b.KV("by", i.author())
	b.KV("labels", format.Tags(labelNames(i.Labels)))
	b.KV("assignees", format.Logins(assigneeLogins(i.Assignees)))
	if i.Milestone != nil {
		ms := i.Milestone.Title
		if i.Milestone.State != "" && i.Milestone.State != "open" {
			ms += " (" + i.Milestone.State + ")"
		}
		b.KV("milestone", ms)
	}
	b.KV("due", datePart(i.DueDate))
	if i.Comments > 0 {
		b.KV("comments", i.Comments)
	}
	if i.IsLocked {
		b.KV("locked", "yes")
	}
	b.KV("ref", i.Ref)
	b.KV("created", format.T(i.CreatedAt))
	b.KV("updated", format.T(i.UpdatedAt))
	b.KV("closed", format.T(i.ClosedAt))
	b.KV("url", i.HTMLURL)
	if len(i.Assets) > 0 {
		parts := make([]string, len(i.Assets))
		for k, a := range i.Assets {
			parts[k] = assetLine(a)
		}
		b.KV("attachments", strings.Join(parts, ", "))
	}
	if verbose {
		b.KV("id", i.ID)
		if i.Repository != nil {
			b.KV("repo", i.Repository.FullName)
		}
		if len(i.Labels) > 0 {
			parts := make([]string, len(i.Labels))
			for k, l := range i.Labels {
				parts[k] = fmt.Sprintf("%d:%s #%s", l.ID, l.Name, l.Color)
			}
			b.KV("label_ids", strings.Join(parts, ", "))
		}
		if i.Milestone != nil {
			b.KV("milestone_id", i.Milestone.ID)
		}
	}
	b.KV("body", body)
	return b.String()
}

// commentLine renders `c1234 alice 2026-11-22 10:15 | body`.
func commentLine(cm *comment, max int) string {
	var b format.B
	head := fmt.Sprintf("c%d", cm.ID)
	if a := cm.author(); a != "" {
		head += " " + a
	}
	if t := format.T(cm.CreatedAt); t != "" {
		head += " " + t
	}
	b.Line(head, format.OneLine(cm.Body, max))
	return b.String()
}

// feedLine renders `c1234 #12 alice T | body` for the repo-wide feed.
func feedLine(cm *comment, max int) string {
	var b format.B
	head := fmt.Sprintf("c%d", cm.ID)
	if n := issueNumberFromURL(cm.IssueURL); n > 0 {
		head += fmt.Sprintf(" #%d", n)
	}
	if a := cm.author(); a != "" {
		head += " " + a
	}
	if t := format.T(cm.CreatedAt); t != "" {
		head += " " + t
	}
	b.Line(head, format.OneLine(cm.Body, max))
	return b.String()
}

// issueNumberFromURL extracts N from `.../issues/N`.
func issueNumberFromURL(u string) int64 {
	u = strings.TrimRight(u, "/")
	if k := strings.LastIndex(u, "/"); k >= 0 {
		if n, err := strconv.ParseInt(u[k+1:], 10, 64); err == nil {
			return n
		}
	}
	return 0
}

func refText(self string, r *timelineRef) string {
	if r == nil {
		return ""
	}
	if r.Repository != nil && r.Repository.FullName != "" && !strings.EqualFold(r.Repository.FullName, self) {
		return fmt.Sprintf("%s#%d", r.Repository.FullName, r.Number)
	}
	return fmt.Sprintf("#%d", r.Number)
}

// eventLine renders one timeline event (6.9): `e<id> T alice close`.
func eventLine(self string, e *timelineEvent) string {
	who := ""
	if e.User != nil {
		who = e.User.Login
	}
	head := strings.TrimSpace(fmt.Sprintf("e%d %s %s", e.ID, format.T(e.CreatedAt), who))
	var what string
	switch e.Type {
	case "comment":
		what = "comment: " + format.OneLine(e.Body, commentMax)
	case "close", "reopen", "lock", "unlock", "merge_pull", "delete_branch":
		what = strings.TrimSuffix(e.Type, "_pull")
	case "label":
		name := ""
		if e.Label != nil {
			name = e.Label.Name
		}
		sign := "-"
		if e.Body != "" {
			sign = "+"
		}
		what = "label " + sign + name
	case "milestone":
		switch {
		case e.Milestone != nil:
			what = "milestone " + e.Milestone.Title
		case e.OldMilestone != nil:
			what = "milestone -" + e.OldMilestone.Title
		default:
			what = "milestone"
		}
	case "assignees":
		name := ""
		if e.Assignee != nil {
			name = e.Assignee.Login
		}
		if e.RemovedAssignee {
			what = "unassign @" + name
		} else {
			what = "assign @" + name
		}
	case "change_title":
		what = fmt.Sprintf("title %q->%q", e.OldTitle, e.NewTitle)
	case "change_target_branch":
		what = fmt.Sprintf("target %s->%s", e.OldRef, e.NewRef)
	case "issue_ref", "pull_ref", "comment_ref":
		what = "ref " + refText(self, e.RefIssue)
	case "commit_ref":
		what = "ref commit " + format.SHA(e.RefCommitSHA, 8)
	case "review":
		what = fmt.Sprintf("review #%d", e.ReviewID)
	case "review_request":
		what = "review_request"
		if e.Assignee != nil {
			what += " @" + e.Assignee.Login
		}
	case "pull_push":
		what = "push"
	case "add_dependency", "remove_dependency":
		what = strings.Replace(e.Type, "_dependency", " dep", 1) + " " + refText(self, e.DependentIssue)
	case "add_time_manual", "stop_tracking":
		what = e.Type
		if e.TrackedTime != nil {
			what += " " + format.Dur(durSeconds(e.TrackedTime.Time))
		}
	default:
		what = e.Type
		if e.Body != "" {
			what += ": " + format.OneLine(e.Body, 120)
		}
	}
	return head + " " + what + "\n"
}
