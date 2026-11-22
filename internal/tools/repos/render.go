package repos

import (
	"fmt"
	"sort"
	"strings"

	"github.com/AndreyQuantum/forgejo-mcp/internal/format"
)

func visibility(private bool) string {
	if private {
		return "private"
	}
	return "public"
}

func repoFlags(r *apiRepo) string {
	var f []string
	if r.Fork {
		f = append(f, "fork")
	}
	if r.Mirror {
		f = append(f, "mirror")
	}
	if r.Template {
		f = append(f, "template")
	}
	if r.Archived {
		f = append(f, "archived")
	}
	if r.Empty {
		f = append(f, "empty")
	}
	return strings.Join(f, ",")
}

// repoLine renders one list line of a repository (6.9).
func repoLine(r *apiRepo) string {
	var b format.B
	stars := ""
	if r.StarsCount > 0 {
		stars = fmt.Sprintf("stars:%d", r.StarsCount)
	}
	upd := ""
	if r.UpdatedAt != "" {
		upd = "upd " + format.T(r.UpdatedAt)
	}
	b.Line(r.FullName, visibility(r.Private), repoFlags(r), stars, upd, format.OneLine(r.Description, 80))
	return b.String()
}

func repoLines(rs []apiRepo) string {
	var b strings.Builder
	for i := range rs {
		b.WriteString(repoLine(&rs[i]))
	}
	return b.String()
}

func permText(p *perms) string {
	if p == nil {
		return ""
	}
	var out []string
	if p.Admin {
		out = append(out, "admin")
	}
	if p.Push {
		out = append(out, "push")
	}
	if p.Pull {
		out = append(out, "pull")
	}
	return strings.Join(out, ",")
}

func features(r *apiRepo) string {
	var out []string
	for _, f := range []struct {
		on   bool
		name string
	}{
		{r.HasIssues, "issues"},
		{r.HasPulls, "pulls"},
		{r.HasWiki, "wiki"},
		{r.HasActions, "actions"},
		{r.HasReleases, "releases"},
		{r.HasPackages, "packages"},
		{r.HasProjects, "projects"},
	} {
		if f.on {
			out = append(out, f.name)
		}
	}
	return strings.Join(out, ",")
}

// renderRepo renders the single-object view; verbose adds the second tier.
func renderRepo(r *apiRepo, verbose bool) string {
	var b format.B
	b.Raw("repo " + r.FullName + "\n")
	b.KV("visibility", visibility(r.Private))
	b.KV("flags", repoFlags(r))
	b.KV("description", format.OneLine(r.Description, 300))
	b.KV("website", r.Website)
	b.KV("default_branch", r.DefaultBranch)
	b.KV("language", r.Language)
	if r.Size > 0 {
		b.KV("size", format.Size(r.Size*1024))
	}
	b.KV("topics", strings.Join(r.Topics, ","))
	counts := []string{
		fmt.Sprintf("stars:%d", r.StarsCount), fmt.Sprintf("forks:%d", r.ForksCount),
		fmt.Sprintf("issues:%d", r.OpenIssues), fmt.Sprintf("prs:%d", r.OpenPRs),
	}
	b.KV("counts", strings.Join(counts, " "))
	b.KV("perms", permText(r.Permissions))
	b.KV("updated", format.T(r.UpdatedAt))
	b.KV("url", r.HTMLURL)
	if !verbose {
		return b.String()
	}
	if r.Parent != nil {
		b.KV("parent", r.Parent.FullName)
	}
	b.KV("features", features(r))
	b.KV("clone_url", r.CloneURL)
	b.KV("ssh_url", r.SSHURL)
	var styles []string
	for _, s := range []struct {
		on   bool
		name string
	}{
		{r.AllowMerge, "merge"},
		{r.AllowRebase, "rebase"},
		{r.AllowRebaseExp, "rebase-merge"},
		{r.AllowSquash, "squash"},
		{r.AllowFFOnly, "fast-forward-only"},
	} {
		if s.on {
			styles = append(styles, s.name)
		}
	}
	b.KV("merge_styles", strings.Join(styles, ","))
	b.KV("default_merge_style", r.DefaultMerge)
	b.KV("default_update_style", r.DefaultUpdate)
	if r.DeleteBranchPR {
		b.KV("delete_branch_after_merge", "default")
	}
	b.KV("mirror_interval", r.MirrorInterval)
	b.KV("mirror_updated", format.T(r.MirrorUpdated))
	return b.String()
}

