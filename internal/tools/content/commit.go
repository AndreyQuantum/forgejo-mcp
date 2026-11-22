package content

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"
	"github.com/AndreyQuantum/forgejo-mcp/internal/format"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
)

// commitParams are the parameters of the commit tool (section 8.3).
type commitParams struct {
	Action   string   `json:"action"`
	Repo     string   `json:"repo,omitempty" jsonschema:"owner/name; default is the detected repo"`
	Ref      string   `json:"ref,omitempty" jsonschema:"branch, tag or sha to start from; default branch when empty"`
	SHA      string   `json:"sha,omitempty" jsonschema:"commit sha, branch or tag"`
	Path     string   `json:"path,omitempty" jsonschema:"only commits touching this path"`
	Paths    []string `json:"paths,omitempty" jsonschema:"only these files in the patch (globs)"`
	Not      string   `json:"not,omitempty" jsonschema:"exclude commits reachable from this ref"`
	Base     string   `json:"base,omitempty" jsonschema:"compare from this ref"`
	Head     string   `json:"head,omitempty" jsonschema:"compare to this ref; owner:branch for a fork"`
	Include  []string `json:"include,omitempty" jsonschema:"extras for get: stat, patch"`
	MaxChars int      `json:"max_chars,omitempty" jsonschema:"patch size in chars, default 14000, max 100000"`
	Verbose  bool     `json:"verbose,omitempty" jsonschema:"print full 40-char shas"`
	Page     int      `json:"page,omitempty" jsonschema:"page number from 1"`
	Limit    int      `json:"limit,omitempty" jsonschema:"page size; at most 50 for list"`
}

const (
	commitListMax      = 50
	commitCompareLimit = 20
	patchDefaultChars  = 14000
	maxCompareFiles    = 200
)

// registerCommit registers the commit tool.
func registerCommit(r *tools.Registry) {
	t := &commitTool{}
	const base = "/repos/{owner}/{repo}"
	r.Add(tools.Spec{
		Name:        "commit",
		Toolset:     "repos",
		Description: "Commits: list, get (stat or patch), compare refs, find the PR of a commit.",
		Params:      commitParams{},
		RepoScoped:  true,
		Actions: []tools.Action{
			tools.NewAction("list", tools.Read, t.list,
				tools.Summary("commits of a ref, optionally limited to a path or excluding another ref"),
				tools.Params("ref", "path", "not", "page", "limit", "verbose"),
				tools.Endpoints("GET "+base+"/commits")),
			tools.NewAction("get", tools.Read, t.get,
				tools.Summary("one commit: message, changed files; include=stat for totals, include=patch for the diff"),
				tools.Params("sha", "ref", "include", "paths", "max_chars", "verbose"),
				tools.Endpoints("GET "+base+"/git/commits/{sha}", "GET "+base+"/git/commits/{sha}.{diffType}")),
			tools.NewAction("compare", tools.Read, t.compare,
				tools.Summary("commits and files between two refs (files are aggregated per commit, not a net diff)"),
				tools.Params("page", "limit", "verbose"),
				tools.Required("base", "head"),
				tools.Endpoints("GET "+base+"/compare/{basehead}")),
			tools.NewAction("pulls_of", tools.Read, t.pullsOf,
				tools.Summary("the merged PR that contains a (merge) commit"),
				tools.Params("sha", "ref"),
				tools.Endpoints("GET "+base+"/commits/{sha}/pull")),
		},
	})
}

type commitTool struct{}

var fullSHA = regexp.MustCompile(`^(?:[0-9a-fA-F]{40}|[0-9a-fA-F]{64})$`)

func (p *commitParams) shortSHA(s string) string {
	if p.Verbose {
		return s
	}
	return format.SHA(s, 8)
}

func commitLine(sc func(string) string, cm commitObj) string {
	author := cm.Commit.Author.Name
	if author == "" && cm.Author != nil {
		author = cm.Author.Login
	}
	date := format.T(cm.Commit.Author.Date)
	if len(date) > 10 {
		date = date[:10]
	}
	return fmt.Sprintf("%s %s %s | %s", sc(cm.SHA), date, author, subject(cm.Commit.Message))
}

// ---- list --------------------------------------------------------------

func (t *commitTool) list(c *tools.Call, p *commitParams) (*tools.Result, error) {
	q := url.Values{"stat": {"false"}, "files": {"false"}, "verification": {"false"}}
	if ref := refOf(p.Ref); ref != "" {
		q.Set("sha", ref)
	}
	if p.Path != "" {
		q.Set("path", cleanPath(p.Path))
	}
	if p.Not != "" {
		q.Set("not", refOf(p.Not))
	}
	var items []commitObj
	pg, err := c.Env.Client.List(c.Ctx, rp(c, "commits"), q, p.Page, listLimit(c, p.Limit, 0, commitListMax), &items)
	if err != nil {
		if errStatus(err, http.StatusConflict) {
			return c.Fail("repo is empty"), nil
		}
		if p.Path != "" && errStatus(err, http.StatusNotFound) {
			// the server answers 404 when no commit touches the path
			return &tools.Result{Text: "# commits 0/0\nnote: no commit touches this path"}, nil
		}
		return nil, err
	}
	var b format.B
	b.Raw(format.ListHeader("commits", pg) + "\n")
	for _, cm := range items {
		b.Raw(commitLine(p.shortSHA, cm) + "\n")
	}
	return &tools.Result{Text: strings.TrimRight(b.String(), "\n")}, nil
}

