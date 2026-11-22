package actions

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"
	"gopkg.in/yaml.v3"

	"github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"
	"github.com/AndreyQuantum/forgejo-mcp/internal/format"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools/shared"
)

// Forgejo Actions reads `.forgejo/workflows`, and `.gitea/workflows` only
// when the first directory is absent; `.github/workflows` is never run, so it
// is not listed (a dispatch of such a file would 404).
var workflowDirs = []string{".forgejo/workflows", ".gitea/workflows"}

const (
	workflowParallel = 6
	workflowCacheTTL = 10 * time.Minute
)

// wfInput is one `on.workflow_dispatch.inputs` entry.
type wfInput struct {
	Name, Type, Description, Default string
	Options                          []string
	Required                         bool
}

// wfInfo is the parsed projection of a workflow file.
type wfInfo struct {
	File   string
	Title  string
	On     []string
	Inputs []wfInput
	Jobs   []string
	Err    string
}

type dirEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Type string `json:"type"`
	SHA  string `json:"sha"`
}

func (t *runTool) workflows(c *tools.Call, p *runParams) (*tools.Result, error) {
	cl := c.Env.Client
	q := url.Values{}
	ref := shared.NormalizeRef(p.Ref)
	if ref != "" {
		q.Set("ref", ref)
	}

	var dir string
	var entries []dirEntry
	for _, d := range workflowDirs {
		var list []dirEntry
		_, err := cl.GetJSON(c.Ctx, forgejo.Path("repos", c.Owner, c.Repo, "contents", forgejo.Wild(d)), q, &list)
		if err != nil {
			var ae *forgejo.APIError
			if errors.As(err, &ae) && ae.Status == http.StatusNotFound {
				continue
			}
			return nil, err
		}
		dir, entries = d, list
		break
	}
	if dir == "" {
		return &tools.Result{Text: "# workflows 0/0\n(no " + strings.Join(workflowDirs, " or ") + " in this repo; .github/workflows is not used by Forgejo Actions)"}, nil
	}

	var files []dirEntry
	for _, e := range entries {
		ext := strings.ToLower(path.Ext(e.Name))
		if e.Type == "file" && (ext == ".yml" || ext == ".yaml") {
			files = append(files, e)
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })

	infos := make([]wfInfo, len(files))
	g, ctx := errgroup.WithContext(c.Ctx)
	g.SetLimit(workflowParallel)
	for i, f := range files {
		g.Go(func() error {
			load := func() (any, error) {
				resp, err := cl.Do(ctx, forgejo.Request{
					Method: http.MethodGet,
					Path:   forgejo.Path("repos", c.Owner, c.Repo, "raw", forgejo.Wild(f.Path)),
					Query:  q,
				})
				if err != nil {
					return nil, err
				}
				return parseWorkflow(f.Name, resp.Body), nil
			}
			var v any
			var err error
			if f.SHA != "" {
				// content-addressed by the blob sha, so a changed file misses
				v, err = cl.Cache().Get(cl.CacheKey("wf:"+c.Owner+"/"+c.Repo+":"+f.SHA), workflowCacheTTL, load)
			} else {
				v, err = load()
			}
			if err != nil {
				return err
			}
			infos[i] = v.(wfInfo)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}

	var b format.B
	b.Raw(fmt.Sprintf("# workflows %d/%d dir: %s", len(infos), len(infos), dir))
	if ref != "" {
		b.Raw(" ref: " + ref)
	}
	b.Raw("\n")
	for _, w := range infos {
		b.Raw(renderWorkflow(w, p.Verbose) + "\n")
	}
	return &tools.Result{Text: strings.TrimRight(b.String(), "\n")}, nil
}

// parseWorkflow reads the parts of a workflow file the tool shows. The YAML
// is decoded as nodes so the order of triggers and inputs is kept.
func parseWorkflow(file string, data []byte) wfInfo {
	w := wfInfo{File: file}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		w.Err = "invalid YAML: " + format.OneLine(err.Error(), 120)
		return w
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		w.Err = "invalid workflow: not a mapping"
		return w
	}
	root := doc.Content[0]
	if n := mapGet(root, "name"); n != nil && n.Kind == yaml.ScalarNode {
		w.Title = n.Value
	}
	if jobs := mapGet(root, "jobs"); jobs != nil && jobs.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(jobs.Content); i += 2 {
			w.Jobs = append(w.Jobs, jobs.Content[i].Value)
		}
	}
	on := mapGet(root, "on")
	if on == nil {
		// YAML 1.1 loaders read the bare key `on` as the boolean true
		on = mapGet(root, "true")
	}
	if on == nil {
		return w
	}
	switch on.Kind {
	case yaml.ScalarNode:
		w.On = []string{on.Value}
	case yaml.SequenceNode:
		for _, n := range on.Content {
			w.On = append(w.On, n.Value)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(on.Content); i += 2 {
			w.On = append(w.On, on.Content[i].Value)
		}
		if wd := mapGet(on, "workflow_dispatch"); wd != nil && wd.Kind == yaml.MappingNode {
			if in := mapGet(wd, "inputs"); in != nil && in.Kind == yaml.MappingNode {
				for i := 0; i+1 < len(in.Content); i += 2 {
					w.Inputs = append(w.Inputs, parseInput(in.Content[i].Value, in.Content[i+1]))
				}
			}
		}
	}
	return w
}

func parseInput(name string, n *yaml.Node) wfInput {
	in := wfInput{Name: name, Type: "string"}
	if n.Kind != yaml.MappingNode {
		return in
	}
	scalar := func(key string) string {
		if v := mapGet(n, key); v != nil && v.Kind == yaml.ScalarNode {
			return v.Value
		}
		return ""
	}
	if tp := scalar("type"); tp != "" {
		in.Type = tp
	}
	in.Description = scalar("description")
	in.Default = scalar("default")
	in.Required = scalar("required") == "true"
	if opts := mapGet(n, "options"); opts != nil && opts.Kind == yaml.SequenceNode {
		for _, o := range opts.Content {
			in.Options = append(in.Options, o.Value)
		}
	}
	return in
}

func mapGet(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// renderWorkflow renders `ci.yml | "CI" | on: push,workflow_dispatch | inputs: env(choice dev|prod, required, default dev)`.
func renderWorkflow(w wfInfo, verbose bool) string {
	var b format.B
	if w.Err != "" {
		b.Line(w.File, w.Err)
		return strings.TrimRight(b.String(), "\n")
	}
	title := ""
	if w.Title != "" {
		title = fmt.Sprintf("%q", format.OneLine(w.Title, 80))
	}
	on := ""
	if len(w.On) > 0 {
		on = "on: " + strings.Join(w.On, ",")
	}
	inputs := ""
	if len(w.Inputs) > 0 {
		parts := make([]string, len(w.Inputs))
		for i, in := range w.Inputs {
			parts[i] = renderInput(in, verbose)
		}
		inputs = "inputs: " + strings.Join(parts, ", ")
	}
	jobs := ""
	if verbose && len(w.Jobs) > 0 {
		jobs = "jobs: " + strings.Join(w.Jobs, ",")
	}
	b.Line(w.File, title, on, inputs, jobs)
	return strings.TrimRight(b.String(), "\n")
}

func renderInput(in wfInput, verbose bool) string {
	typ := in.Type
	if len(in.Options) > 0 {
		typ += " " + strings.Join(in.Options, "|")
	}
	parts := []string{typ}
	if in.Required {
		parts = append(parts, "required")
	}
	if in.Default != "" {
		parts = append(parts, "default "+in.Default)
	}
	if verbose && in.Description != "" {
		parts = append(parts, fmt.Sprintf("%q", format.OneLine(in.Description, 60)))
	}
	return in.Name + "(" + strings.Join(parts, ", ") + ")"
}
