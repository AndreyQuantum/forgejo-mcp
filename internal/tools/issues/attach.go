package issues

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"
	"github.com/AndreyQuantum/forgejo-mcp/internal/format"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools/shared"
)

// attachParams is the flat parameter struct of the `attach` tool.
type attachParams struct {
	Action    string `json:"action"`
	Repo      string `json:"repo,omitempty" jsonschema:"owner/name; default repo when omitted"`
	N         int64  `json:"n,omitempty" jsonschema:"issue or PR number"`
	CommentID int64  `json:"comment_id,omitempty" jsonschema:"use the attachments of this comment instead"`
	ID        int64  `json:"id,omitempty" jsonschema:"attachment id"`
	File      string `json:"file,omitempty" jsonschema:"local file to upload (under the save dir)"`
	Name      string `json:"name,omitempty" jsonschema:"attachment file name"`
	SaveTo    string `json:"save_to,omitempty" jsonschema:"local path to write; default the attachment name"`
}

const uploadTimeout = 10 * time.Minute

func registerAttach(r *tools.Registry) {
	t := &attachTool{}
	R, W := tools.Read, tools.Write
	issueEP := "/repos/{owner}/{repo}/issues/{index}/assets"
	commentEP := "/repos/{owner}/{repo}/issues/comments/{id}/assets"
	r.Add(tools.Spec{
		Name:        "attach",
		Toolset:     "issues",
		Description: "Issue/comment attachments: list, upload, download, rename, delete.",
		Params:      attachParams{},
		RepoScoped:  true,
		Actions: []tools.Action{
			tools.NewAction("list", R, t.list,
				tools.Summary("attachments of an issue (n) or comment (comment_id)"),
				tools.Params("n", "comment_id"),
				tools.Endpoints("GET "+issueEP, "GET "+commentEP)),
			tools.NewAction("upload", W, t.upload,
				tools.Summary("upload a local file as attachment"),
				tools.Params("n", "comment_id", "file", "name"), tools.Required("file"),
				tools.Endpoints("POST "+issueEP, "POST "+commentEP)),
			tools.NewAction("download", R, t.download,
				tools.Summary("save an attachment to a local file"),
				tools.Params("n", "comment_id", "id", "save_to"), tools.Required("id"),
				tools.Endpoints("GET "+issueEP, "GET "+commentEP)),
			tools.NewAction("rename", W, t.rename,
				tools.Summary("rename an attachment"),
				tools.Params("n", "comment_id", "id", "name"), tools.Required("id", "name"),
				tools.Endpoints("PATCH "+issueEP+"/{attachment_id}", "PATCH "+commentEP+"/{attachment_id}")),
			tools.NewAction("delete", W, t.delete,
				tools.Summary("delete an attachment"),
				tools.Params("n", "comment_id", "id"), tools.Required("id"),
				tools.Endpoints("DELETE "+issueEP+"/{attachment_id}", "DELETE "+commentEP+"/{attachment_id}")),
		},
	})
}

type attachTool struct{}

// assetsPath returns the attachment collection of the issue or comment.
func assetsPath(c *tools.Call, p *attachParams) (string, string, error) {
	switch {
	case p.CommentID > 0:
		return forgejo.Path("repos", c.Owner, c.Repo, "issues", "comments", strconv.FormatInt(p.CommentID, 10), "assets"),
			fmt.Sprintf("comment c%d", p.CommentID), nil
	case p.N > 0:
		return issuePath(c, p.N, "assets"), fmt.Sprintf("#%d", p.N), nil
	}
	return "", "", fmt.Errorf("invalid: pass n (issue) or comment_id")
}

func (t *attachTool) fetch(c *tools.Call, base string) ([]*asset, error) {
	var items []*asset
	if _, err := c.Env.Client.GetJSON(c.Ctx, base, nil, &items); err != nil {
		return nil, err
	}
	out := items[:0:0]
	for _, a := range items {
		if a != nil {
			out = append(out, a)
		}
	}
	return out, nil
}

func (t *attachTool) list(c *tools.Call, p *attachParams) (*tools.Result, error) {
	base, _, err := assetsPath(c, p)
	if err != nil {
		return c.Fail("%s", err), nil
	}
	items, err := t.fetch(c, base)
	if err != nil {
		return nil, err
	}
	var b format.B
	b.Raw(fmt.Sprintf("# attachments %d\n", len(items)))
	for _, a := range items {
		ext := ""
		if a.Type == "external" {
			ext = "external"
		}
		dl := ""
		if a.DownloadCount > 0 {
			dl = "dl:" + strconv.FormatInt(a.DownloadCount, 10)
		}
		b.Line("id "+strconv.FormatInt(a.ID, 10), a.Name, format.Size(a.Size), dl, ext)
	}
	return &tools.Result{Text: b.String()}, nil
}

