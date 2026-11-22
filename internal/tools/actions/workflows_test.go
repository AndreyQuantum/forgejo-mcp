package actions

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/AndreyQuantum/forgejo-mcp/internal/testutil"
)

const ciYAML = `name: CI
on:
  push:
    branches: [main]
  pull_request:
  workflow_dispatch:
jobs:
  build:
    runs-on: docker
    steps:
      - run: echo hi
  test:
    runs-on: docker
    needs: build
    steps:
      - run: echo test
`

const manualYAML = `name: Manual
on:
  workflow_dispatch:
    inputs:
      env:
        description: Target environment
        type: choice
        options: [dev, prod]
        required: true
        default: dev
      debug:
        type: boolean
        default: false
      note: {}
jobs:
  deploy:
    runs-on: docker
    steps:
      - run: echo ${{ inputs.env }}
`

const shortOnYAML = `on: [push, workflow_dispatch]
jobs:
  x:
    runs-on: docker
    steps: [{run: "true"}]
`

func dirListing(dir string, names ...string) string {
	var parts []string
	for i, n := range names {
		typ := "file"
		if n == "sub" {
			typ = "dir"
		}
		parts = append(parts, fmt.Sprintf(`{"name":%q,"path":"%s/%s","type":%q,"sha":"blob%d"}`, n, dir, n, typ, i))
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func rawRoute(s *fakeServer, dir, name, body string) {
	s.on("GET /repos/o/r/raw/"+strings.ReplaceAll(dir, "/", "/")+"/"+name, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(body))
	})
}

func TestWorkflowsForgejoDirGolden(t *testing.T) {
	s := newFake(t)
	s.reply("GET /repos/o/r/contents/.forgejo/workflows", 200, dirListing(".forgejo/workflows", "manual.yml", "README.md", "ci.yml", "sub", "short.yaml"))
	rawRoute(s, ".forgejo/workflows", "ci.yml", ciYAML)
	rawRoute(s, ".forgejo/workflows", "manual.yml", manualYAML)
	rawRoute(s, ".forgejo/workflows", "short.yaml", shortOnYAML)
	text := runOK(t, s, map[string]any{"action": "workflows", "ref": "dev"})
	testutil.Golden(t, "workflows", text)
	// .gitea is only a fallback
	if n := len(s.seen("GET /repos/o/r/contents/.gitea/workflows")); n != 0 {
		t.Fatalf(".gitea/workflows must not be read when .forgejo/workflows exists")
	}
	wantQuery(t, s.only("GET /repos/o/r/contents/.forgejo/workflows"), map[string][]string{"ref": {"dev"}})
	wantQuery(t, s.only("GET /repos/o/r/raw/.forgejo/workflows/ci.yml"), map[string][]string{"ref": {"dev"}})
	// no README.md, no directory
	for _, k := range s.keys() {
		if strings.Contains(k, "README") || strings.HasSuffix(k, "/sub") {
			t.Errorf("unexpected request %s", k)
		}
	}
}

func TestWorkflowsVerboseGolden(t *testing.T) {
	s := newFake(t)
	s.reply("GET /repos/o/r/contents/.forgejo/workflows", 200, dirListing(".forgejo/workflows", "manual.yml"))
	rawRoute(s, ".forgejo/workflows", "manual.yml", manualYAML)
	text := runOK(t, s, map[string]any{"action": "workflows", "verbose": true})
	testutil.Golden(t, "workflows_verbose", text)
	if r := s.only("GET /repos/o/r/contents/.forgejo/workflows"); r.Query.Has("ref") {
		t.Errorf("no ref param expected, got %v", r.Query)
	}
}

