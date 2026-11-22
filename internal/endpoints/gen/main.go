// Command gen converts a Forgejo swagger.json into internal/endpoints/index.json.
//
// Usage:
//
//	go run ./gen -in swagger.json -version 17.0.2 -out index.json
//	go run ./gen -in https://forgejo.example.com/swagger.v1.json -out index.json
//
// -in accepts a file path or an http(s) URL of /swagger.v1.json. With a file
// -version is required (the swagger template's info.version is not the
// server version); with a URL and no -version the version is read from
// GET /api/v1/version of that server. The version must be semver. The
// swagger basePath is ignored.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/AndreyQuantum/forgejo-mcp/internal/endpoints"
	"github.com/AndreyQuantum/forgejo-mcp/internal/endpoints/scope"
)

var (
	reSemver  = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
	reVar     = regexp.MustCompile(`\{([^}/]+)\}`)
	reVersion = regexp.MustCompile(`^v?(\d+\.\d+\.\d+)`)
)

func main() {
	in := flag.String("in", "", "swagger.json path or http(s) URL of /swagger.v1.json")
	ver := flag.String("version", "", "Forgejo version, e.g. 17.0.2 (required with a file)")
	out := flag.String("out", "index.json", "output file")
	flag.Parse()
	if err := run(*in, *ver, *out); err != nil {
		fmt.Fprintln(os.Stderr, "gen:", err)
		os.Exit(1)
	}
}

func run(in, ver, out string) error {
	if in == "" {
		return errors.New("-in is required")
	}
	isURL := strings.HasPrefix(in, "http://") || strings.HasPrefix(in, "https://")
	raw, err := load(in, isURL)
	if err != nil {
		return err
	}
	if ver == "" {
		if !isURL {
			return errors.New("-version is required with a file (info.version of the swagger file is not the server version)")
		}
		if ver, err = serverVersion(in); err != nil {
			return err
		}
	}
	ver = strings.TrimPrefix(ver, "v")
	if !reSemver.MatchString(ver) {
		return fmt.Errorf("version %q is not semver (want N.N.N)", ver)
	}
	idx, err := Convert(raw, ver)
	if err != nil {
		return err
	}
	data, err := Marshal(idx)
	if err != nil {
		return err
	}
	if err := os.WriteFile(out, data, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "gen: %d operations, version %s, %d bytes -> %s\n", len(idx.Ops), ver, len(data), out)
	return nil
}

func load(in string, isURL bool) ([]byte, error) {
	if !isURL {
		return os.ReadFile(in)
	}
	cl := &http.Client{Timeout: 60 * time.Second}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, in, nil)
	if err != nil {
		return nil, err
	}
	resp, err := cl.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", in, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 64<<20))
}

func serverVersion(swaggerURL string) (string, error) {
	u, err := url.Parse(swaggerURL)
	if err != nil {
		return "", err
	}
	base := strings.TrimSuffix(u.Path, "/swagger.v1.json")
	u.Path, u.RawQuery = base+"/api/v1/version", ""
	cl := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	resp, err := cl.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var body struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("GET %s: %w", u, err)
	}
	m := reVersion.FindStringSubmatch(body.Version)
	if m == nil {
		return "", fmt.Errorf("cannot parse server version %q", body.Version)
	}
	return m[1], nil
}

// swagger types (only what the generator reads).
type swagger struct {
	Paths       map[string]map[string]json.RawMessage `json:"paths"`
	Definitions map[string]*schema                    `json:"definitions"`
}

type schema struct {
	Ref        string             `json:"$ref"`
	Type       string             `json:"type"`
	Enum       []any              `json:"enum"`
	Required   []string           `json:"required"`
	Properties map[string]*schema `json:"properties"`
}

type param struct {
	Name     string  `json:"name"`
	In       string  `json:"in"`
	Type     string  `json:"type"`
	Required bool    `json:"required"`
	Enum     []any   `json:"enum"`
	Schema   *schema `json:"schema"`
}

type operation struct {
	Tags       []string `json:"tags"`
	Summary    string   `json:"summary"`
	ID         string   `json:"operationId"`
	Deprecated bool     `json:"deprecated"`
	Parameters []param  `json:"parameters"`
}

var methodOrder = map[string]int{"get": 0, "post": 1, "put": 2, "patch": 3, "delete": 4, "head": 5, "options": 6}

