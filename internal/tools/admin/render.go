package admin

import (
	"fmt"
	"strings"

	"github.com/AndreyQuantum/forgejo-mcp/internal/format"
)

// userLine renders `alice | Alice A | alice@x.org | admin | inactive | restricted | login-prohibited`.
func userLine(u *user) string {
	var flags []string
	if u.IsAdmin {
		flags = append(flags, "admin")
	}
	if !u.Active {
		flags = append(flags, "inactive")
	}
	if u.Restricted {
		flags = append(flags, "restricted")
	}
	if u.ProhibitLogin {
		flags = append(flags, "login-prohibited")
	}
	var b format.B
	b.Line(append([]string{u.Login, u.FullName, u.Email}, flags...)...)
	return strings.TrimRight(b.String(), "\n")
}

// orgLine renders `org | visibility | desc(80)`.
func orgLine(o *org) string {
	var b format.B
	b.Line(o.Name, o.Visibility, format.OneLine(o.Description, 80))
	return strings.TrimRight(b.String(), "\n")
}

// emailLine renders `a@x.org | alice | primary | verified`.
func emailLine(e *email) string {
	var flags []string
	if e.Primary {
		flags = append(flags, "primary")
	}
	if e.Verified {
		flags = append(flags, "verified")
	}
	var b format.B
	b.Line(append([]string{e.Email, e.UserName}, flags...)...)
	return strings.TrimRight(b.String(), "\n")
}

// cronLine renders `name | schedule | next T | runs:n`.
func cronLine(c *cron) string {
	var b format.B
	next := ""
	if !strings.HasPrefix(c.Next, "0001") {
		if t := format.T(c.Next); t != "" {
			next = "next " + t
		}
	}
	b.Line(c.Name, c.Schedule, next, fmt.Sprintf("runs:%d", c.ExecTimes))
	return strings.TrimRight(b.String(), "\n")
}

// limitText renders a quota limit; a negative limit means unlimited.
func limitText(n int64) string {
	if n < 0 {
		return "unlimited"
	}
	return format.Size(n)
}

// ruleLine renders `name | limit 1.0GB | size:repos:all,size:git:lfs`.
func ruleLine(r *quotaRule) string {
	var b format.B
	b.Line(r.Name, "limit "+limitText(r.Limit), strings.Join(r.Subjects, ","))
	return strings.TrimRight(b.String(), "\n")
}

// groupLine renders `name | rules: a,b`.
func groupLine(g *quotaGroup) string {
	names := make([]string, 0, len(g.Rules))
	for _, r := range g.Rules {
		names = append(names, r.Name)
	}
	var b format.B
	rules := ""
	if len(names) > 0 {
		rules = "rules: " + strings.Join(names, ",")
	}
	b.Line(g.Name, rules)
	return strings.TrimRight(b.String(), "\n")
}

// renderGroup renders one quota group with its rules.
func renderGroup(g *quotaGroup) string {
	var b format.B
	b.Raw("group " + g.Name + "\n")
	for i := range g.Rules {
		b.Raw("  rule " + ruleLine(&g.Rules[i]) + "\n")
	}
	return b.String()
}

// renderQuota renders the usage of one quota subject.
func renderQuota(label string, q *quotaInfo) string {
	var b format.B
	s := q.Used.Size
	b.Raw("quota " + label + "\n")
	b.Line("used: repos public "+format.Size(s.Repos.Public)+" private "+format.Size(s.Repos.Private),
		"lfs "+format.Size(s.Git.LFS),
		"attachments issues "+format.Size(s.Assets.Attachments.Issues)+" releases "+format.Size(s.Assets.Attachments.Releases),
		"artifacts "+format.Size(s.Assets.Artifacts),
		"packages "+format.Size(s.Assets.Packages.All))
	if len(q.Groups) == 0 {
		b.Raw("groups: none\n")
	}
	for i := range q.Groups {
		b.Raw(renderGroup(&q.Groups[i]))
	}
	return b.String()
}
