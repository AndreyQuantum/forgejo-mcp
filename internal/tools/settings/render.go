package settings

import (
	"slices"
	"strconv"
	"strings"

	"github.com/AndreyQuantum/forgejo-mcp/internal/format"
)

// hookState is "active" or "inactive".
func hookState(h hookObj) string {
	if h.Active {
		return "active"
	}
	return "inactive"
}

// hookLine renders one list line: `id 2 | forgejo | active | push,pull_request | url`.
// Secrets and authorization headers are never printed.
func hookLine(h hookObj) string {
	var b format.B
	b.Line(
		"id "+strconv.FormatInt(h.ID, 10),
		h.Type,
		hookState(h),
		strings.Join(h.Events, ","),
		safeHookURL(h.Type, h.hookURL()),
	)
	return strings.TrimRight(b.String(), "\n")
}

// hookObject renders the single-object view of `hook get`.
func hookObject(h hookObj, label string) string {
	var b format.B
	b.Raw("hook " + strconv.FormatInt(h.ID, 10) + "\n")
	b.KV("scope", label)
	b.KV("type", h.Type)
	b.KV("state", hookState(h))
	b.KV("events", strings.Join(h.Events, ","))
	b.KV("branch_filter", h.BranchFilter)
	b.KV("url", safeHookURL(h.Type, h.hookURL()))
	b.KV("content_type", h.hookContentType())
	if h.AuthorizationHeader != "" {
		b.KV("authorization_header", "set (hidden)")
	}
	b.KV("created", format.T(h.CreatedAt))
	b.KV("updated", format.T(h.UpdatedAt))
	return strings.TrimRight(b.String(), "\n")
}

// keyLine renders one deploy key: `id 4 | title | SHA256:... | ro`.
func keyLine(k deployKey) string {
	var b format.B
	ro := ""
	if k.ReadOnly {
		ro = "ro"
	}
	b.Line("id "+strconv.FormatInt(k.ID, 10), format.OneLine(k.Title, 120), k.Fingerprint, ro)
	return strings.TrimRight(b.String(), "\n")
}

// tokenHookTypes carry bot tokens or webhook secrets in the URL path.
var tokenHookTypes = []string{"slack", "discord", "telegram", "msteams", "feishu", "dingtalk", "wechatwork"}

// safeHookURL hides credentials in a target URL: userinfo, query values and,
// for chat integrations whose path holds a token, the whole path.
func safeHookURL(typ, raw string) string {
	scheme, rest, found := strings.Cut(raw, "://")
	if !found {
		return raw
	}
	authority, tail, _ := strings.Cut(rest, "/")
	if i := strings.LastIndex(authority, "@"); i >= 0 {
		authority = "***@" + authority[i+1:]
	}
	out := scheme + "://" + authority
	if tail == "" {
		return out
	}
	if slices.Contains(tokenHookTypes, typ) {
		return out + "/***"
	}
	path, query, hasQuery := strings.Cut(tail, "?")
	out += "/" + path
	if hasQuery && query != "" {
		out += "?***"
	}
	return out
}