// Convert builds the index from swagger JSON.
func Convert(raw []byte, version string) (*endpoints.Index, error) {
	var sw swagger
	if err := json.Unmarshal(raw, &sw); err != nil {
		return nil, fmt.Errorf("parse swagger: %w", err)
	}
	if len(sw.Paths) == 0 {
		return nil, errors.New("swagger has no paths")
	}
	// Hash only the paths: basePath, host and info differ between the file
	// and the live instance, which would make the index look stale.
	canon, err := json.Marshal(sw.Paths)
	if err != nil {
		return nil, fmt.Errorf("canonicalize paths: %w", err)
	}
	sum := sha256.Sum256(canon)
	idx := &endpoints.Index{V: version, SwaggerSHA256: hex.EncodeToString(sum[:])}
	paths := make([]string, 0, len(sw.Paths))
	for p := range sw.Paths {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		methods := make([]string, 0, 4)
		for m := range sw.Paths[p] {
			if _, ok := methodOrder[m]; ok {
				methods = append(methods, m)
			}
		}
		sort.Slice(methods, func(i, j int) bool { return methodOrder[methods[i]] < methodOrder[methods[j]] })
		for _, m := range methods {
			var o operation
			if err := json.Unmarshal(sw.Paths[p][m], &o); err != nil {
				return nil, fmt.Errorf("%s %s: %w", m, p, err)
			}
			tag := ""
			if len(o.Tags) > 0 {
				tag = o.Tags[0]
			}
			op := endpoints.Op{
				M: strings.ToUpper(m), P: p, ID: o.ID, S: summary(o.Summary), T: tag,
				Dep: o.Deprecated, SC: scope.Category(strings.ToUpper(m), p, tag),
			}
			if isWild(p) {
				op.W = 1
			}
			for _, pa := range o.Parameters {
				switch pa.In {
				case "query", "header":
					op.Q = append(op.Q, queryParam(pa))
				case "formData":
					op.B = append(op.B, []any{pa.Name, typeLetter(pa.Type, len(pa.Enum) > 0), b2i(pa.Required)})
				case "body":
					op.B = append(op.B, bodyFields(sw.Definitions, pa.Schema)...)
				}
			}
			idx.Ops = append(idx.Ops, op)
		}
	}
	idx.N = len(idx.Ops)
	return idx, nil
}

// summary keeps the first sentence of a swagger summary, capped at 110 chars.
func summary(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if i := strings.Index(s, ". "); i > 0 {
		s = s[:i+1]
	}
	if r := []rune(s); len(r) > 110 {
		s = string(r[:109]) + "…"
	}
	return s
}

// isWild reports whether the last path variable swallows several segments
// (routers/api/v1/api.go registers these as "/*").
func isWild(p string) bool {
	m := reVar.FindAllStringSubmatchIndex(p, -1)
	if len(m) == 0 {
		return false
	}
	last := m[len(m)-1]
	if last[1] != len(p) || p[last[0]-1] != '/' {
		return false
	}
	switch p[last[2]:last[3]] {
	case "filepath", "basehead", "archive", "head":
		return true
	case "branch":
		return strings.HasSuffix(p, "/branches/{branch}")
	case "tag":
		return strings.HasSuffix(p, "/repos/{owner}/{repo}/tags/{tag}")
	case "ref":
		return strings.HasSuffix(p, "/git/refs/{ref}")
	}
	return false
}

func queryParam(pa param) []string {
	req := ""
	if pa.Required {
		req = "*"
	}
	if len(pa.Enum) > 0 {
		vals := make([]string, len(pa.Enum))
		for i, e := range pa.Enum {
			vals[i] = fmt.Sprint(e)
		}
		return []string{pa.Name, "e", strings.Join(vals, "|") + req}
	}
	t := typeLetter(pa.Type, false)
	if req != "" {
		return []string{pa.Name, t, req}
	}
	return []string{pa.Name, t}
}

func typeLetter(t string, enum bool) string {
	if enum {
		return "e"
	}
	switch t {
	case "string":
		return "s"
	case "integer", "number":
		return "i"
	case "boolean":
		return "b"
	case "array":
		return "a"
	}
	return "o"
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// bodyFields resolves a body schema (following $ref once) to its top-level
// properties.
func bodyFields(defs map[string]*schema, s *schema) [][]any {
	if s == nil {
		return nil
	}
	if s.Ref != "" {
		s = defs[strings.TrimPrefix(s.Ref, "#/definitions/")]
		if s == nil {
			return nil
		}
	}
	if len(s.Properties) == 0 {
		return nil
	}
	req := map[string]bool{}
	for _, r := range s.Required {
		req[r] = true
	}
	names := make([]string, 0, len(s.Properties))
	for n := range s.Properties {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([][]any, 0, len(names))
	for _, n := range names {
		p := s.Properties[n]
		t := "o"
		if p != nil && p.Ref == "" {
			t = typeLetter(p.Type, len(p.Enum) > 0)
		}
		out = append(out, []any{n, t, b2i(req[n])})
	}
	return out
}

// Marshal renders the index compactly with one operation per line.
func Marshal(idx *endpoints.Index) ([]byte, error) {
	var b bytes.Buffer
	v, err := json.Marshal(idx.V)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(&b, `{"v":%s,"n":%d,"swagger_sha256":%q`, v, idx.N, idx.SwaggerSHA256)
	b.WriteString(`,"ops":[` + "\n")
	for i := range idx.Ops {
		var one bytes.Buffer
		e := json.NewEncoder(&one)
		e.SetEscapeHTML(false)
		if err := e.Encode(&idx.Ops[i]); err != nil {
			return nil, err
		}
		b.Write(bytes.TrimRight(one.Bytes(), "\n"))
		if i < len(idx.Ops)-1 {
			b.WriteByte(',')
		}
		b.WriteByte('\n')
	}
	b.WriteString("]}\n")
	return b.Bytes(), nil
}