func TestWorkflowsGiteaFallbackOnlyOn404(t *testing.T) {
	s := newFake(t)
	s.reply("GET /repos/o/r/contents/.forgejo/workflows", 404, `{"message":"GetContentsOrList","errors":["object does not exist"]}`)
	s.reply("GET /repos/o/r/contents/.gitea/workflows", 200, dirListing(".gitea/workflows", "ci.yml"))
	rawRoute(s, ".gitea/workflows", "ci.yml", ciYAML)
	text := runOK(t, s, map[string]any{"action": "workflows"})
	if !strings.HasPrefix(text, "# workflows 1/1 dir: .gitea/workflows\nci.yml |") {
		t.Fatalf("got %q", text)
	}
	if got := s.keys(); !(indexOf(got, "GET /repos/o/r/contents/.forgejo/workflows") < indexOf(got, "GET /repos/o/r/contents/.gitea/workflows")) {
		t.Fatalf("directory order: %v", got)
	}

	// a server error on the first directory is NOT a reason to fall back
	s2 := newFake(t)
	s2.reply("GET /repos/o/r/contents/.forgejo/workflows", 500, `{"message":""}`)
	s2.reply("GET /repos/o/r/contents/.gitea/workflows", 200, dirListing(".gitea/workflows", "ci.yml"))
	runErr(t, s2, map[string]any{"action": "workflows"})
	if n := len(s2.seen("GET /repos/o/r/contents/.gitea/workflows")); n != 0 {
		t.Fatalf("fell back on a 500")
	}
}

func indexOf(xs []string, x string) int {
	for i, v := range xs {
		if v == x {
			return i
		}
	}
	return -1
}

func TestWorkflowsNone(t *testing.T) {
	s := newFake(t)
	text := runOK(t, s, map[string]any{"action": "workflows"})
	if !strings.HasPrefix(text, "# workflows 0/0\n") || !strings.Contains(text, ".github/workflows is not used") {
		t.Fatalf("got %q", text)
	}
	if n := len(s.seen("GET /repos/o/r/contents/.github/workflows")); n != 0 {
		t.Fatalf(".github/workflows must never be read")
	}
}

func TestWorkflowsInvalidYAMLIsReportedPerFile(t *testing.T) {
	s := newFake(t)
	s.reply("GET /repos/o/r/contents/.forgejo/workflows", 200, dirListing(".forgejo/workflows", "bad.yml", "ci.yml"))
	rawRoute(s, ".forgejo/workflows", "bad.yml", "on: [unclosed\n  - x: : :")
	rawRoute(s, ".forgejo/workflows", "ci.yml", ciYAML)
	text := runOK(t, s, map[string]any{"action": "workflows"})
	lines := strings.Split(text, "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[1], "bad.yml | invalid YAML:") || !strings.HasPrefix(lines[2], `ci.yml | "CI"`) {
		t.Fatalf("got %q", text)
	}
}

func TestWorkflowsCachedByBlobSHA(t *testing.T) {
	s := newFake(t)
	s.reply("GET /repos/o/r/contents/.forgejo/workflows", 200, dirListing(".forgejo/workflows", "ci.yml"))
	rawRoute(s, ".forgejo/workflows", "ci.yml", ciYAML)
	env := testutil.NewEnv(t, s, testutil.WithRepo("o/r"))
	for i := 0; i < 2; i++ {
		if text, isErr := testutil.CallTool(t, env, runSpec(), map[string]any{"action": "workflows"}); isErr {
			t.Fatal(text)
		}
	}
	if n := len(s.seen("GET /repos/o/r/raw/.forgejo/workflows/ci.yml")); n != 1 {
		t.Fatalf("raw file fetched %d times, want 1 (cache by blob sha)", n)
	}
	// the directory listing is re-read every time, so a changed blob misses the cache
	if n := len(s.seen("GET /repos/o/r/contents/.forgejo/workflows")); n != 2 {
		t.Fatalf("directory listed %d times, want 2", n)
	}
}

func TestParseWorkflowShapes(t *testing.T) {
	w := parseWorkflow("a.yml", []byte("on: push\njobs: {}\n"))
	if strings.Join(w.On, ",") != "push" {
		t.Errorf("scalar on: %v", w.On)
	}
	w = parseWorkflow("a.yml", []byte("- not a mapping\n"))
	if !strings.HasPrefix(w.Err, "invalid workflow") {
		t.Errorf("sequence document: %+v", w)
	}
	w = parseWorkflow("a.yml", []byte(""))
	if w.Err == "" {
		t.Errorf("empty document must be flagged: %+v", w)
	}
	w = parseWorkflow("a.yml", []byte("name: x\njobs:\n  a: {}\n"))
	if w.Err != "" || len(w.On) != 0 || renderWorkflow(w, false) != `a.yml | "x"` {
		t.Errorf("no on: %+v / %s", w, renderWorkflow(w, false))
	}
}
