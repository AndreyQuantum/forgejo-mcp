// Package endpoints embeds the generated index of every Forgejo API operation
// (DESIGN.md section 9.5) and offers search and path matching over it.
package endpoints

//go:generate go run ./gen -in $SWAGGER -version $VER -out index.json

import (
	_ "embed"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"sync"
)

//go:embed index.json
var indexJSON []byte

// Op is one API operation (compact keys, see section 9.5).
type Op struct {
	M   string     `json:"m"`            // method
	P   string     `json:"p"`            // path template
	ID  string     `json:"id"`           // operationId
	S   string     `json:"s"`            // summary
	T   string     `json:"t"`            // tag
	Q   [][]string `json:"q,omitempty"`  // [name, type letter, enumOrNote?]; a trailing "*" marks a required parameter
	B   [][]any    `json:"b,omitempty"`  // [name, type letter, required 0|1]
	SC  string     `json:"sc,omitempty"` // token scope category ("a|b" = either)
	Dep bool       `json:"dep,omitempty"`
	W   int        `json:"w,omitempty"` // last path variable is multi-segment
}

// Key returns "METHOD template".
func (o *Op) Key() string { return o.M + " " + o.P }

// Index is the embedded document.
type Index struct {
	V             string `json:"v"`
	N             int    `json:"n"`
	SwaggerSHA256 string `json:"swagger_sha256"`
	Ops           []Op   `json:"ops"`
}

type segment struct {
	lit string         // literal text when re is nil
	re  *regexp.Regexp // segment with {var} parts
}

type compiled struct {
	op     *Op
	segs   []segment
	wild   bool
	litLen int // total literal characters, for specificity ordering
}

var (
	once    sync.Once
	idx     *Index
	idxE    error
	byKey   map[string]*Op
	byMeth  map[string][]*compiled
	reParam = regexp.MustCompile(`\{[^}/]+\}`)
)

// Load returns the parsed embedded index.
func Load() (*Index, error) {
	once.Do(func() {
		idx = &Index{}
		if idxE = json.Unmarshal(indexJSON, idx); idxE != nil {
			return
		}
		build(idx)
	})
	return idx, idxE
}

func build(ix *Index) {
	byKey = make(map[string]*Op, len(ix.Ops))
	byMeth = map[string][]*compiled{}
	for i := range ix.Ops {
		op := &ix.Ops[i]
		byKey[op.Key()] = op
		c := &compiled{op: op, wild: op.W == 1}
		for _, s := range strings.Split(strings.Trim(op.P, "/"), "/") {
			if !strings.Contains(s, "{") {
				c.segs = append(c.segs, segment{lit: s})
				c.litLen += len(s) + 1
				continue
			}
			lit := reParam.ReplaceAllString(s, "")
			c.litLen += len(lit)
			expr := reParam.ReplaceAllStringFunc(s, func(string) string { return "\x00" })
			expr = regexp.QuoteMeta(expr)
			expr = strings.ReplaceAll(expr, "\x00", "[^/]+")
			c.segs = append(c.segs, segment{re: regexp.MustCompile("^" + expr + "$")})
		}
		byMeth[op.M] = append(byMeth[op.M], c)
	}
}

func mustLoad() *Index {
	ix, err := Load()
	if err != nil {
		return &Index{}
	}
	return ix
}

// Header returns the version, operation count and swagger digest of the
// embedded index.
func Header() (version string, n int, sha string) {
	ix := mustLoad()
	return ix.V, len(ix.Ops), ix.SwaggerSHA256
}

// All returns every operation in index order (do not modify).
func All() []Op { return mustLoad().Ops }

// Lookup returns the operation with exactly this method and path template, or nil.
func Lookup(method, template string) *Op {
	mustLoad()
	return byKey[strings.ToUpper(method)+" "+template]
}

// Match finds the operation for a concrete request path (segment-wise match
// on the templates; {var} matches one segment, a wild last variable matches
// the remainder). Returns nil when unknown. path is relative to /api/v1 and
// should be canonical (leading "/", no trailing "/"). When several templates
// match, the one with the most literal text wins (so /repos/search beats
// /repos/{owner}/{repo} would, and "42.diff" prefers {index}.{diffType}).
func Match(method, path string) *Op {
	mustLoad()
	segs := strings.Split(strings.Trim(path, "/"), "/")
	var best *compiled
	for _, c := range byMeth[strings.ToUpper(method)] {
		if !matches(c, segs) {
			continue
		}
		if best == nil || c.litLen > best.litLen || (c.litLen == best.litLen && !c.wild && best.wild) {
			best = c
		}
	}
	if best == nil {
		return nil
	}
	return best.op
}

func matches(c *compiled, segs []string) bool {
	n := len(c.segs)
	if c.wild {
		if len(segs) < n {
			return false
		}
	} else if len(segs) != n {
		return false
	}
	for i, s := range c.segs {
		if i >= len(segs) || segs[i] == "" {
			return false
		}
		if s.re == nil {
			if s.lit != segs[i] {
				return false
			}
			continue
		}
		if !s.re.MatchString(segs[i]) {
			return false
		}
	}
	return true
}

// Search finds operations by space-separated terms (section 9.5): every term
// must be a substring (case-insensitive) of operationId + path + summary +
// tag + parameter names. The score is 4 for a term in the operationId, 3 in
// the path, 2 in the summary, 1 elsewhere, summed; results sort by score
// (descending) then path, stable. tag and method filter exactly (method
// case-insensitive). With an empty q every operation matches with score 0.
// total is the number of matches before paging; page is 1-based.
func Search(q, tag, method string, limit, page int) (ops []Op, total int) {
	all := mustLoad().Ops
	terms := strings.Fields(strings.ToLower(q))
	type hit struct {
		i     int
		score int
	}
	var hits []hit
	for i := range all {
		op := &all[i]
		if tag != "" && !strings.EqualFold(op.T, tag) {
			continue
		}
		if method != "" && !strings.EqualFold(op.M, method) {
			continue
		}
		score, ok := 0, true
		if len(terms) > 0 {
			id, p, s := strings.ToLower(op.ID), strings.ToLower(op.P), strings.ToLower(op.S)
			rest := strings.ToLower(op.T + " " + paramNames(op))
			for _, t := range terms {
				ts := 0
				if strings.Contains(id, t) {
					ts += 4
				}
				if strings.Contains(p, t) {
					ts += 3
				}
				if strings.Contains(s, t) {
					ts += 2
				}
				if strings.Contains(rest, t) {
					ts++
				}
				ok = ts > 0
				score += ts
				if !ok {
					break
				}
			}
		}
		if ok {
			hits = append(hits, hit{i, score})
		}
	}
	sort.SliceStable(hits, func(a, b int) bool {
		if hits[a].score != hits[b].score {
			return hits[a].score > hits[b].score
		}
		return all[hits[a].i].P < all[hits[b].i].P
	})
	total = len(hits)
	if limit <= 0 {
		limit = 15
	}
	if page < 1 {
		page = 1
	}
	lo := (page - 1) * limit
	if lo >= total {
		return nil, total
	}
	hi := min(lo+limit, total)
	for _, h := range hits[lo:hi] {
		ops = append(ops, all[h.i])
	}
	return ops, total
}

func paramNames(op *Op) string {
	var b strings.Builder
	for _, q := range op.Q {
		if len(q) > 0 {
			b.WriteString(q[0])
			b.WriteByte(' ')
		}
	}
	for _, f := range op.B {
		if len(f) > 0 {
			if s, ok := f[0].(string); ok {
				b.WriteString(s)
				b.WriteByte(' ')
			}
		}
	}
	return b.String()
}
