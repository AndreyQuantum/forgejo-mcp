package content

import (
	"bytes"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"
	"github.com/AndreyQuantum/forgejo-mcp/internal/format"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools/shared"
)

// releaseParams are the parameters of the release tool (section 8.2).
type releaseParams struct {
	Action     string  `json:"action"`
	Repo       string  `json:"repo,omitempty" jsonschema:"owner/name; default is the detected repo"`
	ID         int64   `json:"id,omitempty" jsonschema:"release id"`
	Tag        string  `json:"tag,omitempty" jsonschema:"release tag; latest selects the newest release"`
	Title      *string `json:"title,omitempty" jsonschema:"release title"`
	Body       *string `json:"body,omitempty" jsonschema:"release notes"`
	Target     *string `json:"target,omitempty" jsonschema:"branch or sha the tag points at"`
	Draft      *bool   `json:"draft,omitempty" jsonschema:"draft flag; list filter or new value"`
	Prerelease *bool   `json:"prerelease,omitempty" jsonschema:"prerelease flag; list filter or new value"`
	Q          string  `json:"q,omitempty" jsonschema:"search text"`
	AssetID    int64   `json:"asset_id,omitempty" jsonschema:"asset id"`
	Name       string  `json:"name,omitempty" jsonschema:"asset name (upload default: file name)"`
	File       string  `json:"file,omitempty" jsonschema:"local file to upload, under the save dir"`
	URL        string  `json:"url,omitempty" jsonschema:"external asset URL (instead of file)"`
	SaveTo     string  `json:"save_to,omitempty" jsonschema:"local file to write, under the save dir; default is the asset name"`
	Full       bool    `json:"full,omitempty" jsonschema:"lift the release notes cap"`
	Page       int     `json:"page,omitempty" jsonschema:"page number from 1"`
	Limit      int     `json:"limit,omitempty" jsonschema:"page size"`
}

const (
	releaseBodyCap     = 2000
	releaseBodyFullCap = 20000
	uploadTimeout      = 10 * time.Minute
)

// registerRelease registers the release tool.
func registerRelease(r *tools.Registry) {
	t := &releaseTool{}
	const rel = "/repos/{owner}/{repo}/releases"
	r.Add(tools.Spec{
		Name:        "release",
		Toolset:     "repos",
		Description: "Releases and their assets: list, get, create, edit, delete, asset_*.",
		Params:      releaseParams{},
		RepoScoped:  true,
		Actions: []tools.Action{
			tools.NewAction("list", tools.Read, t.list,
				tools.Summary("list releases (filters: draft, prerelease, q)"),
				tools.Params("draft", "prerelease", "q", "page", "limit"),
				tools.Endpoints("GET "+rel)),
			tools.NewAction("get", tools.Read, t.get,
				tools.Summary("one release by id, tag or tag=latest, with its assets"),
				tools.Params("id", "tag", "full"),
				tools.Endpoints("GET "+rel+"/{id}", "GET "+rel+"/tags/{tag}", "GET "+rel+"/latest")),
			tools.NewAction("create", tools.Write, t.create,
				tools.Summary("create a release (creates the tag when it does not exist)"),
				tools.Params("title", "body", "target", "draft", "prerelease"),
				tools.Required("tag"),
				tools.Endpoints("POST "+rel), tools.Needs("repo write")),
			tools.NewAction("edit", tools.Write, t.edit,
				tools.Summary("edit a release by id or tag (empty text cannot be set)"),
				tools.Params("id", "tag", "title", "body", "target", "draft", "prerelease"),
				tools.Endpoints("PATCH "+rel+"/{id}"), tools.Needs("repo write")),
			tools.NewAction("delete", tools.Write, t.delete,
				tools.Summary("delete a release by id or tag (the git tag may remain)"),
				tools.Params("id", "tag"),
				tools.Endpoints("DELETE "+rel+"/{id}", "DELETE "+rel+"/tags/{tag}"), tools.Needs("repo write")),
			tools.NewAction("asset_list", tools.Read, t.assetList,
				tools.Summary("assets of a release"),
				tools.Params("id", "tag", "page", "limit"),
				tools.Endpoints("GET "+rel+"/{id}/assets")),
			tools.NewAction("asset_upload", tools.Write, t.assetUpload,
				tools.Summary("upload a local file (file) or register an external link (url)"),
				tools.Params("id", "tag", "file", "url", "name"),
				tools.Endpoints("POST "+rel+"/{id}/assets"), tools.Needs("repo write")),
			tools.NewAction("asset_edit", tools.Write, t.assetEdit,
				tools.Summary("rename an asset"),
				tools.Params("id", "tag"),
				tools.Required("asset_id", "name"),
				tools.Endpoints("PATCH "+rel+"/{id}/assets/{attachment_id}"), tools.Needs("repo write")),
			tools.NewAction("asset_delete", tools.Write, t.assetDelete,
				tools.Summary("delete an asset"),
				tools.Params("id", "tag"),
				tools.Required("asset_id"),
				tools.Endpoints("DELETE "+rel+"/{id}/assets/{attachment_id}"), tools.Needs("repo write")),
			tools.NewAction("asset_download", tools.Read, t.assetDownload,
				tools.Summary("download an asset to save_to (bytes never enter the context)"),
				tools.Params("id", "tag", "save_to"),
				tools.Required("asset_id"),
				tools.Endpoints("GET "+rel+"/{id}/assets")),
		},
	})
}

