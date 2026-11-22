package pulls

import (
	"fmt"
	"strings"

	"github.com/AndreyQuantum/forgejo-mcp/internal/format"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools/shared"
)

// prLine renders one PR list line (section 6.9).
func prLine(p *pullReq) string {
	var b format.B
	parts := []string{
		fmt.Sprintf("#%d %s", p.Number, p.stateWord()),
		format.OneLine(p.Title, titleMax),
	}
	if a := p.author(); a != "" {
		parts = append(parts, "by "+a)
	}
	if h, ba := p.headName(), p.baseName(); h != "" || ba != "" {
		parts = append(parts, h+"->"+ba)
	}
	if p.isOpen() && !p.Mergeable {
		parts = append(parts, "conflict")
	}
	if f := p.forkRepo(); f != "" {
		parts = append(parts, "fork:"+f)
	}
	parts = append(parts, format.Tags(p.labelNames()), format.Logins(p.assigneeLogins()))
	if u := format.T(p.UpdatedAt); u != "" {
		parts = append(parts, "upd "+u)
	}
	b.Line(parts...)
	return strings.TrimRight(b.String(), "\n")
}

// fileStatus maps the API file status to the one-letter form.
func fileStatus(s string) string {
	switch s {
	case "added":
		return "A"
	case "deleted":
		return "D"
	case "renamed":
		return "R"
	case "copied":
		return "C"
	}
	return "M"
}

// fileLine renders `M src/a.go +4 -1` (`R old->new`).
func fileLine(f changedFile) string {
	st := fileStatus(f.Status)
	name := f.Filename
	if (st == "R" || st == "C") && f.PreviousFilename != "" {
		name = f.PreviousFilename + "->" + f.Filename
	}
	return fmt.Sprintf("%s %s +%d -%d", st, name, f.Additions, f.Deletions)
}

// subject is the first line of a commit message (capped).
func subject(msg string) string {
	first, _, _ := strings.Cut(strings.TrimSpace(msg), "\n")
	return format.OneLine(first, titleMax)
}

// commitLine renders `8a1b2c3d 2026-11-22 alice | subject`.
func commitLine(cm commitItem) string {
	author := cm.Commit.Author.Name
	if cm.Author != nil && cm.Author.Login != "" {
		author = cm.Author.Login
	}
	var b format.B
	b.Line(strings.TrimSpace(format.SHA(cm.SHA, 8)+" "+datePart(format.T(cm.Commit.Author.Date))+" "+author), subject(cm.Commit.Message))
	return strings.TrimRight(b.String(), "\n")
}

// datePart returns the YYYY-MM-DD part of a formatted time.
func datePart(s string) string {
	if len(s) >= 10 {
		return s[:10]
	}
	return s
}

// commentLine renders `c1234 alice 2026-11-22 10:15 | body(300)`.
func commentLine(cm issueComment) string {
	login := ""
	if cm.User != nil {
		login = cm.User.Login
	}
	var b format.B
	b.Line(strings.TrimSpace(fmt.Sprintf("c%d %s %s", cm.ID, login, format.T(cm.CreatedAt))), format.OneLine(cm.Body, commentMax))
	return strings.TrimRight(b.String(), "\n")
}

// reviewState maps the API review state to the lowercase event word.
func reviewState(s string) string {
	switch s {
	case "APPROVED":
		return "approve"
	case "REQUEST_CHANGES":
		return "request_changes"
	case "COMMENT":
		return "comment"
	case "PENDING":
		return "pending"
	}
	return strings.ToLower(s)
}

// reviewLine renders `r77 alice approve official | 8a1b2c3d | stale | ...`.
func reviewLine(r shared.Review) string {
	who := r.User
	if who == "" && r.Team != "" {
		who = "team:" + r.Team
	}
	head := fmt.Sprintf("r%d %s %s", r.ID, who, reviewState(r.State))
	if r.Official {
		head += " official"
	}
	parts := []string{head, format.SHA(r.CommitID, 8)}
	if r.Stale {
		parts = append(parts, "stale")
	}
	if r.Dismissed {
		parts = append(parts, "dismissed")
	}
	if r.CommentsCnt > 0 {
		parts = append(parts, fmt.Sprintf("c:%d", r.CommentsCnt))
	}
	parts = append(parts, format.T(r.SubmittedAt), format.OneLine(r.Body, commentMax))
	var b format.B
	b.Line(parts...)
	return strings.TrimRight(b.String(), "\n")
}

// reviewCommentLine renders `rc9 src/a.go:42 alice (resolved by bob) | body`;
// a comment on the old side reads `src/a.go:-17`. With verbose the last three
// lines of the diff hunk follow, indented.
func reviewCommentLine(rc reviewComment, verbose bool) string {
	where := rc.Path
	switch {
	case rc.Position > 0:
		where = fmt.Sprintf("%s:%d", rc.Path, rc.Position)
	case rc.OriginalPosition > 0:
		where = fmt.Sprintf("%s:-%d", rc.Path, rc.OriginalPosition)
	}
	who := ""
	if rc.User != nil {
		who = rc.User.Login
	}
	head := fmt.Sprintf("rc%d %s %s", rc.ID, where, who)
	if rc.Resolver != nil && rc.Resolver.Login != "" {
		head += " (resolved by " + rc.Resolver.Login + ")"
	}
	var b format.B
	b.Line(strings.TrimSpace(head), format.OneLine(rc.Body, commentMax))
	out := strings.TrimRight(b.String(), "\n")
	if verbose && rc.DiffHunk != "" {
		lines := strings.Split(strings.TrimRight(rc.DiffHunk, "\n"), "\n")
		if len(lines) > 3 {
			lines = lines[len(lines)-3:]
		}
		for _, l := range lines {
			out += "\n  " + l
		}
	}
	return out
}
