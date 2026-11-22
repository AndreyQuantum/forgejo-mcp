package meta

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/AndreyQuantum/forgejo-mcp/internal/endpoints"
	"github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"
	"github.com/AndreyQuantum/forgejo-mcp/internal/format"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools/shared"
)

// apiParams are the parameters of forgejo_api (section 9.1).
type apiParams struct {
	Method   string         `json:"method" jsonschema:"GET, POST, PUT, PATCH, DELETE or HEAD"`
	Path     string         `json:"path" jsonschema:"path relative to /api/v1, e.g. /repos/o/r/topics; /forgejo/v1/... selects the Forgejo API"`
	Query    map[string]any `json:"query,omitempty" jsonschema:"query parameters; arrays repeat the key"`
	Body     any            `json:"body,omitempty" jsonschema:"JSON body; a string is sent raw as text/plain"`
	Pick     []string       `json:"pick,omitempty" jsonschema:"keep only these field paths, e.g. number,user.login:by,labels[].name"`
	AllPages bool           `json:"all_pages,omitempty" jsonschema:"GET only: follow pages until the end"`
	MaxPages int            `json:"max_pages,omitempty" jsonschema:"all_pages page limit, default 5, max 20"`
	MaxChars int            `json:"max_chars,omitempty" jsonschema:"output size in chars, default 12000, max 100000"`
	Raw      bool           `json:"raw,omitempty" jsonschema:"keep every field (no noise stripping)"`
	SaveTo   string         `json:"save_to,omitempty" jsonschema:"write the response body to this file (GET)"`
	Confirm  string         `json:"confirm,omitempty" jsonschema:"destructive DELETE: the exact canonical path"`
	Timeout  int            `json:"timeout,omitempty" jsonschema:"seconds, default 30, max 600"`
}

const (
	apiDefaultChars = 12000
	apiMaxPages     = 20
	apiDefaultPages = 5
	apiMaxTimeout   = 600
)

// registerAPI registers forgejo_api.
func registerAPI(r *tools.Registry) {
	pol := &policy{reg: r}
	a := &apiTool{pol: pol}
	r.Add(tools.Spec{
		Name:        "forgejo_api",
		Toolset:     "meta",
		Description: "Raw Forgejo API call (method, path, query, body) with field pick and paging. Use for anything without a dedicated tool.",
		Params:      apiParams{},
		Single:      true,
		Actions: []tools.Action{
			tools.NewAction("run", tools.Write, a.run,
				tools.Summary("raw Forgejo API call"),
				tools.Params("method", "path", "query", "body", "pick", "all_pages", "max_pages", "max_chars", "raw", "save_to", "confirm", "timeout"),
				tools.Required("method", "path"),
				tools.ModeFor(func(in any) tools.Mode {
					p, ok := in.(*apiParams)
					if !ok {
						return tools.Write
					}
					return pol.classify(p.Method, p.Path)
				}),
			),
		},
	})
}

type apiTool struct{ pol *policy }

func (t *apiTool) run(c *tools.Call, p *apiParams) (*tools.Result, error) {
	cfg := c.Env.Cfg
	d := t.pol.evaluate(cfg, p.Method, p.Path, p.Confirm)
	if d.Err != "" {
		return &tools.Result{Text: d.Err, IsError: true}, nil
	}
	if p.SaveTo != "" && d.Method != http.MethodGet {
		return c.Fail("invalid: save_to needs method GET"), nil
	}
	if p.AllPages && d.Method != http.MethodGet {
		return c.Fail("invalid: all_pages needs method GET"), nil
	}
	if p.AllPages && p.SaveTo != "" {
		return c.Fail("invalid: all_pages and save_to cannot be combined"), nil
	}
	q, err := queryValues(p.Query)
	if err != nil {
		return c.Fail("%s", err.Error()), nil
	}
	req := forgejo.Request{
		Method: d.Method,
		Path:   forgejo.EscapeKeepPct(d.Canon),
		Query:  q,
		API:    d.API,
	}
	if p.Timeout > 0 {
		req.Timeout = time.Duration(min(p.Timeout, apiMaxTimeout)) * time.Second
	}
	switch b := p.Body.(type) {
	case nil:
	case string:
		req.Body = strings.NewReader(b)
		req.ContentLength = int64(len(b))
		req.ContentType = "text/plain"
	default:
		req.JSON = b
	}
	label := d.Method + " " + d.Canon
	if d.API == "forgejo" {
		label = d.Method + " /forgejo/v1" + d.Canon
	}
	budget := c.Chars(p.MaxChars, apiDefaultChars)

	var res *tools.Result
	switch {
	case p.SaveTo != "":
		res, err = t.save(c, req, p.SaveTo)
	case p.AllPages:
		res, err = t.allPages(c, p, req, label, budget)
	default:
		var resp *forgejo.Response
		if resp, err = c.Env.Client.Do(c.Ctx, req); err == nil {
			res = t.render(c, p, resp, label, budget)
		}
	}
	if err != nil {
		return nil, t.annotate404(c, d, err)
	}
	return res, nil
}

