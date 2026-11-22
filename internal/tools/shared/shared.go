// Package shared holds helpers used by several tool areas (DESIGN.md sections
// 1, 3.1, 5.6, 5.7, 10.5). It never imports package tools: helpers take a
// *forgejo.Client and plain arguments.
package shared

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"
)

const (
	repoMetaTTL = 5 * time.Minute
	listTTL     = 60 * time.Second
	maxPages    = 100 // safety bound of every "all pages" loop
)

var namePart = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// validPart reports whether s is a legal owner or repo name.
func validPart(s string) bool { return namePart.MatchString(s) && s != "." && s != ".." }

// ParseOwnerRepo validates and splits "owner/name" (each part must match
// ^[A-Za-z0-9._-]+$).
func ParseOwnerRepo(s string) (owner, name string, err error) {
	o, n, ok := strings.Cut(strings.TrimSpace(s), "/")
	if !ok || !validPart(o) || !validPart(n) {
		return "", "", fmt.Errorf("invalid repo %q: want owner/name", s)
	}
	return o, n, nil
}

func checkOwnerRepo(owner, repo string) error {
	if !validPart(owner) || !validPart(repo) {
		return fmt.Errorf("invalid repo %q: want owner/name", owner+"/"+repo)
	}
	return nil
}

func repoPath(owner, repo string) string { return forgejo.Path("repos", owner, repo) }

// RepoMeta returns the cached (5 min) `GET /repos/{o}/{r}` object.
func RepoMeta(ctx context.Context, c *forgejo.Client, owner, repo string) (*forgejo.Repo, error) {
	if err := checkOwnerRepo(owner, repo); err != nil {
		return nil, err
	}
	v, err := c.Cache().Get(c.CacheKey("repo:"+owner+"/"+repo), repoMetaTTL, func() (any, error) {
		var r forgejo.Repo
		if _, err := c.GetJSON(ctx, repoPath(owner, repo), nil, &r); err != nil {
			return nil, err
		}
		return &r, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*forgejo.Repo), nil
}

// InvalidateRepo drops the cached repo meta (call after `repo edit`).
func InvalidateRepo(c *forgejo.Client, owner, repo string) {
	c.Cache().Invalidate(c.CacheKey("repo:" + owner + "/" + repo))
}

// listAll reads every page of a list endpoint with the maximum page size.
func listAll[T any](ctx context.Context, c *forgejo.Client, p string, q url.Values) ([]T, error) {
	var all []T
	for page := 1; page <= maxPages; page++ {
		var items []T
		pg, err := c.List(ctx, p, q, page, 1<<20, &items)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
		if len(items) == 0 || !pg.More {
			break
		}
	}
	return all, nil
}

// NormalizeRef normalises a user-supplied branch/tag/sha reference
// (strips refs/heads/, refs/tags/ and surrounding whitespace).
func NormalizeRef(ref string) string {
	ref = strings.TrimSpace(ref)
	if r, ok := strings.CutPrefix(ref, "refs/heads/"); ok {
		return r
	}
	return strings.TrimPrefix(ref, "refs/tags/")
}

// AssetPath turns a browser_download_url into the request path on the
// instance (section 5.7): only the URL path is used, minus the ROOT_URL
// sub-path. ok is false for URLs that cannot be a path on the instance
// (unparseable, empty, dot segments, the API tree); callers must also skip
// assets of type external before calling it.
func AssetPath(browserURL, rootSubPath string) (p string, ok bool) {
	u, err := url.Parse(strings.TrimSpace(browserURL))
	if err != nil || (u.Scheme != "" && u.Scheme != "http" && u.Scheme != "https") {
		return "", false
	}
	p = u.EscapedPath()
	if sub := strings.TrimRight(rootSubPath, "/"); sub != "" {
		if rest, found := strings.CutPrefix(p, sub+"/"); found {
			p = "/" + rest
		}
	}
	if !strings.HasPrefix(p, "/") || p == "/" || strings.Contains(p, "\\") {
		return "", false
	}
	for _, seg := range strings.Split(p[1:], "/") {
		if seg == "." || seg == ".." || strings.EqualFold(seg, "%2e") || strings.EqualFold(seg, "%2e%2e") {
			return "", false
		}
	}
	if strings.HasPrefix(path.Clean(p), "/api/") {
		return "", false
	}
	return p, true
}

// RootSubPath learns the ROOT_URL sub-path from a repo's html_url
// (`https://h/sub/owner/repo` -> `/sub`; "" without a sub-path).
func RootSubPath(htmlURL, owner, repo string) string {
	u, err := url.Parse(htmlURL)
	if err != nil {
		return ""
	}
	p := strings.TrimRight(u.Path, "/")
	suffix := "/" + owner + "/" + repo
	if len(p) < len(suffix) || !strings.EqualFold(p[len(p)-len(suffix):], suffix) {
		return ""
	}
	p = p[:len(p)-len(suffix)]
	if p == "" || !strings.HasPrefix(p, "/") {
		return ""
	}
	return p
}