func shortDate(ts string) string {
	t := format.T(ts)
	if len(t) >= 10 {
		return t[:10]
	}
	return t
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	return s
}

// branchLine renders one branch list line.
func branchLine(br *apiBranch) string {
	var b format.B
	prot := ""
	if br.Protected {
		prot = "protected"
	}
	b.Line(br.Name, format.SHA(br.Commit.ID, 8), shortDate(br.Commit.Timestamp), prot,
		format.OneLine(firstLine(br.Commit.Message), 80))
	return b.String()
}

func renderBranch(br *apiBranch) string {
	var b format.B
	b.Raw("branch " + br.Name + "\n")
	b.KV("commit", strings.TrimSpace(format.SHA(br.Commit.ID, 8)+" "+format.OneLine(firstLine(br.Commit.Message), 120)))
	b.KV("date", format.T(br.Commit.Timestamp))
	if br.Protected {
		b.KV("protected", "yes")
	}
	var can []string
	if br.UserCanPush {
		can = append(can, "push")
	}
	if br.UserCanMerge {
		can = append(can, "merge")
	}
	b.KV("you_can", strings.Join(can, ","))
	b.KV("required_approvals", br.RequiredApprovals)
	if br.EnableStatusCheck || len(br.StatusContexts) > 0 {
		b.KV("status_checks", strings.Join(br.StatusContexts, ","))
	}
	return b.String()
}

// tagLine renders one tag list line.
func tagLine(t *apiTag) string {
	var b format.B
	b.Line(t.Name, format.SHA(t.Commit.SHA, 8), format.OneLine(firstLine(t.Message), 80))
	return b.String()
}

// renderTag renders a tag; withMessage=false omits the message line (the
// verbose view prints the full annotation instead).
func renderTag(t *apiTag, annotated, withMessage bool) string {
	var b format.B
	b.Raw("tag " + t.Name + "\n")
	b.KV("commit", format.SHA(t.Commit.SHA, 8))
	if annotated {
		b.KV("type", "annotated")
		b.KV("object", format.SHA(t.ID, 8))
	} else {
		b.KV("type", "lightweight")
	}
	if withMessage {
		b.KV("message", format.OneLine(firstLine(t.Message), 200))
	}
	return b.String()
}

func tagProtectionLine(p *tagProtection) string {
	var b format.B
	var wl []string
	if len(p.Usernames) > 0 {
		wl = append(wl, "users:"+strings.Join(p.Usernames, ","))
	}
	if len(p.Teams) > 0 {
		wl = append(wl, "teams:"+strings.Join(p.Teams, ","))
	}
	b.Line(fmt.Sprintf("id %d", p.ID), p.Pattern, strings.Join(wl, " "))
	return b.String()
}

// protectKeys is the fixed order of branch-protection keys in protect_get.
var protectKeys = []string{
	"enable_push", "enable_push_whitelist", "push_whitelist_usernames", "push_whitelist_teams", "push_whitelist_deploy_keys",
	"enable_merge_whitelist", "merge_whitelist_usernames", "merge_whitelist_teams",
	"enable_status_check", "status_check_contexts", "required_approvals",
	"enable_approvals_whitelist", "approvals_whitelist_username", "approvals_whitelist_teams",
	"block_on_rejected_reviews", "block_on_official_review_requests", "block_on_outdated_branch",
	"dismiss_stale_approvals", "ignore_stale_approvals", "require_signed_commits",
	"protected_file_patterns", "unprotected_file_patterns", "apply_to_admins",
}

func ruleName(m map[string]any) string {
	if s, ok := m["rule_name"].(string); ok && s != "" {
		return s
	}
	s, _ := m["branch_name"].(string)
	return s
}

