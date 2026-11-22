package ci

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools/shared"
	sci "github.com/AndreyQuantum/forgejo-mcp/internal/tools/shared/ci"
)

// selector is the set of mutually exclusive CI target parameters shared by
// ci_failure and ci_wait.
type selector struct {
	Run, Number, PR int64
	Ref, SHA        string
}

func (s selector) count() int {
	n := 0
	for _, set := range []bool{s.Run != 0, s.Number != 0, s.PR != 0, s.Ref != "", s.SHA != ""} {
		if set {
			n++
		}
	}
	return n
}

// check validates the selector values.
func (s selector) check() error {
	if s.Run < 0 || s.Number < 0 || s.PR < 0 {
		return errors.New("invalid: run, number and pr must be positive")
	}
	if s.count() > 1 {
		return errors.New("invalid: give only one of run, number, pr, ref, sha")
	}
	if strings.HasPrefix(strings.TrimSpace(s.Ref), "#") {
		return errors.New("invalid: use pr=12 instead of ref=#12")
	}
	return nil
}

// implicit is the target of a call without any selector (section 3.1 step 4).
type implicit struct {
	Label  string
	PR     int64
	Branch string
}

// defaultTarget applies shared.ResolveTarget.
func defaultTarget(ctx context.Context, c *tools.Call) (implicit, error) {
	t, err := shared.ResolveTarget(ctx, c.Env.Client, c.Env.Repo, c.Owner, c.Repo)
	if err != nil {
		return implicit{}, err
	}
	return implicit{Label: t.Label, PR: t.PR, Branch: t.Branch}, nil
}

// waitBounds returns the effective timeout of ci_wait: the request or
// FORGEJO_WAIT_DEFAULT, clamped to [1 s, FORGEJO_WAIT_MAX].
func waitBounds(c *tools.Call, seconds int) time.Duration {
	def, maxWait := 50*time.Second, 55*time.Second
	if c.Env.Cfg != nil {
		if c.Env.Cfg.WaitDefault > 0 {
			def = c.Env.Cfg.WaitDefault
		}
		if c.Env.Cfg.WaitMax > 0 {
			maxWait = c.Env.Cfg.WaitMax
		}
	}
	d := def
	if seconds > 0 {
		d = time.Duration(seconds) * time.Second
	}
	return min(max(d, time.Second), maxWait)
}

// errf is fmt.Errorf kept short for call sites that return plain errors.
func errf(format string, a ...any) error { return fmt.Errorf(format, a...) }

// fullCommit returns the full lowercased commit id for a user-given sha: a
// full id (40 hex, 64 for sha256 repos) is used as is, a short one is expanded
// through the API.
func fullCommit(c *tools.Call, s string) (string, error) {
	s = strings.TrimSpace(s)
	if sci.ValidSHA(s) {
		return strings.ToLower(s), nil
	}
	return shared.ExpandSHA(c.Ctx, c.Env.Client, c.Owner, c.Repo, s)
}
