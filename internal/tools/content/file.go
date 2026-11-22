package content

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"
	"github.com/AndreyQuantum/forgejo-mcp/internal/format"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools/shared"
)

// fileOp is one entry of `file commit ops`.
type fileOp struct {
	Op            string  `json:"op" jsonschema:"create, update, delete or rename"`
	Path          string  `json:"path" jsonschema:"file path"`
	Content       *string `json:"content,omitempty" jsonschema:"new text content"`
	ContentBase64 *string `json:"content_base64,omitempty" jsonschema:"new content, base64"`
	FromPath      string  `json:"from_path,omitempty" jsonschema:"rename source path"`
}

// fileParams are the parameters of the file tool (section 8.3).
type fileParams struct {
	Action        string   `json:"action"`
	Repo          string   `json:"repo,omitempty" jsonschema:"owner/name; default is the detected repo"`
	Path          string   `json:"path,omitempty" jsonschema:"file or directory path"`
	Ref           string   `json:"ref,omitempty" jsonschema:"branch, tag or sha; default branch when empty"`
	Branch        string   `json:"branch,omitempty" jsonschema:"branch to commit to; default branch when empty"`
	NewBranch     string   `json:"new_branch,omitempty" jsonschema:"create this branch for the commit"`
	Message       string   `json:"message,omitempty" jsonschema:"commit message"`
	Content       *string  `json:"content,omitempty" jsonschema:"file text"`
	ContentBase64 *string  `json:"content_base64,omitempty" jsonschema:"file content, base64"`
	SHA           string   `json:"sha,omitempty" jsonschema:"blob sha of the file being replaced; looked up when empty"`
	FromPath      string   `json:"from_path,omitempty" jsonschema:"rename source path"`
	Ops           []fileOp `json:"ops,omitempty" jsonschema:"file operations of one commit"`
	Diff          string   `json:"diff,omitempty" jsonschema:"unified diff text to apply"`
	Glob          string   `json:"glob,omitempty" jsonschema:"path glob, e.g. **/*.go"`
	Depth         int      `json:"depth,omitempty" jsonschema:"levels below path; 1 = direct children"`
	Recursive     *bool    `json:"recursive,omitempty" jsonschema:"descend into directories; default true"`
	Grep          string   `json:"grep,omitempty" jsonschema:"regex; return only matching lines with numbers"`
	StartLine     int      `json:"start_line,omitempty" jsonschema:"first line to show, from 1"`
	EndLine       int      `json:"end_line,omitempty" jsonschema:"last line to show"`
	MaxChars      int      `json:"max_chars,omitempty" jsonschema:"output size in chars, default 12000, max 100000"`
	Numbers       bool     `json:"numbers,omitempty" jsonschema:"prefix lines with their numbers"`
	Format        string   `json:"format,omitempty" jsonschema:"archive format: zip, tar.gz or bundle"`
	SaveTo        string   `json:"save_to,omitempty" jsonschema:"local file to write, under the save dir"`
	Page          int      `json:"page,omitempty" jsonschema:"page number from 1"`
	Limit         int      `json:"limit,omitempty" jsonschema:"page size"`
}

const (
	fileDefaultChars = 12000
	treePerPage      = 1000
	treeMaxPages     = 5
	binarySniff      = 8000
	lookupParallel   = 8
	lookupMaxBody    = 32 << 20
)

