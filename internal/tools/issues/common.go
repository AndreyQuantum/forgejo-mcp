package issues

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"
	"github.com/AndreyQuantum/forgejo-mcp/internal/format"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools/shared"
)

const (
	defaultBodyMax = 1500  // issue/PR body cap (6.6)
	hardBodyMax    = 20000 // body_max=0 / full=true
	commentMax     = 300   // comment bodies in lists
	titleMax       = 120
)

// repoBase returns "/repos/{owner}/{repo}" for the call's resolved repo.
func repoBase(c *tools.Call) string { return forgejo.Path("repos", c.Owner, c.Repo) }

func issuePath(c *tools.Call, n int64, rest ...string) string {
	parts := append([]string{"repos", c.Owner, c.Repo, "issues", strconv.FormatInt(n, 10)}, rest...)
	return forgejo.Path(parts...)
}

// pageArgs normalises page/limit; the client clamps limit to the server max.
func pageArgs(c *tools.Call, page, limit int) (int, int) {
	if page < 1 {
		page = 1
	}
	if limit <= 0 {
		limit = 20
		if c.Env.Cfg != nil && c.Env.Cfg.DefaultLimit > 0 {
			limit = c.Env.Cfg.DefaultLimit
		}
	}
	return page, min(limit, 100)
}

// bodyText caps a long text per 6.6: body_max (default max), 0 or full lifts
// the cap to the hard limit.
func bodyText(s string, bodyMax *int, full bool, def int) string {
	limit := def
	switch {
	case full:
		limit = hardBodyMax
	case bodyMax != nil && *bodyMax == 0:
		limit = hardBodyMax
	case bodyMax != nil && *bodyMax > 0:
		limit = min(*bodyMax, hardBodyMax)
	}
	hint := "pass body_max=0 or full=true"
	if limit >= hardBodyMax {
		hint = "hard cap reached"
	}
	return format.Trunc(strings.TrimRight(s, "\n"), limit, hint)
}

// datePart returns the YYYY-MM-DD part of an RFC 3339 time.
func datePart(s string) string {
	if len(s) >= 10 {
		return s[:10]
	}
	return s
}

var reDate = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// parseDue turns YYYY-MM-DD (or a full RFC 3339 time) into an RFC 3339 time.
func parseDue(s string) (string, error) {
	s = strings.TrimSpace(s)
	if reDate.MatchString(s) {
		if _, err := time.Parse("2006-01-02", s); err != nil {
			return "", fmt.Errorf("invalid: due %q: want YYYY-MM-DD", s)
		}
		return s + "T00:00:00Z", nil
	}
	if _, err := time.Parse(time.RFC3339, s); err != nil {
		return "", fmt.Errorf("invalid: due %q: want YYYY-MM-DD", s)
	}
	return s, nil
}

// apiStatus returns the HTTP status of an APIError in err's chain, or 0.
func apiStatus(err error) int {
	var ae *forgejo.APIError
	if errors.As(err, &ae) {
		return ae.Status
	}
	return 0
}

// resolveRepo resolves an explicit/default repo for actions that are not
// repo-scoped by the framework (label with org, search).
func resolveRepo(c *tools.Call, explicit string) (owner, repo string, err error) {
	if c.Env.Repo == nil {
		return "", "", errors.New("invalid: no repository resolver configured")
	}
	o, n, _, err := c.Env.Repo.Resolve(explicit)
	if err != nil {
		return "", "", err
	}
	if _, _, perr := shared.ParseOwnerRepo(o + "/" + n); perr != nil {
		return "", "", fmt.Errorf("invalid: repo %q (owner and name allow letters, digits, . _ -)", o+"/"+n)
	}
	return o, n, nil
}

// splitOther parses `owner/repo#N` or `#N` (owner/repo empty for the latter).
func splitOther(s string) (owner, repo string, n int64, err error) {
	s = strings.TrimSpace(s)
	repoPart, num, ok := strings.Cut(s, "#")
	if !ok {
		return "", "", 0, fmt.Errorf("invalid: other %q: want owner/repo#N or #N", s)
	}
	n, perr := strconv.ParseInt(strings.TrimSpace(num), 10, 64)
	if perr != nil || n < 1 {
		return "", "", 0, fmt.Errorf("invalid: other %q: want owner/repo#N or #N", s)
	}
	if repoPart != "" {
		o, r, e := shared.ParseOwnerRepo(repoPart)
		if e != nil {
			return "", "", 0, fmt.Errorf("invalid: other %q: want owner/repo#N or #N", s)
		}
		owner, repo = o, r
	}
	return owner, repo, n, nil
}

