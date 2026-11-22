package content

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"

	"github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"
	"github.com/AndreyQuantum/forgejo-mcp/internal/format"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools/shared"
)

// Projection structs of the content area (DESIGN.md section 6.9). Only the
// JSON paths we print are declared; everything else is dropped by decoding.

type login struct {
	Login string `json:"login"`
}

// attachment is a release asset.
type attachment struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	Size          int64  `json:"size"`
	DownloadCount int64  `json:"download_count"`
	DownloadURL   string `json:"browser_download_url"`
	Type          string `json:"type"`
}

// release is the Release object.
type release struct {
	ID          int64        `json:"id"`
	TagName     string       `json:"tag_name"`
	Name        string       `json:"name"`
	Draft       bool         `json:"draft"`
	Prerelease  bool         `json:"prerelease"`
	Target      string       `json:"target_commitish"`
	PublishedAt string       `json:"published_at"`
	CreatedAt   string       `json:"created_at"`
	Author      *login       `json:"author"`
	Body        string       `json:"body"`
	HTMLURL     string       `json:"html_url"`
	Assets      []attachment `json:"assets"`
}

// contentsEntry is the ContentsResponse object.
type contentsEntry struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	SHA      string `json:"sha"`
	Type     string `json:"type"`
	Size     int64  `json:"size"`
	Target   string `json:"target"`
	Encoding string `json:"encoding"`
	Content  string `json:"content"`
}

// gitCommitRef is the commit part of FileResponse and friends.
type commitSHA struct {
	SHA string `json:"sha"`
}

// fileResponse is FileResponse / FileDeleteResponse.
type fileResponse struct {
	Content *contentsEntry `json:"content"`
	Commit  commitSHA      `json:"commit"`
}

// filesResponse is FilesResponse.
type filesResponse struct {
	Files  []contentsEntry `json:"files"`
	Commit commitSHA       `json:"commit"`
}

// commitUser is the git identity of a commit.
type commitUser struct {
	Name string `json:"name"`
	Date string `json:"date"`
}

// commitObj is the Commit object (list, git/commits/{sha}, compare).
type commitFile struct {
	Filename string `json:"filename"`
	Status   string `json:"status"`
}

type commitObj struct {
	SHA    string `json:"sha"`
	Commit struct {
		Message string     `json:"message"`
		Author  commitUser `json:"author"`
	} `json:"commit"`
	Author  *login       `json:"author"`
	Parents []commitSHA  `json:"parents"`
	Files   []commitFile `json:"files"`
	Stats   *struct {
		Additions int `json:"additions"`
		Deletions int `json:"deletions"`
		Total     int `json:"total"`
	} `json:"stats"`
	HTMLURL string `json:"html_url"`
}

// ---- helpers -----------------------------------------------------------

// rp builds an escaped path below /repos/{owner}/{repo}.
func rp(c *tools.Call, parts ...string) string {
	return forgejo.Path(append([]string{"repos", c.Owner, c.Repo}, parts...)...)
}

// listLimit returns the effective page size: the requested limit, else the
// configured default, capped at max.
func listLimit(c *tools.Call, requested, def, max int) int {
	n := requested
	if n <= 0 {
		n = def
	}
	if n <= 0 && c.Env.Cfg != nil {
		n = c.Env.Cfg.DefaultLimit
	}
	if n <= 0 {
		n = 20
	}
	if max > 0 && n > max {
		n = max
	}
	return n
}

// window returns the slice bounds of page/limit over n items and the Page.
func window(n, page, limit int) (lo, hi int, pg forgejo.Page) {
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 20
	}
	lo = min((page-1)*limit, n)
	hi = min(lo+limit, n)
	pg = forgejo.Page{Page: page, Limit: limit, Total: int64(n), Shown: hi - lo, More: hi < n}
	return lo, hi, pg
}

// cleanPath strips leading slashes from a repo path.
func cleanPath(p string) string { return strings.Trim(strings.TrimSpace(p), "/") }

// apiErr returns the *forgejo.APIError inside err, or nil.
func apiErr(err error) *forgejo.APIError {
	var ae *forgejo.APIError
	if errors.As(err, &ae) {
		return ae
	}
	return nil
}

// hintOn adds hint to an APIError of one of the given statuses when it has
// no more specific hint already, and returns err.
func hintOn(err error, hint string, statuses ...int) error {
	if ae := apiErr(err); ae != nil && ae.Hint == "" {
		for _, s := range statuses {
			if ae.Status == s {
				ae.Hint = hint
				break
			}
		}
	}
	return err
}

// roots builds the local-file roots of section 10.5 from the config.
func roots(c *tools.Call) *shared.FileRoots {
	cfg := c.Env.Cfg
	return &shared.FileRoots{
		SaveDir:   cfg.SaveDir,
		AllowCWD:  cfg.SaveCWD,
		TokenFile: cfg.TokenFile,
		Disabled:  cfg.Transport == "http" && !cfg.HTTPFiles,
	}
}

// lazyDownload runs the download only when the first byte is read, so a
// destination refused by SaveFile never causes a request.
type lazyDownload struct {
	start func(w *io.PipeWriter) error
	pr    *io.PipeReader
	pw    *io.PipeWriter
	once  sync.Once
	done  chan struct{}
	err   error

	skipped bool
}

func (l *lazyDownload) Read(p []byte) (int, error) {
	l.once.Do(func() {
		go func() {
			defer close(l.done)
			l.err = l.start(l.pw)
			l.pw.CloseWithError(l.err)
		}()
	})
	return l.pr.Read(p)
}