// registerFile registers the file tool.
func registerFile(r *tools.Registry) {
	t := &fileTool{}
	const base = "/repos/{owner}/{repo}"
	r.Add(tools.Spec{
		Name:        "file",
		Toolset:     "repos",
		Description: "Repo files: read, ls, tree, write, delete, multi-file commit, apply patch, archive.",
		Params:      fileParams{},
		RepoScoped:  true,
		Actions: []tools.Action{
			tools.NewAction("read", tools.Read, t.read,
				tools.Summary("read a file (line range, grep, size cap); a directory behaves like ls"),
				tools.Params("ref", "start_line", "end_line", "grep", "max_chars", "numbers"),
				tools.Required("path"),
				tools.Endpoints("GET "+base+"/raw/{filepath}")),
			tools.NewAction("ls", tools.Read, t.ls,
				tools.Summary("list one directory (default: the root)"),
				tools.Params("path", "ref"),
				tools.Endpoints("GET "+base+"/contents/{filepath}", "GET "+base+"/contents")),
			tools.NewAction("tree", tools.Read, t.tree,
				tools.Summary("recursive path listing with prefix, glob and depth filters"),
				tools.Params("path", "glob", "depth", "recursive", "ref", "page", "limit"),
				tools.Endpoints("GET "+base+"/git/trees/{sha}")),
			tools.NewAction("write", tools.Write, t.write,
				tools.Summary("create or update one file (creates when missing); from_path renames"),
				tools.Params("content", "content_base64", "branch", "new_branch", "sha", "from_path"),
				tools.Required("path", "message"),
				tools.Endpoints("POST "+base+"/contents/{filepath}", "PUT "+base+"/contents/{filepath}"),
				tools.Needs("repo write"),
				tools.Hint500("no write access, or the repo is a pull mirror")),
			tools.NewAction("delete", tools.Write, t.deleteFile,
				tools.Summary("delete one file in a commit"),
				tools.Params("branch", "new_branch", "sha"),
				tools.Required("path", "message"),
				tools.Endpoints("DELETE "+base+"/contents/{filepath}"),
				tools.Needs("repo write"),
				tools.Hint500("no write access, or the repo is a pull mirror")),
			tools.NewAction("commit", tools.Write, t.commit,
				tools.Summary("one commit with several create/update/delete/rename ops"),
				tools.Params("branch", "new_branch"),
				tools.Required("message", "ops"),
				tools.Endpoints("POST "+base+"/contents"),
				tools.Needs("repo write"),
				tools.Hint500("no write access, or the repo is a pull mirror")),
			tools.NewAction("patch", tools.Write, t.patch,
				tools.Summary("apply a unified diff as one commit"),
				tools.Params("branch", "new_branch"),
				tools.Required("diff", "message"),
				tools.Endpoints("POST "+base+"/diffpatch"),
				tools.Needs("repo write")),
			tools.NewAction("archive", tools.Read, t.archive,
				tools.Summary("download a repo archive to save_to (streamed to disk)"),
				tools.Params("ref", "format", "save_to"),
				tools.Endpoints("GET "+base+"/archive/{archive}")),
		},
	})
}

type fileTool struct{}

// ---- shared helpers ----------------------------------------------------

func refOf(s string) string { return shared.NormalizeRef(s) }