// refOf renders an issue reference: `#N` inside the current repo, else
// `owner/repo#N`.
func refOf(c *tools.Call, fullName string, n int64) string {
	if fullName == "" || strings.EqualFold(fullName, c.Owner+"/"+c.Repo) {
		return fmt.Sprintf("#%d", n)
	}
	return fmt.Sprintf("%s#%d", fullName, n)
}

// labelRefs resolves label names to the server's labels (id and canonical
// name). The server matches names exactly and silently drops unknown ones, so
// callers send ids or canonical names, never the user's spelling.
func labelRefs(c *tools.Call, names []string) ([]forgejo.Label, error) {
	cl := c.Env.Client
	ids, err := shared.ResolveLabels(c.Ctx, cl, c.Owner, c.Repo, names)
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	// ResolveLabels has just filled this cache entry; the loader is a fallback.
	v, err := cl.Cache().Get(cl.CacheKey("labels:"+c.Owner+"/"+c.Repo), 5*time.Minute, func() (any, error) {
		return listAllPages[forgejo.Label](c, repoBase(c)+"/labels", nil)
	})
	if err != nil {
		return nil, err
	}
	all, _ := v.([]forgejo.Label)
	out := make([]forgejo.Label, 0, len(ids))
	for i, id := range ids {
		l := forgejo.Label{ID: id, Name: strings.TrimSpace(names[i])}
		for _, cand := range all {
			if cand.ID == id {
				l = cand
				break
			}
		}
		out = append(out, l)
	}
	return out, nil
}

func listAllPages[T any](c *tools.Call, path string, q url.Values) ([]T, error) {
	var all []T
	for page := 1; page <= 20; page++ {
		var items []T
		pg, err := c.Env.Client.List(c.Ctx, path, q, page, 100, &items)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
		if !pg.More {
			break
		}
	}
	return all, nil
}

func labelIDs(ls []forgejo.Label) []int64 {
	out := make([]int64, len(ls))
	for i, l := range ls {
		out[i] = l.ID
	}
	return out
}

// milestoneByTitle resolves a milestone by its title only (exact, then
// case-insensitive); a numeric title is never taken for an id.
func milestoneByTitle(c *tools.Call, title string) (int64, error) {
	cl := c.Env.Client
	title = strings.TrimSpace(title)
	v, err := cl.Cache().Get(cl.CacheKey("milestones:"+c.Owner+"/"+c.Repo), 5*time.Minute, func() (any, error) {
		return listAllPages[forgejo.Milestone](c, repoBase(c)+"/milestones", url.Values{"state": {"all"}})
	})
	if err != nil {
		return 0, err
	}
	ms, _ := v.([]forgejo.Milestone)
	for _, m := range ms {
		if m.Title == title {
			return m.ID, nil
		}
	}
	for _, m := range ms {
		if strings.EqualFold(m.Title, title) {
			return m.ID, nil
		}
	}
	known := make([]string, 0, 30)
	for _, m := range ms[:min(len(ms), 30)] {
		known = append(known, m.Title)
	}
	return 0, fmt.Errorf("unknown milestone %q; known: %s", title, csv(known))
}

func csv(xs []string) string { return strings.Join(xs, ",") }

func addIf(q url.Values, key, val string) {
	if strings.TrimSpace(val) != "" {
		q.Set(key, strings.TrimSpace(val))
	}
}

func rfc3339(s, name string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	if reDate.MatchString(s) {
		return s + "T00:00:00Z", nil
	}
	if _, err := time.Parse(time.RFC3339, s); err != nil {
		return "", fmt.Errorf("invalid: %s %q: want RFC 3339 (2026-11-22T10:00:00Z) or YYYY-MM-DD", name, s)
	}
	return s, nil
}

// fileRoots builds the local-file roots of section 10.5 from the config.
func fileRoots(c *tools.Call) *shared.FileRoots {
	cfg := c.Env.Cfg
	return &shared.FileRoots{
		SaveDir:   cfg.SaveDir,
		AllowCWD:  cfg.SaveCWD,
		TokenFile: cfg.TokenFile,
		Disabled:  cfg.Transport == "http" && !cfg.HTTPFiles,
	}
}

func durSeconds(s int64) time.Duration { return time.Duration(s) * time.Second }
