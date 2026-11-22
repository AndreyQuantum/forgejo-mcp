package format

import (
	"encoding/json"
	"strings"
)

// keepURLKeys are the *_url keys that survive noise stripping.
var keepURLKeys = map[string]bool{
	"html_url":             true,
	"target_url":           true,
	"clone_url":            true,
	"ssh_url":              true,
	"browser_download_url": true,
	"download_url":         true,
}

// userKeys hold nested user objects that collapse to their login.
var userKeys = map[string]bool{
	"user": true, "owner": true, "creator": true, "author": true,
	"doer": true, "merged_by": true, "trigger_user": true,
}

// repoKeys hold nested repository objects that collapse to full_name.
var repoKeys = map[string]bool{"repository": true, "repo": true}

// StripNoise applies the generic noise stripping of section 9.2 to a decoded
// JSON value: drops null/""/[]/{} values and url-ish keys, collapses nested
// user and repository objects, unwraps list wrappers. The wrapper's
// total_count is returned as the total; it is -1 when the value was not a
// wrapper or the wrapper carries no total (the {"ok":true,"data":[...]} form).
func StripNoise(v any) (out any, total int64) {
	total = -1
	if m, ok := v.(map[string]any); ok {
		if arr, tc, ok := unwrap(m); ok {
			v, total = arr, tc
		}
	}
	return stripValue(v), total
}

// unwrap detects {"ok":true,"data":[...]} and {"<list>":[...],"total_count":N}.
func unwrap(m map[string]any) (arr []any, total int64, ok bool) {
	if data, isArr := m["data"].([]any); isArr {
		if _, hasOK := m["ok"]; hasOK && len(m) == 2 {
			return data, -1, true
		}
	}
	tcv, has := m["total_count"]
	if !has {
		return nil, -1, false
	}
	var list []any
	found := 0
	for k, val := range m {
		if k == "total_count" {
			continue
		}
		a, isArr := val.([]any)
		if !isArr {
			return nil, -1, false
		}
		list, found = a, found+1
	}
	if found != 1 {
		return nil, -1, false
	}
	return list, toInt64(tcv), true
}

func toInt64(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return -1
		}
		return i
	case int:
		return int64(n)
	case int64:
		return n
	}
	return -1
}

func stripValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			if k == "event_payload" || dropURLKey(k) {
				continue
			}
			val = collapse(k, val)
			if k == "permissions" && allFalse(val) {
				continue
			}
			val = stripValue(val)
			if isEmpty(val) {
				continue
			}
			out[k] = val
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = stripValue(e)
		}
		return out
	default:
		return v
	}
}

func dropURLKey(k string) bool {
	if keepURLKeys[k] {
		return false
	}
	return k == "url" || strings.HasSuffix(k, "_url")
}

// collapse reduces nested user and repository objects to a scalar.
func collapse(key string, val any) any {
	m, ok := val.(map[string]any)
	if !ok {
		return val
	}
	switch {
	case userKeys[key]:
		if login, ok := m["login"].(string); ok {
			return login
		}
	case repoKeys[key]:
		if name, ok := m["full_name"].(string); ok {
			return name
		}
	}
	return val
}

func allFalse(v any) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	for _, val := range m {
		if b, ok := val.(bool); !ok || b {
			return false
		}
	}
	return true
}

func isEmpty(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return x == ""
	case []any:
		return len(x) == 0
	case map[string]any:
		return len(x) == 0
	}
	return false
}