type releaseTool struct{}

// ---- rendering ---------------------------------------------------------

func releaseFlags(r release) string {
	switch {
	case r.Draft && r.Prerelease:
		return "draft,prerelease"
	case r.Draft:
		return "draft"
	case r.Prerelease:
		return "prerelease"
	}
	return ""
}

func releaseLine(r release) string {
	var b format.B
	assets := ""
	if len(r.Assets) > 0 {
		assets = "assets:" + strconv.Itoa(len(r.Assets))
	}
	when := r.PublishedAt
	if when == "" {
		when = r.CreatedAt
	}
	b.Line("id "+strconv.FormatInt(r.ID, 10), r.TagName, format.OneLine(r.Name, 120), releaseFlags(r), format.T(when), assets)
	return b.String()
}

func assetLine(a attachment) string {
	s := fmt.Sprintf("asset %d %s %s dl:%d", a.ID, a.Name, format.Size(a.Size), a.DownloadCount)
	if a.Type == "external" {
		s += " external"
	}
	return s
}

func releaseView(c *tools.Call, r release, full bool) string {
	var b format.B
	b.Raw("release " + strconv.FormatInt(r.ID, 10) + " " + r.TagName + "\n")
	if r.Name != r.TagName {
		b.KV("title", format.OneLine(r.Name, 120))
	}
	b.KV("flags", releaseFlags(r))
	b.KV("target", r.Target)
	if r.Author != nil {
		b.KV("by", r.Author.Login)
	}
	when := r.PublishedAt
	if when == "" {
		when = r.CreatedAt
	}
	b.KV("published", format.T(when))
	b.KV("url", r.HTMLURL)
	if len(r.Assets) > 0 {
		b.Raw("assets: " + strconv.Itoa(len(r.Assets)) + "\n")
		for _, a := range r.Assets {
			b.Raw("  " + assetLine(a) + "\n")
		}
	}
	if strings.TrimSpace(r.Body) != "" {
		limit := releaseBodyCap
		hint := "pass full=true"
		if full {
			limit, hint = releaseBodyFullCap, "release notes longer than 20000 chars are cut"
		}
		b.Raw("body:\n  " + strings.ReplaceAll(format.Trunc(strings.TrimSpace(r.Body), limit, hint), "\n", "\n  ") + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// ---- release lookup ----------------------------------------------------

// fetchRelease reads one release by id, tag or tag=latest.
func (t *releaseTool) fetchRelease(c *tools.Call, p *releaseParams) (*release, error) {
	var p2 string
	switch {
	case p.ID > 0:
		p2 = rp(c, "releases", strconv.FormatInt(p.ID, 10))
	case p.Tag == "latest":
		p2 = rp(c, "releases", "latest")
	case p.Tag != "":
		p2 = rp(c, "releases", "tags", p.Tag)
	default:
		return nil, nil
	}
	var r release
	if _, err := c.Env.Client.GetJSON(c.Ctx, p2, nil, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// releaseID resolves the release id of params (id, or tag through a lookup).
// The returned result is non-nil when the params are unusable.
func (t *releaseTool) releaseID(c *tools.Call, p *releaseParams) (int64, *tools.Result, error) {
	if p.ID > 0 {
		return p.ID, nil, nil
	}
	if p.Tag == "" {
		return 0, c.Fail("invalid: pass id or tag"), nil
	}
	r, err := t.fetchRelease(c, p)
	if err != nil {
		return 0, nil, err
	}
	return r.ID, nil, nil
}

// ---- actions -----------------------------------------------------------

func (t *releaseTool) list(c *tools.Call, p *releaseParams) (*tools.Result, error) {
	q := url.Values{}
	if p.Draft != nil {
		q.Set("draft", strconv.FormatBool(*p.Draft))
	}
	if p.Prerelease != nil {
		q.Set("pre-release", strconv.FormatBool(*p.Prerelease))
	}
	if p.Q != "" {
		q.Set("q", p.Q)
	}
	var items []release
	pg, err := c.Env.Client.List(c.Ctx, rp(c, "releases"), q, p.Page, listLimit(c, p.Limit, 0, 0), &items)
	if err != nil {
		return nil, err
	}
	var b format.B
	b.Raw(format.ListHeader("releases", pg) + "\n")
	for _, r := range items {
		b.Raw(releaseLine(r))
	}
	return &tools.Result{Text: strings.TrimRight(b.String(), "\n")}, nil
}

func (t *releaseTool) get(c *tools.Call, p *releaseParams) (*tools.Result, error) {
	if p.ID <= 0 && p.Tag == "" {
		return c.Fail("invalid: pass id, tag or tag=latest"), nil
	}
	r, err := t.fetchRelease(c, p)
	if err != nil {
		return nil, err
	}
	return &tools.Result{Text: releaseView(c, *r, p.Full)}, nil
}

func (t *releaseTool) create(c *tools.Call, p *releaseParams) (*tools.Result, error) {
	body := map[string]any{"tag_name": p.Tag}
	if p.Target != nil && *p.Target != "" {
		body["target_commitish"] = *p.Target
	}
	if p.Title != nil && *p.Title != "" {
		body["name"] = *p.Title
	}
	if p.Body != nil && *p.Body != "" {
		body["body"] = *p.Body
	}
	if p.Draft != nil {
		body["draft"] = *p.Draft
	}
	if p.Prerelease != nil {
		body["prerelease"] = *p.Prerelease
	}
	var r release
	if _, err := c.Env.Client.SendJSON(c.Ctx, http.MethodPost, rp(c, "releases"), nil, body, &r); err != nil {
		return nil, hintOn(err, "release exists for tag; use release.edit", http.StatusConflict)
	}
	line := fmt.Sprintf("ok: created release %d %s", r.ID, r.TagName)
	if f := releaseFlags(r); f != "" {
		line += " (" + f + ")"
	}
	if r.HTMLURL != "" {
		line += "  " + r.HTMLURL
	}
	return &tools.Result{Text: line}, nil
}

func (t *releaseTool) edit(c *tools.Call, p *releaseParams) (*tools.Result, error) {
	body := map[string]any{}
	var changed []string
	for _, f := range []struct {
		name, key string
		val       *string
	}{{"title", "name", p.Title}, {"body", "body", p.Body}, {"target", "target_commitish", p.Target}} {
		if f.val == nil {
			continue
		}
		if *f.val == "" {
			return c.Fail("invalid: %s cannot be emptied (the server ignores empty values); set a non-empty text", f.name), nil
		}
		body[f.key] = *f.val
		changed = append(changed, f.name)
	}
	if p.Draft != nil {
		body["draft"] = *p.Draft
		changed = append(changed, "draft")
	}
	if p.Prerelease != nil {
		body["prerelease"] = *p.Prerelease
		changed = append(changed, "prerelease")
	}
	if len(body) == 0 {
		return c.Fail("invalid: nothing to edit; pass title, body, target, draft or prerelease"), nil
	}
	id, bad, err := t.releaseID(c, p)
	if bad != nil || err != nil {
		return bad, err
	}
	var r release
	if _, err := c.Env.Client.SendJSON(c.Ctx, http.MethodPatch, rp(c, "releases", strconv.FormatInt(id, 10)), nil, body, &r); err != nil {
		return nil, err
	}
	return &tools.Result{Text: fmt.Sprintf("ok: edited release %d %s: %s", r.ID, r.TagName, strings.Join(changed, ","))}, nil
}

func (t *releaseTool) delete(c *tools.Call, p *releaseParams) (*tools.Result, error) {
	var path, what string
	switch {
	case p.ID > 0:
		path, what = rp(c, "releases", strconv.FormatInt(p.ID, 10)), "release "+strconv.FormatInt(p.ID, 10)
	case p.Tag != "" && p.Tag != "latest":
		path, what = rp(c, "releases", "tags", p.Tag), "release "+p.Tag
	default:
		return c.Fail("invalid: pass id or a tag"), nil
	}
	if _, err := c.Env.Client.Do(c.Ctx, forgejo.Request{Method: http.MethodDelete, Path: path}); err != nil {
		return nil, err
	}
	return &tools.Result{Text: "ok: deleted " + what + " (git tag may remain)"}, nil
}

func (t *releaseTool) assets(c *tools.Call, id int64) ([]attachment, error) {
	var items []attachment
	if _, err := c.Env.Client.GetJSON(c.Ctx, rp(c, "releases", strconv.FormatInt(id, 10), "assets"), nil, &items); err != nil {
		return nil, err
	}
	return items, nil
}

func (t *releaseTool) assetList(c *tools.Call, p *releaseParams) (*tools.Result, error) {
	id, bad, err := t.releaseID(c, p)
	if bad != nil || err != nil {
		return bad, err
	}
	items, err := t.assets(c, id)
	if err != nil {
		return nil, err
	}
	lo, hi, pg := window(len(items), p.Page, listLimit(c, p.Limit, 0, 100))
	var b format.B
	b.Raw(format.ListHeader("assets", pg) + "\n")
	for _, a := range items[lo:hi] {
		b.Raw(assetLine(a) + "\n")
	}
	return &tools.Result{Text: strings.TrimRight(b.String(), "\n")}, nil
}

func (t *releaseTool) assetUpload(c *tools.Call, p *releaseParams) (*tools.Result, error) {
	switch {
	case p.File == "" && p.URL == "":
		return c.Fail("invalid: pass file (local path) or url (external asset)"), nil
	case p.File != "" && p.URL != "":
		return c.Fail("invalid: file and url are mutually exclusive"), nil
	}
	id, bad, err := t.releaseID(c, p)
	if bad != nil || err != nil {
		return bad, err
	}
	reqPath := rp(c, "releases", strconv.FormatInt(id, 10), "assets")
	var req forgejo.Request
	if p.URL != "" {
		u, perr := url.Parse(p.URL)
		if perr != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return c.Fail("invalid: url must be an http(s) URL"), nil
		}
		name := p.Name
		if name == "" {
			name = path.Base(u.Path)
			if name == "." || name == "/" || name == "" {
				name = u.Host
			}
		}
		// The server tells a form from a raw body by the Content-Type, and a
		// raw body is always "a file" to it; so the external link is sent as
		// multipart/form-data with an EMPTY form (no `attachment` part).
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		if err := mw.Close(); err != nil {
			return c.Fail("%v", err), nil
		}
		req = forgejo.Request{
			Method:        http.MethodPost,
			Path:          reqPath,
			Query:         url.Values{"name": {name}, "external_url": {p.URL}},
			Body:          &buf,
			ContentLength: int64(buf.Len()),
			ContentType:   mw.FormDataContentType(),
			Timeout:       uploadTimeout,
		}
	} else {
		f, size, ferr := roots(c).OpenLocal(p.File)
		if ferr != nil {
			return c.Fail("%s", strings.TrimPrefix(ferr.Error(), "error: ")), nil
		}
		defer f.Close()
		name := p.Name
		if name == "" {
			name = filepath.Base(f.Name())
		}
		req = forgejo.Request{
			Method:        http.MethodPost,
			Path:          reqPath,
			Query:         url.Values{"name": {name}},
			Body:          f,
			ContentLength: size,
			ContentType:   "application/octet-stream",
			Timeout:       uploadTimeout,
		}
	}
	resp, err := c.Env.Client.Do(c.Ctx, req)
	if err != nil {
		return nil, err
	}
	var a attachment
	if derr := decodeJSON(resp.Body, &a); derr != nil {
		return c.Fail("%v", derr), nil
	}
	line := fmt.Sprintf("ok: uploaded asset %d %s", a.ID, a.Name)
	if a.Type == "external" {
		line = fmt.Sprintf("ok: linked external asset %d %s", a.ID, a.Name)
	} else {
		line += " " + format.Size(a.Size)
	}
	return &tools.Result{Text: line}, nil
}

func (t *releaseTool) assetEdit(c *tools.Call, p *releaseParams) (*tools.Result, error) {
	id, bad, err := t.releaseID(c, p)
	if bad != nil || err != nil {
		return bad, err
	}
	var a attachment
	if _, err := c.Env.Client.SendJSON(c.Ctx, http.MethodPatch,
		rp(c, "releases", strconv.FormatInt(id, 10), "assets", strconv.FormatInt(p.AssetID, 10)), nil,
		map[string]any{"name": p.Name}, &a); err != nil {
		return nil, err
	}
	return &tools.Result{Text: fmt.Sprintf("ok: renamed asset %d to %s", p.AssetID, p.Name)}, nil
}

func (t *releaseTool) assetDelete(c *tools.Call, p *releaseParams) (*tools.Result, error) {
	id, bad, err := t.releaseID(c, p)
	if bad != nil || err != nil {
		return bad, err
	}
	if _, err := c.Env.Client.Do(c.Ctx, forgejo.Request{
		Method: http.MethodDelete,
		Path:   rp(c, "releases", strconv.FormatInt(id, 10), "assets", strconv.FormatInt(p.AssetID, 10)),
	}); err != nil {
		return nil, err
	}
	return &tools.Result{Text: fmt.Sprintf("ok: deleted asset %d", p.AssetID)}, nil
}

func (t *releaseTool) assetDownload(c *tools.Call, p *releaseParams) (*tools.Result, error) {
	id, bad, err := t.releaseID(c, p)
	if bad != nil || err != nil {
		return bad, err
	}
	items, err := t.assets(c, id)
	if err != nil {
		return nil, err
	}
	var asset *attachment
	for i := range items {
		if items[i].ID == p.AssetID {
			asset = &items[i]
			break
		}
	}
	if asset == nil {
		return c.Fail("404 not found: asset %d in release %d", p.AssetID, id), nil
	}
	if asset.Type == "external" {
		return &tools.Result{Text: fmt.Sprintf("external asset: %s (not downloaded; credentials are not sent to other hosts)", asset.DownloadURL)}, nil
	}
	// Rule 5.7: only the URL path is used, rebuilt on FORGEJO_URL.
	repo, err := shared.RepoMeta(c.Ctx, c.Env.Client, c.Owner, c.Repo)
	if err != nil {
		return nil, err
	}
	sub := shared.RootSubPath(repo.HTMLURL, c.Owner, c.Repo)
	apath, ok := shared.AssetPath(asset.DownloadURL, sub)
	if !ok {
		return c.Fail("invalid: asset %d has no downloadable URL on this instance", asset.ID), nil
	}
	to := p.SaveTo
	if to == "" {
		to = asset.Name
	}
	return download(c, forgejo.Request{Method: http.MethodGet, Path: apath, API: "root", Timeout: uploadTimeout}, to)
}