// annotate404 adds the "endpoint may not exist on this server" hint of 9.1(2).
func (t *apiTool) annotate404(c *tools.Call, d decision, err error) error {
	var ae *forgejo.APIError
	if !errors.As(err, &ae) || ae.Status != http.StatusNotFound {
		return err
	}
	v, verr := c.Env.Client.ServerVersion(c.Ctx)
	if verr != nil {
		return err
	}
	ixVer, _, _ := endpoints.Header()
	ixMajor := 0
	if iv, perr := forgejo.ParseVersion(ixVer); perr == nil {
		ixMajor = iv.Major
	}
	switch {
	case d.Op == nil:
		ae.Hint = "endpoint may not exist on this server (version " + v.String() + ")"
	case ixMajor > v.Major:
		ae.Hint = fmt.Sprintf("endpoint may not exist on this server (version %s); forgejo_endpoints lists it for >= major %d", v.String(), ixMajor)
	}
	return ae
}

// queryValues converts the free-form query object to url.Values.
func queryValues(m map[string]any) (url.Values, error) {
	if len(m) == 0 {
		return nil, nil
	}
	out := url.Values{}
	for k, v := range m {
		switch x := v.(type) {
		case nil:
		case []any:
			for _, e := range x {
				s, ok := scalar(e)
				if !ok {
					return nil, fmt.Errorf("invalid: query.%s must hold strings, numbers or booleans", k)
				}
				out.Add(k, s)
			}
		default:
			s, ok := scalar(v)
			if !ok {
				return nil, fmt.Errorf("invalid: query.%s must be a string, number, boolean or array", k)
			}
			out.Set(k, s)
		}
	}
	return out, nil
}

func scalar(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case bool:
		return strconv.FormatBool(x), true
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), true
	case json.Number:
		return x.String(), true
	}
	return "", false
}

// decodeJSON decodes with UseNumber so large ids survive.
func decodeJSON(b []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

func compactJSON(v any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return strings.TrimRight(buf.String(), "\n"), nil
}

// project applies unwrap/pick/noise stripping (9.2, 9.3) to a decoded value.
// total is the wrapper's total_count (-1 when unknown); n is the number of
// list items (-1 when the result is not a list).
func project(p *apiParams, v any) (text string, total int64, n int, err error) {
	total, n = -1, -1
	if !p.Raw {
		maskSecrets(v)
	}
	switch {
	case len(p.Pick) > 0:
		un, tc := format.Unwrap(v)
		total = tc
		if arr, ok := un.([]any); ok {
			n = len(arr)
		}
		text, err = format.Pick(un, p.Pick)
	case p.Raw:
		if arr, ok := v.([]any); ok {
			n = len(arr)
		}
		text, err = compactJSON(v)
	default:
		out, tc := format.StripNoise(v)
		total = tc
		if arr, ok := out.([]any); ok {
			n = len(arr)
		}
		text, err = compactJSON(out)
	}
	return text, total, n, err
}

// maskSecrets replaces non-empty secret values (webhook authorization
// headers) in a decoded JSON value, in place, unless the caller asked for raw.
func maskSecrets(v any) {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			if s, ok := e.(string); ok && s != "" && (k == "authorization_header" || k == "secret") {
				x[k] = "(hidden)"
				continue
			}
			maskSecrets(e)
		}
	case []any:
		for _, e := range x {
			maskSecrets(e)
		}
	}
}

type contentKind int

const (
	kindText contentKind = iota
	kindJSON
	kindBinary
)

