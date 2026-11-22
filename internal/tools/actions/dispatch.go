package actions

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools/shared"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools/shared/ci"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools/shared/ci/wait"
)

const (
	// locateAttempts x locateEvery is the 10 s window of section 8.6 in which
	// a run is searched after a 204 answer.
	locateAttempts = 10
	locateEvery    = time.Second
	// returnRunInfoMajor: servers older than this answer 204 without the run
	// info, so the run is remembered before the call and searched afterwards.
	returnRunInfoMajor = 16
	// locateSkew tolerates clock differences between this host and the server
	// when comparing a run's creation time with the dispatch time.
	locateSkew = 5 * time.Second
)

// stringifyInputs turns the free-form inputs object into the string map the
// API wants (`true` -> "true", 3 -> "3").
func stringifyInputs(in map[string]any) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		switch x := v.(type) {
		case nil:
			out[k] = ""
		case string:
			out[k] = x
		case bool:
			out[k] = strconv.FormatBool(x)
		case float64:
			out[k] = strconv.FormatFloat(x, 'f', -1, 64)
		case json.Number:
			out[k] = x.String()
		default:
			b, err := json.Marshal(x)
			if err != nil {
				out[k] = fmt.Sprint(x)
			} else {
				out[k] = string(b)
			}
		}
	}
	return out
}

// dispatched is the answer of a dispatch with return_run_info.
type dispatched struct {
	ID        int64    `json:"id"`
	RunNumber int64    `json:"run_number"`
	Jobs      []string `json:"jobs"`
}

func (t *runTool) dispatch(c *tools.Call, p *runParams) (*tools.Result, error) {
	cl := c.Env.Client
	wf := strings.TrimSpace(p.Workflow)
	ref := shared.NormalizeRef(p.Ref)
	if ref == "" {
		meta, err := shared.RepoMeta(c.Ctx, cl, c.Owner, c.Repo)
		if err != nil {
			return nil, err
		}
		ref = meta.DefaultBranch
	}
	body := map[string]any{"ref": ref, "return_run_info": true}
	if in := stringifyInputs(p.Inputs); len(in) > 0 {
		body["inputs"] = in
	}

	// Older servers answer 204 without the run: remember the newest run of
	// the workflow first so the new one can be told apart afterwards.
	t0 := c.Env.Clock.Now()
	var before int64
	if v, err := cl.ServerVersion(c.Ctx); err != nil || !v.Atleast(returnRunInfoMajor) {
		if r, _ := newestDispatchRun(c, wf); r != nil {
			before = r.ID
		}
	}

	path := forgejo.Path("repos", c.Owner, c.Repo, "actions", "workflows", wf, "dispatches")
	resp, err := cl.Do(c.Ctx, forgejo.Request{Method: http.MethodPost, Path: path, JSON: body})
	if err != nil {
		return nil, err
	}
	var d dispatched
	if len(resp.Body) > 0 {
		_ = json.Unmarshal(resp.Body, &d)
	}
	// The dispatch already succeeded: a failure to find the run must never
	// turn into a tool error, or the caller would dispatch a second run.
	var locateErr error
	if d.ID == 0 {
		var located *ci.Run
		located, locateErr = locateRun(c, wf, before, t0.Add(-locateSkew))
		if located != nil {
			d = dispatched{ID: located.ID, RunNumber: located.Index}
		}
	}

	line := fmt.Sprintf("ok: dispatched %s on %s", wf, ref)
	if d.ID == 0 {
		line += "; run id not available yet (run list workflow=" + wf + ")"
		if locateErr != nil {
			line += "\nnote: could not look up the run: " + firstLine(locateErr.Error())
		}
		if p.Wait {
			line += "\nnot waiting: the run could not be identified"
		}
		return &tools.Result{Text: line}, nil
	}
	line += fmt.Sprintf(": run %d", d.ID)
	if d.RunNumber > 0 {
		line += fmt.Sprintf(" no.%d", d.RunNumber)
	}
	if len(d.Jobs) > 0 {
		jobs := append([]string(nil), d.Jobs...)
		sort.Strings(jobs)
		line += " jobs: " + strings.Join(jobs, ",")
	}
	if !p.Wait {
		return &tools.Result{Text: line}, nil
	}

	res, err := wait.WaitRun(c.Ctx, cl, wait.RunOpts{
		Owner: c.Owner, Repo: c.Repo, Run: d.ID,
		FailFast: true, WithFailure: true,
		Poll: wait.PollOpts{
			Clock:    c.Env.Clock,
			Timeout:  waitTimeout(c, p.Timeout),
			Progress: c.Progress,
		},
	})
	if err != nil {
		return nil, err
	}
	return &tools.Result{Text: line + "\n" + res.Text}, nil
}

// waitTimeout resolves the wait timeout: the param or FORGEJO_WAIT_DEFAULT,
// clamped to [1 s, FORGEJO_WAIT_MAX] (section 10.1).
func waitTimeout(c *tools.Call, seconds int) time.Duration {
	def, maxWait := 50*time.Second, 55*time.Second
	if cfg := c.Env.Cfg; cfg != nil {
		if cfg.WaitDefault > 0 {
			def = cfg.WaitDefault
		}
		if cfg.WaitMax > 0 {
			maxWait = cfg.WaitMax
		}
	}
	d := def
	if seconds > 0 {
		d = time.Duration(seconds) * time.Second
	}
	return min(max(d, time.Second), maxWait)
}

// newestDispatchRun returns the newest workflow_dispatch run of a workflow
// (highest id) or nil.
func newestDispatchRun(c *tools.Call, wf string) (*ci.Run, error) {
	q := url.Values{"workflow_id": {wf}, "event": {"workflow_dispatch"}}
	var body struct {
		Runs []ci.Run `json:"workflow_runs"`
	}
	if _, err := c.Env.Client.List(c.Ctx, runsPath(c), q, 1, 3, &body); err != nil {
		return nil, err
	}
	var best *ci.Run
	for i := range body.Runs {
		if best == nil || body.Runs[i].ID > best.ID {
			best = &body.Runs[i]
		}
	}
	return best, nil
}

// locateRun finds the run created by a dispatch that answered 204: the
// newest workflow_dispatch run of the workflow with an id above before that
// was created no earlier than notBefore. It polls for about 10 s through the
// clock.
func locateRun(c *tools.Call, wf string, before int64, notBefore time.Time) (*ci.Run, error) {
	for i := 0; i < locateAttempts; i++ {
		r, err := newestDispatchRun(c, wf)
		if err != nil {
			return nil, err
		}
		if r != nil && r.ID > before && !createdBefore(r.Created, notBefore) {
			return r, nil
		}
		if i == locateAttempts-1 {
			break
		}
		if err := c.Env.Clock.Sleep(c.Ctx, locateEvery); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

// createdBefore reports whether the RFC 3339 timestamp is earlier than t; an
// unparsable or empty timestamp is not treated as older.
func createdBefore(ts string, t time.Time) bool {
	got, err := time.Parse(time.RFC3339, ts)
	return err == nil && got.Before(t)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