func asStrings(v any) []string {
	arr, _ := v.([]any)
	out := make([]string, 0, len(arr))
	for _, x := range arr {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func asInt(v any) int {
	f, _ := v.(float64)
	return int(f)
}

func isDefaultVal(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case bool:
		return !x
	case float64:
		return x == 0
	case string:
		return x == ""
	case []any:
		return len(x) == 0
	}
	return false
}

func valText(v any) string {
	switch x := v.(type) {
	case bool:
		return "true"
	case float64:
		return fmt.Sprint(int64(x))
	case string:
		return x
	case []any:
		return strings.Join(asStrings(x), ",")
	}
	return fmt.Sprint(v)
}

// protectionLine is the one-line summary of protect_list (non-default flags only).
func protectionLine(m map[string]any) string {
	var b format.B
	parts := []string{"rule " + ruleName(m)}
	if n := asInt(m["required_approvals"]); n > 0 {
		parts = append(parts, fmt.Sprintf("approvals:%d", n))
	}
	if ctx := asStrings(m["status_check_contexts"]); len(ctx) > 0 {
		parts = append(parts, "checks:"+strings.Join(ctx, ","))
	} else if on, _ := m["enable_status_check"].(bool); on {
		parts = append(parts, "checks:any")
	}
	if on, _ := m["enable_push"].(bool); on {
		if on2, _ := m["enable_push_whitelist"].(bool); on2 {
			n := len(asStrings(m["push_whitelist_usernames"])) + len(asStrings(m["push_whitelist_teams"]))
			parts = append(parts, fmt.Sprintf("push-whitelist:%d", n))
		} else {
			parts = append(parts, "push:all")
		}
	}
	if on, _ := m["enable_merge_whitelist"].(bool); on {
		n := len(asStrings(m["merge_whitelist_usernames"])) + len(asStrings(m["merge_whitelist_teams"]))
		parts = append(parts, fmt.Sprintf("merge-whitelist:%d", n))
	}
	if on, _ := m["enable_approvals_whitelist"].(bool); on {
		n := len(asStrings(m["approvals_whitelist_username"])) + len(asStrings(m["approvals_whitelist_teams"]))
		parts = append(parts, fmt.Sprintf("approvals-whitelist:%d", n))
	}
	for _, k := range []string{
		"block_on_rejected_reviews", "block_on_official_review_requests", "block_on_outdated_branch",
		"dismiss_stale_approvals", "ignore_stale_approvals", "require_signed_commits", "apply_to_admins",
	} {
		if on, _ := m[k].(bool); on {
			parts = append(parts, k)
		}
	}
	b.Line(parts...)
	return b.String()
}

// renderProtection prints non-default keys in a fixed order.
func renderProtection(m map[string]any) string {
	var b format.B
	b.Raw("protection " + ruleName(m) + "\n")
	for _, k := range protectKeys {
		v, ok := m[k]
		if !ok || isDefaultVal(v) {
			continue
		}
		b.KV(k, valText(v))
	}
	return b.String()
}

func wikiMetaLine(w *wikiMeta) string {
	var b format.B
	upd := ""
	if w.LastCommit != nil && w.LastCommit.Committer != nil && w.LastCommit.Committer.Date != "" {
		upd = "upd " + format.T(w.LastCommit.Committer.Date)
	}
	b.Line(w.Title, w.SubURL, upd)
	return b.String()
}

func pushMirrorLine(m *pushMirror) string {
	parts := []string{m.RemoteName + " -> " + m.RemoteAddr}
	if m.Interval != "" && m.Interval != "0s" && m.Interval != "0" {
		parts = append(parts, "every "+m.Interval)
	}
	if m.SyncOnCommit {
		parts = append(parts, "on-commit")
	}
	if m.BranchFilter != "" {
		parts = append(parts, "branches:"+m.BranchFilter)
	}
	if m.LastUpdate != "" {
		parts = append(parts, "last "+format.T(m.LastUpdate))
	}
	if m.LastError != "" {
		parts = append(parts, "err:"+format.OneLine(m.LastError, 120))
	}
	return strings.Join(parts, " ") + "\n"
}

// languageText renders `Go 81.2% (123.0KB) | JS 18.8% (12.0KB)`.
func languageText(langs map[string]int64) string {
	type kv struct {
		name string
		n    int64
	}
	var total int64
	items := make([]kv, 0, len(langs))
	for k, v := range langs {
		items = append(items, kv{k, v})
		total += v
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].n != items[j].n {
			return items[i].n > items[j].n
		}
		return items[i].name < items[j].name
	})
	out := make([]string, 0, len(items))
	for _, it := range items {
		pct := 0.0
		if total > 0 {
			pct = float64(it.n) * 100 / float64(total)
		}
		out = append(out, fmt.Sprintf("%s %.1f%% (%s)", it.name, pct, format.Size(it.n)))
	}
	return strings.Join(out, " | ")
}