// download streams a GET straight into a NEW local file under the save roots
// (10.5). The destination is validated and created before the request is
// made, and a failed request removes the partial file.
func download(c *tools.Call, req forgejo.Request, to string) (*tools.Result, error) {
	rt := roots(c)
	if rt.Disabled {
		return c.Fail("save_to is disabled in HTTP mode (set FORGEJO_HTTP_FILES to enable)"), nil
	}
	pr, pw := io.Pipe()
	l := &lazyDownload{pr: pr, pw: pw, done: make(chan struct{})}
	l.start = func(w *io.PipeWriter) error {
		_, _, err := c.Env.Client.Download(c.Ctx, req, w)
		return err
	}
	final, n, err := rt.SaveFile(to, l)
	pr.Close()                             // unblocks a download that is still writing
	l.once.Do(func() { l.skipped = true }) // marks "never started" when SaveFile refused first
	if !l.skipped {
		<-l.done
		if l.err != nil {
			return nil, l.err
		}
	}
	if err != nil {
		return c.Fail("%s", strings.TrimPrefix(err.Error(), "error: ")), nil
	}
	return &tools.Result{Text: fmt.Sprintf("ok: saved %s (%s)", final, format.Size(n))}, nil
}

// defaultBranch returns the default branch of the resolved repo from the
// cached repo meta ("" when it cannot be read).
func defaultBranch(c *tools.Call) string {
	r, err := shared.RepoMeta(c.Ctx, c.Env.Client, c.Owner, c.Repo)
	if err != nil || r == nil {
		return ""
	}
	return r.DefaultBranch
}

// errStatus reports whether err is an APIError with the given status.
func errStatus(err error, status int) bool {
	ae := apiErr(err)
	return ae != nil && ae.Status == status
}

// statusLetter maps a Forgejo file status word to the one-letter code.
func statusLetter(s string) string {
	switch strings.ToLower(s) {
	case "added", "create", "created", "a":
		return "A"
	case "modified", "changed", "update", "updated", "m":
		return "M"
	case "removed", "deleted", "delete", "d":
		return "D"
	case "renamed", "r":
		return "R"
	case "copied", "c":
		return "C"
	case "":
		return "M"
	}
	return strings.ToUpper(s[:1])
}

// subject returns the first line of a commit message, capped at 120 chars.
func subject(msg string) string {
	first, _, _ := strings.Cut(strings.TrimSpace(msg), "\n")
	return format.OneLine(first, 120)
}

// globRegex converts a path glob (`*`, `**`, `?`) to a regexp.
func globRegex(glob string) (*regexp.Regexp, error) {
	var sb strings.Builder
	sb.WriteString("^")
	for i := 0; i < len(glob); i++ {
		switch ch := glob[i]; ch {
		case '*':
			if i+1 < len(glob) && glob[i+1] == '*' {
				i++
				if i+1 < len(glob) && glob[i+1] == '/' {
					i++
					sb.WriteString("(?:.*/)?")
				} else {
					sb.WriteString(".*")
				}
			} else {
				sb.WriteString("[^/]*")
			}
		case '?':
			sb.WriteString("[^/]")
		default:
			sb.WriteString(regexp.QuoteMeta(string(ch)))
		}
	}
	sb.WriteString("$")
	return regexp.Compile(sb.String())
}

// globMatcher returns a matcher for glob: a pattern without `/` matches the
// base name at any depth, otherwise the whole path.
func globMatcher(glob string) (func(string) bool, error) {
	re, err := globRegex(glob)
	if err != nil {
		return nil, err
	}
	whole := strings.Contains(glob, "/")
	return func(p string) bool {
		if whole {
			return re.MatchString(p)
		}
		if i := strings.LastIndex(p, "/"); i >= 0 {
			p = p[i+1:]
		}
		return re.MatchString(p)
	}, nil
}

// readEntry is one cached `file read` response (ETag revalidation).
type readEntry struct {
	etag      string
	body      []byte
	truncated bool
	size      int64
}

const (
	readCacheSize    = 32
	readCacheMaxBody = 1 << 20
)

// readCache keeps the last 32 raw reads so a repeated read costs a 304.
// Keys are namespaced by host and token (Client.CacheKey), so two tokens
// never share entries.
type readCache struct {
	mu    sync.Mutex
	m     map[string]*readEntry
	order []string
}

var reads = &readCache{}

func (rc *readCache) get(key string) *readEntry {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return rc.m[key]
}

func (rc *readCache) put(key string, e *readEntry) {
	if len(e.body) > readCacheMaxBody {
		return
	}
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if rc.m == nil {
		rc.m = map[string]*readEntry{}
	}
	if _, ok := rc.m[key]; !ok {
		rc.order = append(rc.order, key)
		for len(rc.order) > readCacheSize {
			delete(rc.m, rc.order[0])
			rc.order = rc.order[1:]
		}
	}
	rc.m[key] = e
}

func (rc *readCache) drop(key string) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	delete(rc.m, key)
	for i, k := range rc.order {
		if k == key {
			rc.order = append(rc.order[:i], rc.order[i+1:]...)
			break
		}
	}
}

// notModified reports whether err is the 304 answer of a conditional GET.
func notModified(err error) bool { return errStatus(err, http.StatusNotModified) }

// decodeJSON decodes a response body; an empty body is an error because the
// callers only use it for answers that must carry an object.
func decodeJSON(body []byte, out any) error {
	if len(bytes.TrimSpace(body)) == 0 {
		return errors.New("error: empty response")
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("error: invalid JSON in response: %w", err)
	}
	return nil
}