// lookup reads the contents object at path on ref; nil when it is absent.
// A non-empty directory yields an entry of Type "dir".
func lookup(c *tools.Call, path, ref string) (*contentsEntry, error) {
	q := url.Values{}
	if ref != "" {
		q.Set("ref", ref)
	}
	resp, err := c.Env.Client.Do(c.Ctx, forgejo.Request{
		Method: http.MethodGet,
		Path:   rp(c, "contents", forgejo.Wild(path)),
		Query:  q,
		// the body carries the whole base64 file; allow files up to ~24 MiB
		MaxBody: lookupMaxBody,
	})
	if err != nil {
		if errStatus(err, http.StatusNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if resp.Truncated {
		return nil, &forgejo.APIError{Message: "file too large to look up (over 32 MiB as base64)", Method: http.MethodGet, Path: path}
	}
	body := bytes.TrimSpace(resp.Body)
	switch {
	case len(body) == 0:
		return nil, nil
	case body[0] == '[':
		var arr []json.RawMessage
		if err := json.Unmarshal(body, &arr); err != nil || len(arr) == 0 {
			// an empty array is what an empty repo answers for any path
			return nil, nil
		}
		return &contentsEntry{Type: "dir", Path: path}, nil
	}
	var e contentsEntry
	if err := json.Unmarshal(body, &e); err != nil {
		return nil, &forgejo.APIError{Message: "invalid JSON in response: " + err.Error(), Method: http.MethodGet, Path: path}
	}
	return &e, nil
}

// contentB64 returns the base64 form of the content/content_base64 pair.
// provided is false when neither is set.
func contentB64(content, b64 *string) (out string, provided bool, err error) {
	switch {
	case content != nil && b64 != nil:
		return "", false, fmt.Errorf("invalid: pass content or content_base64, not both")
	case content != nil:
		return base64.StdEncoding.EncodeToString([]byte(*content)), true, nil
	case b64 != nil:
		clean := strings.Join(strings.Fields(*b64), "")
		if _, derr := base64.StdEncoding.DecodeString(clean); derr != nil {
			return "", false, fmt.Errorf("invalid: content_base64 is not valid base64")
		}
		return clean, true, nil
	}
	return "", false, nil
}

// branchLabel is the branch a change lands on, for the ok line.
func branchLabel(c *tools.Call, branch, newBranch string) string {
	switch {
	case newBranch != "":
		return newBranch
	case branch != "":
		return branch
	}
	return defaultBranch(c)
}

// commitBody fills the fields shared by every contents write.
func commitBody(m map[string]any, message, branch, newBranch string) map[string]any {
	m["message"] = message
	if b := refOf(branch); b != "" {
		m["branch"] = b
	}
	if newBranch != "" {
		m["new_branch"] = newBranch
	}
	return m
}

// writeFail adds the protected-branch hint of section 8.3 to 403 answers.
func writeFail(err error) error {
	return hintOn(err, "use new_branch, then pr create (the branch may be protected)", http.StatusForbidden)
}

func okCommit(sha string) string {
	if sha == "" {
		return ""
	}
	return " @" + format.SHA(sha, 8)
}

// ---- read --------------------------------------------------------------

func (t *fileTool) read(c *tools.Call, p *fileParams) (*tools.Result, error) {
	path := cleanPath(p.Path)
	if path == "" {
		return c.Fail("invalid: missing param path for file.read"), nil
	}
	ref := refOf(p.Ref)
	q := url.Values{}
	if ref != "" {
		q.Set("ref", ref)
	}
	key := c.Env.Client.CacheKey("file", c.Owner+"/"+c.Repo, ref, path)
	hdr := http.Header{}
	cached := reads.get(key)
	if cached != nil {
		hdr.Set("If-None-Match", cached.etag)
	}
	var body []byte
	var size int64
	truncated := false
	resp, err := c.Env.Client.Do(c.Ctx, forgejo.Request{
		Method: http.MethodGet,
		Path:   rp(c, "raw", forgejo.Wild(path)),
		Query:  q,
		Header: hdr,
	})
	switch {
	case err == nil:
		body, truncated = resp.Body, resp.Truncated
		size = int64(len(body))
		if cl, perr := strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64); perr == nil && cl > size {
			size = cl
		}
		if etag := resp.Header.Get("ETag"); etag != "" {
			reads.put(key, &readEntry{etag: etag, body: body, truncated: truncated, size: size})
		} else {
			reads.drop(key)
		}
	case notModified(err) && cached != nil:
		body, truncated, size = cached.body, cached.truncated, cached.size
	default:
		reads.drop(key)
		if errStatus(err, http.StatusNotFound) {
			// a directory has no raw form: fall back to the listing
			if res, lerr := t.lsAt(c, path, ref); lerr == nil && res != nil && !res.IsError {
				return res, nil
			}
		}
		return nil, err
	}
	return t.renderRead(c, p, path, ref, body, size, truncated), nil
}

func compileGrep(s string) *regexp.Regexp {
	if re, err := regexp.Compile(s); err == nil {
		return re
	}
	return regexp.MustCompile(regexp.QuoteMeta(s))
}

func (t *fileTool) renderRead(c *tools.Call, p *fileParams, path, ref string, body []byte, size int64, truncated bool) *tools.Result {
	head := "file " + path
	if ref != "" {
		head += " ref " + ref
	}
	head += " " + format.Size(size)
	if bytes.IndexByte(body[:min(binarySniff, len(body))], 0) >= 0 {
		return &tools.Result{Text: head + " binary (content not shown; use file archive or release assets to fetch it)"}
	}
	if len(body) == 0 {
		return &tools.Result{Text: head + " empty"}
	}
	budget := c.Chars(p.MaxChars, fileDefaultChars)
	text := string(body)
	if !utf8.ValidString(text) {
		text = strings.ToValidUTF8(text, "�")
	}
	text = strings.TrimSuffix(text, "\n")
	lines := strings.Split(text, "\n")
	total := len(lines)
	start, end := 1, total
	if p.StartLine > 0 {
		start = p.StartLine
	}
	if p.EndLine > 0 && p.EndLine < end {
		end = p.EndLine
	}
	if start > total {
		return &tools.Result{Text: fmt.Sprintf("%s lines 0/%d (start_line %d is beyond the end)", head, total, start)}
	}
	if end < start {
		return c.Fail("invalid: end_line %d is before start_line %d", p.EndLine, start)
	}
	suffix := ""
	if truncated {
		suffix = "\n[truncated: file larger than 8MB, only the first 8MB were read; use grep or start_line/end_line on a smaller file]"
	}

	if p.Grep != "" {
		re := compileGrep(p.Grep)
		var out []string
		matches := 0
		for i := start; i <= end; i++ {
			if re.MatchString(lines[i-1]) {
				matches++
				out = append(out, fmt.Sprintf("%d: %s", i, lines[i-1]))
			}
		}
		h := fmt.Sprintf("%s | matches %d of %d lines for %q", head, matches, total, p.Grep)
		if matches == 0 {
			return &tools.Result{Text: h + suffix}
		}
		return &tools.Result{Text: h + "\n" + format.Trunc(strings.Join(out, "\n"), budget, "narrow grep or pass start_line/end_line, max_chars") + suffix}
	}

	sel := lines[start-1 : end]
	if p.Numbers {
		numbered := make([]string, len(sel))
		for i, l := range sel {
			numbered[i] = strconv.Itoa(start+i) + ": " + l
		}
		sel = numbered
	}
	out := strings.Join(sel, "\n")
	shown := end
	if utf8.RuneCountInString(out) > budget {
		full := format.Trunc(out, budget, "pass start_line/end_line, grep or max_chars")
		if i := strings.LastIndex(full, "\n[truncated:"); i >= 0 {
			shown = start + strings.Count(full[:i], "\n")
		}
		out = full
	}
	h := fmt.Sprintf("%s lines %d-%d/%d", head, start, shown, total)
	return &tools.Result{Text: h + "\n" + out + suffix}
}

// ---- ls ----------------------------------------------------------------

func (t *fileTool) ls(c *tools.Call, p *fileParams) (*tools.Result, error) {
	return t.lsAt(c, cleanPath(p.Path), refOf(p.Ref))
}

func (t *fileTool) lsAt(c *tools.Call, path, ref string) (*tools.Result, error) {
	q := url.Values{}
	if ref != "" {
		q.Set("ref", ref)
	}
	rpath := rp(c, "contents")
	if path != "" {
		rpath = rp(c, "contents", forgejo.Wild(path))
	}
	resp, err := c.Env.Client.Do(c.Ctx, forgejo.Request{Method: http.MethodGet, Path: rpath, Query: q})
	if err != nil {
		return nil, err
	}
	body := bytes.TrimSpace(resp.Body)
	if len(body) > 0 && body[0] == '{' {
		return c.Fail("path is a file; use action=read"), nil
	}
	var entries []contentsEntry
	if len(body) > 0 {
		if err := json.Unmarshal(body, &entries); err != nil {
			return c.Fail("invalid JSON in response: %v", err), nil
		}
	}
	sort.SliceStable(entries, func(i, j int) bool {
		di, dj := entries[i].Type == "dir", entries[j].Type == "dir"
		if di != dj {
			return di
		}
		return entries[i].Name < entries[j].Name
	})
	label := "/"
	if path != "" {
		label = path + "/"
	}
	var b format.B
	parts := []string{"# entries " + strconv.Itoa(len(entries)), label}
	if ref != "" {
		parts = append(parts, "ref "+ref)
	}
	b.Line(parts...)
	for _, e := range entries {
		switch e.Type {
		case "dir":
			b.Raw("d " + e.Name + "/\n")
		case "symlink":
			b.Raw("l " + e.Name + " -> " + e.Target + "\n")
		case "submodule":
			b.Raw("s " + e.Name + "\n")
		default:
			b.Raw("f " + e.Name + " " + format.Size(e.Size) + "\n")
		}
	}
	return &tools.Result{Text: strings.TrimRight(b.String(), "\n")}, nil
}

// ---- tree --------------------------------------------------------------

type treeResponse struct {
	Tree []struct {
		Path string `json:"path"`
		Type string `json:"type"`
	} `json:"tree"`
	Truncated bool `json:"truncated"`
}

func (t *fileTool) tree(c *tools.Call, p *fileParams) (*tools.Result, error) {
	ref := refOf(p.Ref)
	if ref == "" {
		ref = defaultBranch(c)
	}
	if ref == "" {
		return c.Fail("invalid: cannot determine the default branch; pass ref"), nil
	}
	prefix := cleanPath(p.Path)
	recursive := p.Recursive == nil || *p.Recursive
	depth := p.Depth
	if !recursive && depth == 0 {
		depth = 1
	}
	fetchRecursive := recursive || prefix != ""
	var match func(string) bool
	if p.Glob != "" {
		m, err := globMatcher(p.Glob)
		if err != nil {
			return c.Fail("invalid: glob %q: %v", p.Glob, err), nil
		}
		match = m
	}

	var paths []string
	serverTruncated := false
	for page := 1; page <= treeMaxPages; page++ {
		var tr treeResponse
		q := url.Values{
			"recursive": {strconv.FormatBool(fetchRecursive)},
			"per_page":  {strconv.Itoa(treePerPage)},
			"page":      {strconv.Itoa(page)},
		}
		if _, err := c.Env.Client.GetJSON(c.Ctx, rp(c, "git", "trees", ref), q, &tr); err != nil {
			if errStatus(err, http.StatusBadRequest) && len(paths) == 0 {
				// the tree endpoint answers 400 for everything, including an
				// unknown ref and an empty repository
				return nil, hintOn(err, "check ref; an empty repository has no tree", http.StatusBadRequest)
			}
			return nil, err
		}
		for _, e := range tr.Tree {
			rel := e.Path
			if prefix != "" {
				if e.Path == prefix || !strings.HasPrefix(e.Path, prefix+"/") {
					continue
				}
				rel = e.Path[len(prefix)+1:]
			}
			if depth > 0 && strings.Count(rel, "/")+1 > depth {
				continue
			}
			if match != nil && !match(e.Path) {
				continue
			}
			if e.Type == "tree" {
				paths = append(paths, e.Path+"/")
			} else {
				paths = append(paths, e.Path)
			}
		}
		serverTruncated = tr.Truncated
		if !tr.Truncated {
			break
		}
	}
	lo, hi, pg := window(len(paths), p.Page, listLimit(c, p.Limit, 0, 100))
	var b format.B
	b.Raw(format.ListHeader("paths", pg) + "\n")
	for _, s := range paths[lo:hi] {
		b.Raw(s + "\n")
	}
	if serverTruncated {
		b.Raw("[truncated: the server tree is cut after " + strconv.Itoa(treeMaxPages*treePerPage) + " entries; narrow with path]\n")
	}
	return &tools.Result{Text: strings.TrimRight(b.String(), "\n")}, nil
}

// ---- write / delete ----------------------------------------------------

func (t *fileTool) write(c *tools.Call, p *fileParams) (*tools.Result, error) {
	path := cleanPath(p.Path)
	if path == "" || p.Message == "" {
		return c.Fail("invalid: path and message are required"), nil
	}
	b64, provided, err := contentB64(p.Content, p.ContentBase64)
	if err != nil {
		return c.Fail("%s", strings.TrimPrefix(err.Error(), "error: ")), nil
	}
	from := cleanPath(p.FromPath)
	if !provided && from == "" {
		return c.Fail("invalid: pass content or content_base64"), nil
	}
	ref := refOf(p.Branch)
	sha := p.SHA
	update := sha != "" || from != ""
	op := "updated"
	if from != "" {
		op = "renamed " + from + " ->"
		cur, lerr := lookup(c, from, ref)
		if lerr != nil {
			return nil, lerr
		}
		if cur == nil || cur.Type == "dir" {
			return c.Fail("404 not found: from_path %s is not a file on %s", from, orDefault(ref)), nil
		}
		if sha == "" {
			sha = cur.SHA
		}
		if !provided {
			if cur.Content == "" && cur.Size > 0 {
				return c.Fail("invalid: %s is too large to copy for a rename; pass content", from), nil
			}
			b64 = strings.Join(strings.Fields(cur.Content), "")
		}
	} else if sha == "" {
		cur, lerr := lookup(c, path, ref)
		if lerr != nil {
			return nil, lerr
		}
		switch {
		case cur == nil:
			update, op = false, "created"
		case cur.Type == "dir":
			return c.Fail("invalid: %s is a directory", path), nil
		default:
			update, sha = true, cur.SHA
		}
	}

	body := commitBody(map[string]any{"content": b64}, p.Message, p.Branch, p.NewBranch)
	method := http.MethodPost
	if update {
		method = http.MethodPut
		body["sha"] = sha
		if from != "" {
			body["from_path"] = from
		}
	}
	var fr fileResponse
	if _, err := c.Env.Client.SendJSON(c.Ctx, method, rp(c, "contents", forgejo.Wild(path)), nil, body, &fr); err != nil {
		return nil, writeFail(err)
	}
	line := fmt.Sprintf("ok: %s %s on %s%s", op, path, branchLabel(c, p.Branch, p.NewBranch), okCommit(fr.Commit.SHA))
	if fr.Content != nil && fr.Content.SHA != "" {
		line += " (blob " + format.SHA(fr.Content.SHA, 8) + ")"
	}
	return &tools.Result{Text: line}, nil
}

func orDefault(ref string) string {
	if ref == "" {
		return "the default branch"
	}
	return ref
}

func (t *fileTool) deleteFile(c *tools.Call, p *fileParams) (*tools.Result, error) {
	path := cleanPath(p.Path)
	if path == "" || p.Message == "" {
		return c.Fail("invalid: path and message are required"), nil
	}
	sha := p.SHA
	if sha == "" {
		cur, err := lookup(c, path, refOf(p.Branch))
		if err != nil {
			return nil, err
		}
		if cur == nil || cur.Type == "dir" {
			return c.Fail("404 not found: %s is not a file on %s", path, orDefault(refOf(p.Branch))), nil
		}
		sha = cur.SHA
	}
	body := commitBody(map[string]any{"sha": sha}, p.Message, p.Branch, p.NewBranch)
	resp, err := c.Env.Client.Do(c.Ctx, forgejo.Request{
		Method: http.MethodDelete,
		Path:   rp(c, "contents", forgejo.Wild(path)),
		JSON:   body,
	})
	if err != nil {
		return nil, writeFail(err)
	}
	var fr fileResponse
	if len(bytes.TrimSpace(resp.Body)) > 0 {
		_ = json.Unmarshal(resp.Body, &fr)
	}
	return &tools.Result{Text: fmt.Sprintf("ok: deleted %s on %s%s", path, branchLabel(c, p.Branch, p.NewBranch), okCommit(fr.Commit.SHA))}, nil
}

// ---- commit (multi-file) ------------------------------------------------

type changeOp struct {
	Operation string `json:"operation"`
	Path      string `json:"path"`
	Content   string `json:"content,omitempty"`
	SHA       string `json:"sha,omitempty"`
	FromPath  string `json:"from_path,omitempty"`
}

func (t *fileTool) commit(c *tools.Call, p *fileParams) (*tools.Result, error) {
	if p.Message == "" || len(p.Ops) == 0 {
		return c.Fail("invalid: message and ops are required"), nil
	}
	ref := refOf(p.Branch)
	ops := make([]changeOp, len(p.Ops))
	// which path each op must look up to learn its blob sha ("" = none)
	lookups := make([]string, len(p.Ops))
	for i, o := range p.Ops {
		path := cleanPath(o.Path)
		if path == "" {
			return c.Fail("invalid: ops[%d]: path is required", i), nil
		}
		b64, provided, err := contentB64(o.Content, o.ContentBase64)
		if err != nil {
			return c.Fail("ops[%d]: %s", i, strings.TrimPrefix(err.Error(), "error: ")), nil
		}
		switch strings.ToLower(o.Op) {
		case "create":
			if !provided {
				return c.Fail("invalid: ops[%d] create %s: content or content_base64 is required", i, path), nil
			}
			ops[i] = changeOp{Operation: "create", Path: path, Content: b64}
		case "update":
			if !provided {
				return c.Fail("invalid: ops[%d] update %s: content or content_base64 is required", i, path), nil
			}
			ops[i] = changeOp{Operation: "update", Path: path, Content: b64}
			lookups[i] = path
		case "delete":
			ops[i] = changeOp{Operation: "delete", Path: path}
			lookups[i] = path
		case "rename":
			from := cleanPath(o.FromPath)
			if from == "" {
				return c.Fail("invalid: ops[%d] rename %s: from_path is required", i, path), nil
			}
			ops[i] = changeOp{Operation: "update", Path: path, FromPath: from, Content: b64}
			lookups[i] = from
		default:
			return c.Fail("invalid: ops[%d]: op %q; valid: create,update,delete,rename", i, o.Op), nil
		}
	}

	// resolve blob shas (and, for renames without new content, the current
	// content) with at most 8 parallel lookups
	entries := make([]*contentsEntry, len(ops))
	errs := make([]error, len(ops))
	sem := make(chan struct{}, lookupParallel)
	var wg sync.WaitGroup
	for i, lp := range lookups {
		if lp == "" {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			entries[i], errs[i] = lookup(c, lp, ref)
		}()
	}
	wg.Wait()
	for i, lp := range lookups {
		if lp == "" {
			continue
		}
		if errs[i] != nil {
			return nil, errs[i]
		}
		e := entries[i]
		if e == nil || e.Type == "dir" {
			return c.Fail("404 not found: ops[%d] %s: %s is not a file on %s", i, p.Ops[i].Op, lp, orDefault(ref)), nil
		}
		ops[i].SHA = e.SHA
		if ops[i].Operation == "update" && ops[i].FromPath != "" && ops[i].Content == "" {
			if e.Content == "" && e.Size > 0 {
				return c.Fail("invalid: ops[%d] %s is too large to copy for a rename; pass content", i, lp), nil
			}
			ops[i].Content = strings.Join(strings.Fields(e.Content), "")
		}
	}

	body := commitBody(map[string]any{"files": ops}, p.Message, p.Branch, p.NewBranch)
	var fr filesResponse
	if _, err := c.Env.Client.SendJSON(c.Ctx, http.MethodPost, rp(c, "contents"), nil, body, &fr); err != nil {
		return nil, writeFail(err)
	}
	noun := "files"
	if len(ops) == 1 {
		noun = "file"
	}
	return &tools.Result{Text: fmt.Sprintf("ok: committed %d %s on %s%s", len(ops), noun, branchLabel(c, p.Branch, p.NewBranch), okCommit(fr.Commit.SHA))}, nil
}

// ---- patch -------------------------------------------------------------

func (t *fileTool) patch(c *tools.Call, p *fileParams) (*tools.Result, error) {
	if strings.TrimSpace(p.Diff) == "" || p.Message == "" {
		return c.Fail("invalid: diff and message are required"), nil
	}
	branch := refOf(p.Branch)
	if branch == "" {
		// the handler does not default the branch the way /contents does
		branch = defaultBranch(c)
	}
	if branch == "" {
		return c.Fail("invalid: cannot determine the default branch; pass branch"), nil
	}
	body := map[string]any{"content": p.Diff, "sha": "x", "message": p.Message, "branch": branch}
	if p.NewBranch != "" {
		body["new_branch"] = p.NewBranch
	}
	var fr fileResponse
	if _, err := c.Env.Client.SendJSON(c.Ctx, http.MethodPost, rp(c, "diffpatch"), nil, body, &fr); err != nil {
		hintForce(err)
		return nil, writeFail(err)
	}
	return &tools.Result{Text: "ok: applied patch" + okCommit(fr.Commit.SHA) + " on " + branchLabel(c, branch, p.NewBranch)}, nil
}

// hintForce sets the diffpatch 422 hint of section 8.3 (a patch that does
// not apply, or a changed file).
func hintForce(err error) {
	if ae := apiErr(err); ae != nil && ae.Status == http.StatusUnprocessableEntity {
		ae.Hint = "file changed since read; re-read the file and rebuild the diff"
	}
}

// ---- archive -----------------------------------------------------------

func (t *fileTool) archive(c *tools.Call, p *fileParams) (*tools.Result, error) {
	ref := refOf(p.Ref)
	if ref == "" {
		ref = defaultBranch(c)
	}
	if ref == "" {
		return c.Fail("invalid: cannot determine the default branch; pass ref"), nil
	}
	ext := p.Format
	if ext == "" {
		ext = "zip"
	}
	switch ext {
	case "zip", "tar.gz", "bundle":
	default:
		return c.Fail("invalid: format %q; valid: zip,tar.gz,bundle", ext), nil
	}
	to := p.SaveTo
	if to == "" {
		to = c.Repo + "-" + strings.NewReplacer("/", "-", "\\", "-").Replace(ref) + "." + ext
	}
	return download(c, forgejo.Request{
		Method:  http.MethodGet,
		Path:    rp(c, "archive", forgejo.Wild(ref+"."+ext)),
		Timeout: 10 * time.Minute,
	}, to)
}