func classify(h http.Header, body []byte) (contentKind, string) {
	ct := h.Get("Content-Type")
	mt, _, _ := mime.ParseMediaType(ct)
	mt = strings.ToLower(mt)
	switch {
	case strings.Contains(mt, "json"):
		return kindJSON, mt
	case strings.HasPrefix(mt, "text/") || strings.Contains(mt, "xml") || strings.Contains(mt, "diff") ||
		strings.Contains(mt, "patch") || strings.Contains(mt, "yaml") || strings.Contains(mt, "javascript"):
		return kindText, mt
	case strings.HasPrefix(mt, "image/") || strings.HasPrefix(mt, "audio/") || strings.HasPrefix(mt, "video/") ||
		strings.Contains(mt, "zip") || strings.Contains(mt, "gzip") || strings.Contains(mt, "tar") ||
		strings.Contains(mt, "octet-stream") || strings.Contains(mt, "pdf") || strings.Contains(mt, "zstd"):
		return kindBinary, mt
	}
	if bytes.IndexByte(body, 0) >= 0 || !utf8.Valid(body) {
		return kindBinary, mt
	}
	t := bytes.TrimSpace(body)
	if len(t) > 0 && (t[0] == '{' || t[0] == '[') && json.Valid(t) {
		return kindJSON, mt
	}
	return kindText, mt
}

// render turns one successful response into the tool result (9.1 step 3).
func (t *apiTool) render(c *tools.Call, p *apiParams, resp *forgejo.Response, label string, budget int) *tools.Result {
	body := bytes.TrimSpace(resp.Body)
	if len(body) == 0 {
		return &tools.Result{Text: fmt.Sprintf("ok (%d)", resp.Status)}
	}
	kind, mt := classify(resp.Header, body)
	size := len(resp.Body)
	hdr := fmt.Sprintf("# %d %s %s", resp.Status, label, format.Size(int64(size)))
	switch kind {
	case kindBinary:
		n := size
		if cl, err := strconv.Atoi(resp.Header.Get("Content-Length")); err == nil && cl > n {
			n = cl
		}
		if mt == "" {
			mt = "data"
		}
		return &tools.Result{Text: fmt.Sprintf("binary %s %d bytes; pass save_to=<path>", mt, n)}
	case kindText:
		return &tools.Result{Text: hdr + "\n" + cutNote(c, string(resp.Body), budget, resp.Truncated)}
	}
	v, err := decodeJSON(resp.Body)
	if err != nil {
		if resp.Truncated {
			return &tools.Result{Text: hdr + " (response larger than the 8 MiB read limit)\n" + format.Trunc(string(resp.Body), budget, "raise max_chars or use pick on a smaller response")}
		}
		return &tools.Result{Text: hdr + "\n" + format.Trunc(string(resp.Body), budget, "raise max_chars")}
	}
	text, total, n, err := project(p, v)
	if err != nil {
		return c.Fail("%s", err.Error())
	}
	if total < 0 {
		total = resp.Total
	}
	if n >= 0 {
		hdr = listHeader(c, p, resp.Status, label, int64(size), n, total)
	}
	return &tools.Result{Text: hdr + "\n" + cutNote(c, text, budget, false)}
}

func cutNote(_ *tools.Call, s string, budget int, srcTruncated bool) string {
	how := "narrow with pick or raise max_chars"
	if srcTruncated {
		how = "response exceeded the 8 MiB read limit; use pick, query filters or save_to"
	}
	return format.Trunc(s, budget, how)
}

// listHeader renders `# 200 GET /path 20/134 items page 1 (more: page=2)`.
func listHeader(c *tools.Call, p *apiParams, status int, label string, size int64, n int, total int64) string {
	page := 1
	if v, ok := p.Query["page"]; ok {
		if s, ok := scalar(v); ok {
			if x, err := strconv.Atoi(s); err == nil && x > 0 {
				page = x
			}
		}
	}
	limit := 0
	if v, ok := p.Query["limit"]; ok {
		if s, ok := scalar(v); ok {
			limit, _ = strconv.Atoi(s)
		}
	}
	if limit <= 0 {
		def, _ := c.Env.Client.Limits(c.Ctx)
		limit = def
	} else if _, maxLimit := c.Env.Client.Limits(c.Ctx); maxLimit > 0 && limit > maxLimit {
		limit = maxLimit
	}
	var more bool
	var count string
	if total >= 0 {
		more = int64(page-1)*int64(limit)+int64(n) < total
		count = fmt.Sprintf("%d/%d", n, total)
	} else {
		more = n >= limit && limit > 0
		count = strconv.Itoa(n)
	}
	h := fmt.Sprintf("# %d %s %s items page %d", status, label, count, page)
	if more {
		h += fmt.Sprintf(" (more: page=%d)", page+1)
	}
	return h
}

