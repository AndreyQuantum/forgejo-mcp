package scope

import "testing"

func TestCategory(t *testing.T) {
	cases := []struct{ method, path, tag, want string }{
		{"GET", "/admin/users", "admin", "admin"},
		{"GET", "/repos/{owner}/{repo}/pulls", "repository", "repository"},
		{"GET", "/repos/{owner}/{repo}/issues", "issue", "issue"},
		{"GET", "/repos/{owner}/{repo}/labels", "issue", "issue"},
		{"GET", "/repos/{owner}/{repo}/notifications", "notification", "notification"},
		{"GET", "/notifications", "notification", "notification"},
		{"GET", "/orgs/{org}", "organization", "organization"},
		{"GET", "/orgs/{org}/labels", "organization", "organization"},
		{"GET", "/teams/{id}", "organization", "organization"},
		{"GET", "/user/orgs", "organization", "user|organization"},
		{"GET", "/user", "user", "user"},
		{"GET", "/user/keys", "user", "user"},
		{"GET", "/users/{username}", "user", "user"},
		{"GET", "/user/repos", "repository", "repository"},
		{"GET", "/user/starred", "repository", "repository"},
		{"GET", "/packages/{owner}", "package", "package"},
		{"POST", "/org/{org}/repos", "organization", "organization|repository"},
		{"GET", "/version", "miscellaneous", ""},
		{"POST", "/markdown", "miscellaneous", ""},
		{"GET", "/activitypub/actor", "activitypub", "activitypub"},
		{"GET", "/repos/search", "repository", "repository"},
		{"PUT", "/repos/{owner}/{repo}/flags", "repository", "admin"},
		{"GET", "/repos/{owner}/{repo}/flags/{flag}", "repository", "admin"},
	}
	for _, c := range cases {
		if got := Category(c.method, c.path, c.tag); got != c.want {
			t.Errorf("Category(%s %s %s) = %q, want %q", c.method, c.path, c.tag, got, c.want)
		}
	}
}