// ---- get ---------------------------------------------------------------

func (t *commitTool) get(c *tools.Call, p *commitParams) (*tools.Result, error) {
	ref := refOf(p.SHA)
	if ref == "" {
		ref = refOf(p.Ref)
	}
	if ref == "" {
		return c.Fail("invalid: pass sha (commit, branch or tag)"), nil
	}
	wantStat, wantPatch := false, false
	for _, inc := range p.Include {
		switch strings.ToLower(strings.TrimSpace(inc)) {
		case "stat":
			wantStat = true
		case "patch":
			wantPatch = true
		default:
			return c.Fail("invalid: include %q; valid: stat,patch", inc), nil
		}
	}
	q := url.Values{
		"stat":         {strconv.FormatBool(wantStat)},
		"files":        {"true"},
		"verification": {"false"},
	}
	var cm commitObj
	if _, err := c.Env.Client.GetJSON(c.Ctx, rp(c, "git", "commits", ref), q, &cm); err != nil {
		return nil, err
	}

	var b format.B
	b.Raw("commit " + p.shortSHA(cm.SHA) + "\n")
	author := cm.Commit.Author.Name
	if cm.Author != nil && cm.Author.Login != "" && cm.Author.Login != author {
		author += " (@" + cm.Author.Login + ")"
	}
	b.KV("author", author)
	b.KV("date", format.T(cm.Commit.Author.Date))
	if len(cm.Parents) > 0 {
		ps := make([]string, len(cm.Parents))
		for i, pr := range cm.Parents {
			ps[i] = p.shortSHA(pr.SHA)
		}
		b.KV("parents", strings.Join(ps, ","))
	}
	if wantStat && cm.Stats != nil {
		b.KV("stats", fmt.Sprintf("+%d -%d files:%d", cm.Stats.Additions, cm.Stats.Deletions, len(cm.Files)))
	}
	// The server builds the file list from a map, so the order is random.
	slices.SortStableFunc(cm.Files, func(x, y commitFile) int { return strings.Compare(x.Filename, y.Filename) })
	if len(cm.Files) > 0 {
		b.Raw("files: " + strconv.Itoa(len(cm.Files)) + "\n")
		for i, f := range cm.Files {
			if i >= maxCompareFiles {
				b.Raw(fmt.Sprintf("  [truncated: %d of %d files shown]\n", maxCompareFiles, len(cm.Files)))
				break
			}
			b.Raw("  " + statusLetter(f.Status) + " " + f.Filename + "\n")
		}
	}
	msg := strings.TrimSpace(cm.Commit.Message)
	if msg != "" {
		b.Raw("message:\n  " + strings.ReplaceAll(format.Trunc(msg, 2000, "message is longer than 2000 chars"), "\n", "\n  ") + "\n")
	}
	out := strings.TrimRight(b.String(), "\n")

	if wantPatch {
		patch, err := t.patchText(c, p, cm.SHA)
		if err != nil {
			return nil, err
		}
		out += "\n\npatch:\n" + patch
	}
	return &tools.Result{Text: out}, nil
}

// patchText fetches, filters and caps the diff of one commit.
func (t *commitTool) patchText(c *tools.Call, p *commitParams, ref string) (string, error) {
	resp, err := c.Env.Client.Do(c.Ctx, forgejo.Request{
		Method: http.MethodGet,
		Path:   rp(c, "git", "commits", ref+".diff"),
		Header: http.Header{"Accept": {"text/plain"}},
	})
	if err != nil {
		return "", err
	}
	files := format.SplitDiff(string(resp.Body))
	if len(files) == 0 {
		return "(no changes)", nil
	}
	all := len(files)
	if len(p.Paths) > 0 {
		files = format.FilterDiff(files, p.Paths)
		if len(files) == 0 {
			return fmt.Sprintf("(no file of the %d changed files matches paths)", all), nil
		}
	}
	text := format.RenderDiff(files, c.Chars(p.MaxChars, patchDefaultChars))
	if resp.Truncated {
		text += "\n[truncated: the diff is larger than 8MB and was cut; pass paths=[...] to read single files]"
	}
	return text, nil
}

// ---- compare -----------------------------------------------------------

type compareResponse struct {
	TotalCommits int         `json:"total_commits"`
	Commits      []commitObj `json:"commits"`
	Files        []struct {
		Filename string `json:"filename"`
		Status   string `json:"status"`
	} `json:"files"`
}

