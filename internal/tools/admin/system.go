package admin

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"
	"github.com/AndreyQuantum/forgejo-mcp/internal/format"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
)

func runOrgsList(c *tools.Call, p *adminParams) (*tools.Result, error) {
	return listLines(c, "orgs", "/admin/orgs", nil, p, orgLine)
}

func runEmailsList(c *tools.Call, p *adminParams) (*tools.Result, error) {
	return listLines(c, "emails", "/admin/emails", nil, p, emailLine)
}

func runEmailsSearch(c *tools.Call, p *adminParams) (*tools.Result, error) {
	return listLines(c, "emails", "/admin/emails/search", url.Values{"q": {p.Q}}, p, emailLine)
}

func runCronList(c *tools.Call, p *adminParams) (*tools.Result, error) {
	return listLines(c, "cron", "/admin/cron", nil, p, cronLine)
}

const (
	cronDetached = 10 * time.Minute // the server runs the task inline
	cronProgress = 10 * time.Second
)

// runCronRun starts a cron task. The server executes it inside the request,
// so the request is sent from a detached context; this call returns when it
// finishes or after FORGEJO_WAIT_MAX, whichever comes first.
func runCronRun(c *tools.Call, p *adminParams) (*tools.Result, error) {
	if !okName(p.Task) {
		return invalid(c, "task"), nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Ctx), cronDetached)
	done := make(chan error, 1)
	cl := c.Env.Client
	path := forgejo.Path("admin", "cron", p.Task)
	go func() {
		defer cancel()
		_, err := cl.Do(ctx, forgejo.Request{Method: http.MethodPost, Path: path, Timeout: cronDetached})
		done <- err
	}()

	clk := c.Env.Clock
	waitMax := c.Env.Cfg.WaitMax
	if waitMax <= 0 {
		waitMax = 55 * time.Second
	}
	deadline := clk.After(waitMax)
	tick := clk.After(cronProgress)
	var elapsed time.Duration
	for {
		select {
		case err := <-done:
			if err != nil {
				return nil, err
			}
			return &tools.Result{Text: "ok: cron task " + p.Task + " finished"}, nil
		case <-tick:
			elapsed += cronProgress
			c.Progress("cron "+p.Task+" running ("+format.Dur(elapsed)+")", elapsed.Seconds(), waitMax.Seconds())
			tick = clk.After(cronProgress)
		case <-deadline:
			return &tools.Result{Text: "started: cron task " + p.Task + "; still running on the server; check with cron_list"}, nil
		case <-c.Ctx.Done():
			return &tools.Result{Text: "started: cron task " + p.Task + "; the call was canceled but the task keeps running on the server; check with cron_list"}, nil
		}
	}
}

func runUnadoptedList(c *tools.Call, p *adminParams) (*tools.Result, error) {
	q := url.Values{}
	if p.Pattern != "" {
		q.Set("query", p.Pattern) // the real parameter name (swagger documents it as `pattern`)
	}
	return listLines(c, "unadopted", "/admin/unadopted", q, p, func(s *string) string { return *s })
}

// unadoptedTarget returns owner and repo; repo may be given as owner/name.
func unadoptedTarget(p *adminParams) (owner, repo string, err error) {
	owner, repo = strings.TrimSpace(p.Owner), strings.TrimSpace(p.Repo)
	if o, r, ok := strings.Cut(repo, "/"); ok {
		if owner == "" {
			owner = o
		} else if !strings.EqualFold(owner, o) {
			return "", "", fmt.Errorf("invalid: repo %s does not belong to owner %s", p.Repo, p.Owner)
		}
		repo = r
	}
	if !okName(owner) || !okName(repo) || strings.Contains(repo, "/") {
		return "", "", fmt.Errorf("invalid: owner and repo (got %q, %q)", p.Owner, p.Repo)
	}
	return owner, repo, nil
}

func runUnadoptedAdopt(c *tools.Call, p *adminParams) (*tools.Result, error) {
	o, r, err := unadoptedTarget(p)
	if err != nil {
		return c.Fail("%s", err.Error()), nil
	}
	if _, err := c.Env.Client.Do(c.Ctx, forgejo.Request{Method: http.MethodPost, Path: forgejo.Path("admin", "unadopted", o, r)}); err != nil {
		return nil, err
	}
	return &tools.Result{Text: fmt.Sprintf("ok: adopted %s/%s", o, r)}, nil
}

func runUnadoptedDelete(c *tools.Call, p *adminParams) (*tools.Result, error) {
	o, r, err := unadoptedTarget(p)
	if err != nil {
		return c.Fail("%s", err.Error()), nil
	}
	if res := c.Confirm(p.Confirm, o+"/"+r); res != nil {
		return res, nil
	}
	if _, err := c.Env.Client.Do(c.Ctx, forgejo.Request{Method: http.MethodDelete, Path: forgejo.Path("admin", "unadopted", o, r)}); err != nil {
		return nil, err
	}
	return &tools.Result{Text: fmt.Sprintf("ok: deleted unadopted files of %s/%s (cannot be undone)", o, r)}, nil
}
