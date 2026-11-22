package actions

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"
	"github.com/AndreyQuantum/forgejo-mcp/internal/format"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools/shared"
)

// artifact is the projection of an ActionArtifact (section 6.9).
type artifact struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Size      int64  `json:"size_in_bytes"`
	RunID     int64  `json:"run_id"`
	Expired   bool   `json:"expired"`
	ExpiresAt string `json:"expires_at"`
}

// artifactLine renders `artifact 3 dist.zip 1.2MB run:1234 expires T (expired)`.
func artifactLine(a artifact) string {
	s := fmt.Sprintf("artifact %d %s %s", a.ID, a.Name, format.Size(a.Size))
	if a.RunID != 0 {
		s += fmt.Sprintf(" run:%d", a.RunID)
	}
	if a.ExpiresAt != "" {
		s += " expires " + format.T(a.ExpiresAt)
	}
	if a.Expired {
		s += " (expired)"
	}
	return s
}

func (t *runTool) artifacts(c *tools.Call, p *runParams) (*tools.Result, error) {
	path := forgejo.Path("repos", c.Owner, c.Repo, "actions", "artifacts")
	if p.Run != 0 || p.Number != 0 {
		d, err := pickRun(c, p)
		if err != nil {
			return nil, err
		}
		path = runsPath(c, itoa(d.ID), "artifacts")
	}
	q := url.Values{}
	if p.Name != "" {
		q.Set("name", p.Name)
	}
	var items []artifact
	pg, err := c.Env.Client.List(c.Ctx, path, q, p.Page, p.Limit, &items)
	if err != nil {
		return nil, err
	}
	var b format.B
	b.Raw(format.ListHeader("artifacts", pg) + "\n")
	for _, a := range items {
		b.Raw(artifactLine(a) + "\n")
	}
	return &tools.Result{Text: strings.TrimRight(b.String(), "\n")}, nil
}

func (t *runTool) artifactDelete(c *tools.Call, p *runParams) (*tools.Result, error) {
	path := forgejo.Path("repos", c.Owner, c.Repo, "actions", "artifacts", itoa(p.ID))
	if _, err := c.Env.Client.Do(c.Ctx, forgejo.Request{Method: http.MethodDelete, Path: path}); err != nil {
		return nil, err
	}
	return &tools.Result{Text: fmt.Sprintf("ok: deleted artifact %d", p.ID)}, nil
}

// artifactDownload streams the zip to a spool file first and then moves it to
// a NEW local file under the save roots (section 10.5): a failed request or a
// refused destination leaves nothing behind. A cross-host storage redirect is
// followed by Client.Download without credentials (section 5.2).
func (t *runTool) artifactDownload(c *tools.Call, p *runParams) (*tools.Result, error) {
	cfg := c.Env.Cfg
	roots := &shared.FileRoots{}
	if cfg != nil {
		roots = &shared.FileRoots{
			SaveDir:   cfg.SaveDir,
			AllowCWD:  cfg.SaveCWD,
			TokenFile: cfg.TokenFile,
			Disabled:  cfg.Transport == "http" && !cfg.HTTPFiles,
		}
	}
	if roots.Disabled {
		return c.Fail("save_to is disabled in HTTP mode (set FORGEJO_HTTP_FILES to enable)"), nil
	}
	req := forgejo.Request{
		Method: http.MethodGet,
		Path:   forgejo.Path("repos", c.Owner, c.Repo, "actions", "artifacts", itoa(p.ID), "zip"),
	}
	// The destination is validated and created (O_EXCL) by SaveFile, which
	// reads from a pipe: a refused destination fails the first write of the
	// download, and a failed download removes the partial file.
	// A refused destination cancels the request context: the client retries
	// a failed write of an empty body, which would only delay the answer.
	dctx, cancel := context.WithCancel(c.Ctx)
	defer cancel()
	pr, pw := io.Pipe()
	type saved struct {
		final string
		n     int64
		err   error
	}
	done := make(chan saved, 1)
	go func() {
		final, n, err := roots.SaveFile(p.SaveTo, pr)
		pr.CloseWithError(err)
		if err != nil {
			cancel()
		}
		done <- saved{final, n, err}
	}()
	_, _, dlErr := c.Env.Client.Download(dctx, req, pw)
	if dlErr != nil {
		pw.CloseWithError(dlErr)
	} else {
		pw.Close()
	}
	res := <-done
	if res.err != nil && !errors.Is(res.err, dlErr) {
		return c.Fail("%s", strings.TrimPrefix(res.err.Error(), "error: ")), nil
	}
	if dlErr != nil {
		return nil, dlErr
	}
	final, n := res.final, res.n
	return &tools.Result{Text: fmt.Sprintf("ok: saved %s (%s)", final, format.Size(n))}, nil
}
