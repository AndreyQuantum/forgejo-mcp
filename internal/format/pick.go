package format

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// segment is one parsed path element.
type segment struct {
	name  string
	all   bool // name[]
	index int  // name[i]; -1 when unused
	star  bool // "*"
}

type pickExpr struct {
	key  string // output key: alias or the path as written
	segs []segment
}

// Pick projects a decoded JSON value (any) onto the jq-lite paths of
// section 9.3: expr := path [":" alias]; path := seg ("." seg)*;
// seg := name | name "[]" | name "[" int "]" | "*". A root array is applied
// per element and rendered as JSON Lines; a root object as one object.
// Output keys are the alias or the path as written (flat, in expression
// order); a path ending in "*" emits every key of that level as
// "<prefix>.<key>". Missing paths are omitted. Invalid syntax returns an
// error "pick: <reason> at <col>" (col is the 1-based column in the
// expression).
func Pick(v any, paths []string) (string, error) {
	var exprs []pickExpr
	for _, p := range paths {
		for _, part := range strings.Split(p, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			e, err := parsePick(part)
			if err != nil {
				return "", err
			}
			exprs = append(exprs, e)
		}
	}
	switch root := v.(type) {
	case []any:
		var sb strings.Builder
		for i, el := range root {
			if i > 0 {
				sb.WriteByte('\n')
			}
			s, err := pickOne(el, exprs)
			if err != nil {
				return "", err
			}
			sb.WriteString(s)
		}
		return sb.String(), nil
	case map[string]any:
		return pickOne(root, exprs)
	default:
		return "", fmt.Errorf("pick: value is not an object or array at 1")
	}
}

func parsePick(s string) (pickExpr, error) {
	r := []rune(s)
	fail := func(reason string, col int) (pickExpr, error) {
		return pickExpr{}, fmt.Errorf("pick: %s at %d", reason, col+1)
	}
	// Split off the alias at the last ':' outside brackets.
	pathEnd, alias := len(r), ""
	hasAlias := false
	depth := 0
	for i, c := range r {
		switch c {
		case '[':
			depth++
		case ']':
			depth--
		case ':':
			if depth == 0 {
				pathEnd, alias, hasAlias = i, string(r[i+1:]), true
			}
		}
		if hasAlias {
			break
		}
	}
	if hasAlias && strings.TrimSpace(alias) == "" {
		return fail("empty alias", pathEnd)
	}
	var segs []segment
	i := 0
	for {
		start := i
		for i < pathEnd && !strings.ContainsRune(".[]", r[i]) {
			i++
		}
		name := string(r[start:i])
		if name == "" {
			return fail("empty segment", start)
		}
		sg := segment{name: name, index: -1}
		if name == "*" {
			sg.star = true
		} else if strings.Contains(name, "*") {
			return fail("'*' must be a whole segment", start)
		}
		if i < pathEnd && r[i] == '[' {
			if sg.star {
				return fail("'*' cannot be indexed", i)
			}
			close := i + 1
			for close < pathEnd && r[close] != ']' {
				close++
			}
			if close >= pathEnd {
				return fail("missing ']'", i)
			}
			inner := string(r[i+1 : close])
			if inner == "" {
				sg.all = true
			} else {
				n, err := strconv.Atoi(inner)
				if err != nil || n < 0 {
					return fail("bad index", i+1)
				}
				sg.index = n
			}
			i = close + 1
		}
		segs = append(segs, sg)
		if i >= pathEnd {
			break
		}
		if r[i] != '.' {
			return fail(fmt.Sprintf("unexpected %q", string(r[i])), i)
		}
		i++
		if i >= pathEnd {
			return fail("empty segment", i)
		}
	}
	for k, sg := range segs {
		if sg.star && k != len(segs)-1 {
			return fail("'*' must be the last segment", 0)
		}
	}
	e := pickExpr{segs: segs, key: string(r[:pathEnd])}
	if hasAlias {
		if segs[len(segs)-1].star {
			return fail("alias not allowed with '*'", pathEnd)
		}
		e.key = strings.TrimSpace(alias)
	}
	return e, nil
}

// pickOne builds the ordered output object for one element.
func pickOne(el any, exprs []pickExpr) (string, error) {
	type kv struct {
		k string
		v any
	}
	var out []kv
	idx := map[string]int{}
	set := func(k string, v any) {
		if i, ok := idx[k]; ok {
			out[i].v = v
			return
		}
		idx[k] = len(out)
		out = append(out, kv{k, v})
	}
	for _, e := range exprs {
		if last := e.segs[len(e.segs)-1]; last.star {
			prefix := strings.TrimSuffix(e.key, "*")
			parent, ok := walk(el, e.segs[:len(e.segs)-1])
			if !ok {
				continue
			}
			if m, isMap := parent.(map[string]any); isMap {
				keys := make([]string, 0, len(m))
				for k := range m {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, k := range keys {
					set(prefix+k, m[k])
				}
			}
			continue
		}
		if v, ok := walk(el, e.segs); ok {
			set(e.key, v)
		}
	}
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, p := range out {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, err := marshal(p.k)
		if err != nil {
			return "", err
		}
		vb, err := marshal(p.v)
		if err != nil {
			return "", err
		}
		buf.Write(kb)
		buf.WriteByte(':')
		buf.Write(vb)
	}
	buf.WriteByte('}')
	return buf.String(), nil
}

func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// walk evaluates segs against v. A missing path reports ok=false.
func walk(v any, segs []segment) (any, bool) {
	if len(segs) == 0 {
		return v, true
	}
	sg := segs[0]
	m, isMap := v.(map[string]any)
	if !isMap {
		return nil, false
	}
	child, has := m[sg.name]
	if !has {
		return nil, false
	}
	switch {
	case sg.all:
		arr, isArr := child.([]any)
		if !isArr {
			return nil, false
		}
		if len(segs) == 1 {
			return arr, true
		}
		res := make([]any, 0, len(arr))
		for _, el := range arr {
			if r, ok := walk(el, segs[1:]); ok {
				res = append(res, r)
			}
		}
		return res, true
	case sg.index >= 0:
		arr, isArr := child.([]any)
		if !isArr || sg.index >= len(arr) {
			return nil, false
		}
		return walk(arr[sg.index], segs[1:])
	default:
		return walk(child, segs[1:])
	}
}