func (t *attachTool) upload(c *tools.Call, p *attachParams) (*tools.Result, error) {
	base, target, err := assetsPath(c, p)
	if err != nil {
		return c.Fail("%s", err), nil
	}
	f, size, err := fileRoots(c).OpenLocal(p.File)
	if err != nil {
		return c.Fail("%s", strings.TrimPrefix(err.Error(), "error: ")), nil
	}
	defer f.Close()
	name := strings.TrimSpace(p.Name)
	if name == "" {
		name = filepath.Base(f.Name())
	}
	// multipart envelope around the streamed file; the length is known, so
	// the request is not chunked.
	var head bytes.Buffer
	mw := multipart.NewWriter(&head)
	if _, err := mw.CreateFormFile("attachment", name); err != nil {
		return nil, err
	}
	tail := "\r\n--" + mw.Boundary() + "--\r\n"
	length := int64(head.Len()) + size + int64(len(tail))
	body := io.MultiReader(&head, f, strings.NewReader(tail))
	resp, err := c.Env.Client.Do(c.Ctx, forgejo.Request{
		Method:        http.MethodPost,
		Path:          base,
		Query:         url.Values{"name": {name}},
		Body:          body,
		ContentType:   mw.FormDataContentType(),
		ContentLength: length,
		Timeout:       uploadTimeout,
	})
	if err != nil {
		return nil, err
	}
	var out asset
	idText := ""
	if err := json.Unmarshal(resp.Body, &out); err == nil && out.ID != 0 {
		idText = strconv.FormatInt(out.ID, 10) + " "
	}
	return &tools.Result{Text: fmt.Sprintf("ok: uploaded attachment %s%s (%s) to %s", idText, name, format.Size(size), target)}, nil
}

func (t *attachTool) download(c *tools.Call, p *attachParams) (*tools.Result, error) {
	base, _, err := assetsPath(c, p)
	if err != nil {
		return c.Fail("%s", err), nil
	}
	items, err := t.fetch(c, base)
	if err != nil {
		return nil, err
	}
	var a *asset
	for _, it := range items {
		if it.ID == p.ID {
			a = it
			break
		}
	}
	if a == nil {
		return c.Fail("404 attachment %d not found; list shows %d attachments", p.ID, len(items)), nil
	}
	if a.Type == "external" {
		return &tools.Result{Text: fmt.Sprintf("external asset: %s (not downloaded; credentials are not sent to other hosts)", a.URL)}, nil
	}
	meta, err := shared.RepoMeta(c.Ctx, c.Env.Client, c.Owner, c.Repo)
	if err != nil {
		return nil, err
	}
	apiPath, ok := shared.AssetPath(a.URL, shared.RootSubPath(meta.HTMLURL, c.Owner, c.Repo))
	if !ok {
		return c.Fail("attachment %d has no downloadable URL path", a.ID), nil
	}
	dest := strings.TrimSpace(p.SaveTo)
	if dest == "" {
		dest = filepath.Base(a.Name)
	}
	roots := fileRoots(c)
	if roots.Disabled {
		return c.Fail("save_to is disabled in HTTP mode (set FORGEJO_HTTP_FILES to enable)"), nil
	}
	// SaveFile creates the destination (O_EXCL) before the first read, so a
	// refused destination is reported before any bytes are downloaded; the
	// download starts lazily on the first Read and a failed one removes the file.
	src := &lazyDownload{start: func(w io.Writer) error {
		_, _, err := c.Env.Client.Download(c.Ctx, forgejo.Request{Method: http.MethodGet, Path: apiPath, API: "root"}, w)
		return err
	}}
	final, n, err := roots.SaveFile(dest, src)
	if dlErr := src.finish(); dlErr != nil && (err == nil || !errors.Is(dlErr, io.ErrClosedPipe)) {
		return nil, dlErr
	}
	if err != nil {
		return c.Fail("%s", strings.TrimPrefix(err.Error(), "error: ")), nil
	}
	return &tools.Result{Text: fmt.Sprintf("ok: saved attachment %d %s to %s (%s)", a.ID, a.Name, final, format.Size(n))}, nil
}

func (t *attachTool) rename(c *tools.Call, p *attachParams) (*tools.Result, error) {
	base, _, err := assetsPath(c, p)
	if err != nil {
		return c.Fail("%s", err), nil
	}
	if _, err := c.Env.Client.SendJSON(c.Ctx, http.MethodPatch, base+"/"+strconv.FormatInt(p.ID, 10), nil, map[string]string{"name": p.Name}, nil); err != nil {
		return nil, err
	}
	return &tools.Result{Text: fmt.Sprintf("ok: renamed attachment %d to %s", p.ID, p.Name)}, nil
}

func (t *attachTool) delete(c *tools.Call, p *attachParams) (*tools.Result, error) {
	base, _, err := assetsPath(c, p)
	if err != nil {
		return c.Fail("%s", err), nil
	}
	if _, err := c.Env.Client.Do(c.Ctx, forgejo.Request{Method: http.MethodDelete, Path: base + "/" + strconv.FormatInt(p.ID, 10)}); err != nil {
		return nil, err
	}
	return &tools.Result{Text: fmt.Sprintf("ok: deleted attachment %d", p.ID)}, nil
}

// lazyDownload is an io.Reader that starts its download on the first Read.
type lazyDownload struct {
	start func(w io.Writer) error
	pr    *io.PipeReader
	done  chan error
}

func (l *lazyDownload) Read(p []byte) (int, error) {
	if l.pr == nil {
		pr, pw := io.Pipe()
		l.pr = pr
		l.done = make(chan error, 1)
		go func() {
			err := l.start(pw)
			pw.CloseWithError(err) // nil closes with io.EOF
			l.done <- err
		}()
	}
	return l.pr.Read(p)
}

// finish stops a started download and returns its error, if any.
func (l *lazyDownload) finish() error {
	if l.pr == nil {
		return nil
	}
	l.pr.Close()
	return <-l.done
}
