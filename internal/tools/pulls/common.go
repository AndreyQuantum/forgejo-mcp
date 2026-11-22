package pulls

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"
	"github.com/AndreyQuantum/forgejo-mcp/internal/format"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
)

const (
	defaultBodyMax = 1500  // PR body cap (6.6)
	hardBodyMax    = 20000 // body_max=0 / full=true
	commentMax     = 300   // comment and review bodies in lists
	titleMax       = 120
	maxParallel    = 6
)

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// repoPath returns "/repos/{owner}/{repo}" plus rest for the call's repo.
func repoPath(c *tools.Call, rest ...string) string {
	return forgejo.Path(append([]string{"repos", c.Owner, c.Repo}, rest...)...)
}

func pullPath(c *tools.Call, n int64, rest ...string) string {
	return repoPath(c, append([]string{"pulls", itoa(n)}, rest...)...)
}

// pageArgs normalises page/limit; the client clamps limit to the server max.
func pageArgs(c *tools.Call, page, limit, def int) (int, int) {
	if page < 1 {
		page = 1
	}
	if limit <= 0 {
		limit = def
		if limit <= 0 {
			limit = 20
			if c.Env.Cfg != nil && c.Env.Cfg.DefaultLimit > 0 {
				limit = c.Env.Cfg.DefaultLimit
			}
		}
	}
	return page, min(limit, 100)
}

// bodyText caps a long text per 6.6: body_max (default 1500), 0 or full
// lifts the cap to the hard limit.
func bodyText(s string, bodyMax *int, full bool) string {
	limit := defaultBodyMax
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

// apiError returns the *APIError in err's chain, or nil.
func apiError(err error) *forgejo.APIError {
	var ae *forgejo.APIError
	if errors.As(err, &ae) {
		return ae
	}
	return nil
}

func apiStatus(err error) int {
	if ae := apiError(err); ae != nil {
		return ae.Status
	}
	return 0
}

// oneLineErr is a short single-line form of err for soft section failures.
func oneLineErr(err error) string {
	s := err.Error()
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimPrefix(s, "error: ")
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

// parallel runs fns concurrently (at most maxParallel at once) and waits.
func parallel(fns ...func()) {
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxParallel)
	for _, fn := range fns {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			fn()
		}()
	}
	wg.Wait()
}

// sleep waits d on the call's clock (fake in tests).
func sleep(c *tools.Call, d time.Duration) error {
	if c.Env.Clock != nil {
		return c.Env.Clock.Sleep(c.Ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-c.Ctx.Done():
		return c.Ctx.Err()
	}
}