// mergeStatuses folds the per-commit statuses of one file into one letter;
// "" means the file was added and removed again within the range.
func mergeStatuses(sts []string) string {
	first, last := statusLetter(sts[0]), statusLetter(sts[len(sts)-1])
	switch {
	case first == "A" && last == "D":
		return ""
	case first == "A":
		return "A"
	case last == "D":
		return "D"
	}
	if slices.ContainsFunc(sts, func(s string) bool { return statusLetter(s) == "R" }) {
		return "R"
	}
	return "M"
}

func (t *commitTool) compare(c *tools.Call, p *commitParams) (*tools.Result, error) {
	base, head := refOf(p.Base), p.Head
	if i := strings.Index(head, ":"); i < 0 {
		head = refOf(head)
	}
	if base == "" || head == "" {
		return c.Fail("invalid: base and head are required"), nil
	}
	q := url.Values{"verification": {"false"}, "files": {"true"}}
	var cr compareResponse
	if _, err := c.Env.Client.GetJSON(c.Ctx, rp(c, "compare", forgejo.Wild(base+"..."+head)), q, &cr); err != nil {
		return nil, err
	}
	lo, hi, pg := window(len(cr.Commits), p.Page, listLimit(c, p.Limit, commitCompareLimit, 100))
	var b format.B
	b.Raw(fmt.Sprintf("compare %s...%s\ntotal_commits: %d\n", base, head, cr.TotalCommits))
	b.Raw(format.ListHeader("commits", pg) + "\n")
	for _, cm := range cr.Commits[lo:hi] {
		b.Raw(commitLine(p.shortSHA, cm) + "\n")
	}

	// unique files with a merged status
	var order []string
	seen := map[string][]string{}
	// The server lists files per commit in git log order (newest first);
	// mergeStatuses wants them oldest first, so collect in reverse.
	for _, f := range cr.Files {
		if _, ok := seen[f.Filename]; !ok {
			order = append(order, f.Filename)
			seen[f.Filename] = nil
		}
	}
	for i := len(cr.Files) - 1; i >= 0; i-- {
		f := cr.Files[i]
		seen[f.Filename] = append(seen[f.Filename], f.Status)
	}
	var lines []string
	for _, name := range order {
		if st := mergeStatuses(seen[name]); st != "" {
			lines = append(lines, st+" "+name)
		}
	}
	if len(lines) > 0 {
		b.Raw(fmt.Sprintf("files: %d unique (status merged across commits; not a net diff)\n", len(lines)))
		for i, l := range lines {
			if i >= maxCompareFiles {
				b.Raw(fmt.Sprintf("[truncated: %d of %d files shown]\n", maxCompareFiles, len(lines)))
				break
			}
			b.Raw("  " + l + "\n")
		}
	}
	return &tools.Result{Text: strings.TrimRight(b.String(), "\n")}, nil
}

// ---- pulls_of ----------------------------------------------------------

type pullOf struct {
	Number int64  `json:"number"`
	Title  string `json:"title"`
	State  string `json:"state"`
	Draft  bool   `json:"draft"`
	Merged bool   `json:"merged"`
	User   *login `json:"user"`
	Base   struct {
		Ref string `json:"ref"`
	} `json:"base"`
	Head struct {
		Label string `json:"label"`
		Ref   string `json:"ref"`
	} `json:"head"`
	UpdatedAt string `json:"updated_at"`
}

func (t *commitTool) pullsOf(c *tools.Call, p *commitParams) (*tools.Result, error) {
	ref := refOf(p.SHA)
	if ref == "" {
		ref = refOf(p.Ref)
	}
	if ref == "" {
		return c.Fail("invalid: pass sha (a merge commit)"), nil
	}
	if !fullSHA.MatchString(ref) {
		// the server matches the merge commit sha exactly, so resolve short shas and refs first
		var cm commitObj
		q := url.Values{"stat": {"false"}, "files": {"false"}, "verification": {"false"}}
		if _, err := c.Env.Client.GetJSON(c.Ctx, rp(c, "git", "commits", ref), q, &cm); err != nil {
			if errStatus(err, http.StatusNotFound) {
				return c.Fail("not found: no commit %q", ref), nil
			}
			return nil, err
		}
		ref = cm.SHA
	}
	var pr pullOf
	if _, err := c.Env.Client.GetJSON(c.Ctx, rp(c, "commits", ref, "pull"), nil, &pr); err != nil {
		if errStatus(err, http.StatusNotFound) {
			return &tools.Result{Text: "no merged PR has this merge commit"}, nil
		}
		return nil, err
	}
	state := pr.State
	switch {
	case pr.Merged:
		state = "merged"
	case pr.Draft:
		state = "draft"
	}
	head := pr.Head.Label
	if head == "" {
		head = pr.Head.Ref
	}
	by := ""
	if pr.User != nil {
		by = "by " + pr.User.Login
	}
	var b format.B
	b.Line("#"+strconv.FormatInt(pr.Number, 10)+" "+state, format.OneLine(pr.Title, 120), by, head+"->"+pr.Base.Ref, "upd "+format.T(pr.UpdatedAt))
	return &tools.Result{Text: strings.TrimRight(b.String(), "\n")}, nil
}