// allPages follows page=1.. for a GET list (9.4).
func (t *apiTool) allPages(c *tools.Call, p *apiParams, req forgejo.Request, label string, budget int) (*tools.Result, error) {
	maxPages := p.MaxPages
	if maxPages <= 0 {
		maxPages = apiDefaultPages
	}
	maxPages = min(maxPages, apiMaxPages)
	_, srvMax := c.Env.Client.Limits(c.Ctx)
	limit := srvMax
	if v, ok := p.Query["limit"]; ok {
		if s, ok := scalar(v); ok {
			if x, err := strconv.Atoi(s); err == nil && x > 0 {
				limit = min(x, srvMax)
			}
		}
	}
	page := 1
	if v, ok := p.Query["page"]; ok {
		if s, ok := scalar(v); ok {
			if x, err := strconv.Atoi(s); err == nil && x > 0 {
				page = x
			}
		}
	}
	var items []any
	var total int64 = -1
	fetched, stopped := 0, "end"
	var last *forgejo.Response
	for fetched < maxPages {
		q := url.Values{}
		for k, vs := range req.Query {
			q[k] = append([]string(nil), vs...)
		}
		q.Set("page", strconv.Itoa(page))
		q.Set("limit", strconv.Itoa(limit))
		r := req
		r.Query = q
		resp, err := c.Env.Client.Do(c.Ctx, r)
		if err != nil {
			return nil, err
		}
		last = resp
		v, derr := decodeJSON(resp.Body)
		if derr != nil {
			return c.Fail("all_pages needs a list response"), nil
		}
		un, tc := format.Unwrap(v)
		arr, ok := un.([]any)
		if !ok {
			return c.Fail("all_pages needs a list response"), nil
		}
		fetched++
		items = append(items, arr...)
		switch {
		case tc >= 0:
			total = tc
		case resp.Total >= 0:
			total = resp.Total
		}
		if len(arr) < limit || (total >= 0 && int64(len(items)) >= total) {
			stopped = "end"
			break
		}
		page++
		if fetched >= maxPages {
			stopped = "max_pages"
			break
		}
		if text, _, _, perr := project(p, items); perr == nil && utf8.RuneCountInString(text) > budget {
			stopped = "chars"
			break
		}
	}
	text, _, n, err := project(p, items)
	if err != nil {
		return c.Fail("%s", err.Error()), nil
	}
	_ = n
	status := 200
	if last != nil {
		status = last.Status
	}
	count := strconv.Itoa(len(items))
	if total >= 0 {
		count = fmt.Sprintf("%d/%d", len(items), total)
	}
	hdr := fmt.Sprintf("# %d %s %s items\npages: %d/%d fetched, %d items, stopped: %s", status, label, count, fetched, maxPages, len(items), stopped)
	return &tools.Result{Text: hdr + "\n" + format.Trunc(text, budget, "narrow with pick or raise max_chars")}, nil
}

// save streams a GET body to a spool file first and then moves it to a NEW
// local file under the save roots (10.5). Spooling first means a failed
// request (404, 403) or a refused destination (exists, outside the roots)
// leaves nothing behind and never makes the client retry the GET.
func (t *apiTool) save(c *tools.Call, req forgejo.Request, to string) (*tools.Result, error) {
	cfg := c.Env.Cfg
	roots := &shared.FileRoots{
		SaveDir:   cfg.SaveDir,
		AllowCWD:  cfg.SaveCWD,
		TokenFile: cfg.TokenFile,
		Disabled:  cfg.Transport == "http" && !cfg.HTTPFiles,
	}
	if roots.Disabled {
		return c.Fail("save_to is disabled in HTTP mode (set FORGEJO_HTTP_FILES to enable)"), nil
	}
	spool, err := os.CreateTemp("", "forgejo-mcp-dl-*")
	if err != nil {
		return c.Fail("cannot create a temporary file: %v", err), nil
	}
	defer func() {
		spool.Close()
		os.Remove(spool.Name())
	}()
	if _, _, err := c.Env.Client.Download(c.Ctx, req, spool); err != nil {
		return nil, err
	}
	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		return c.Fail("%v", err), nil
	}
	path, n, err := roots.SaveFile(to, spool)
	if err != nil {
		return c.Fail("%s", strings.TrimPrefix(err.Error(), "error: ")), nil
	}
	return &tools.Result{Text: fmt.Sprintf("ok: saved %s (%s)", path, format.Size(n))}, nil
}
