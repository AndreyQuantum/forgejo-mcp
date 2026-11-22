// Package scope derives the token scope category of an API operation. It has
// no embed and no internal imports so the generator and the runtime share it.
package scope

import "strings"

// Category returns the scope category of an operation, from its method, path
// template and swagger tag. The result is one of "activitypub", "admin",
// "notification", "organization", "package", "issue", "repository", "user",
// or "" when the operation needs no scope (version, settings, markdown...).
// When the server accepts either of two categories the result joins them
// with "|" (for example "user|organization"). The mapping follows the route
// groups of routers/api/v1/api.go: HTTP POST/PUT/PATCH/DELETE need
// write:<category>, everything else read:<category> or write:<category>.
func Category(method, path, tag string) string {
	_ = method
	switch {
	case path == "/nodeinfo" || strings.HasPrefix(path, "/activitypub/"):
		return "activitypub"
	case strings.HasPrefix(path, "/admin/"):
		return "admin"
	case path == "/notifications" || strings.HasPrefix(path, "/notifications/"):
		return "notification"
	case strings.HasPrefix(path, "/packages/"):
		return "package"
	case path == "/orgs" || strings.HasPrefix(path, "/orgs/") || strings.HasPrefix(path, "/teams/"):
		return "organization"
	case strings.HasPrefix(path, "/org/") && strings.HasSuffix(path, "/repos"):
		return "organization|repository"
	case path == "/user/orgs" || (strings.HasPrefix(path, "/users/") && strings.Contains(path, "/orgs")):
		return "user|organization"
	case path == "/user/repos" || strings.HasPrefix(path, "/user/starred") ||
		path == "/users/{username}/repos" || strings.HasPrefix(path, "/repositories/") ||
		strings.HasPrefix(path, "/topics/") || path == "/repos/search" ||
		strings.HasPrefix(path, "/repos/migrate"):
		return "repository"
	case strings.HasPrefix(path, "/repos/{owner}/{repo}/flags"):
		return "admin"
	case strings.HasPrefix(path, "/repos/"):
		switch tag {
		case "issue":
			return "issue"
		case "notification":
			return "notification"
		}
		return "repository"
	case path == "/user" || strings.HasPrefix(path, "/user/") || strings.HasPrefix(path, "/users"):
		return "user"
	}
	return ""
}
