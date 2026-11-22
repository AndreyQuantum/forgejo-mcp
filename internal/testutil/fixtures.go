package testutil

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

// Fixture returns the bytes of testdata/<name> relative to the package under
// test (recorded API responses live there).
func Fixture(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	return b
}

// ServeFixtures returns a handler that answers "METHOD /path?query" keys with
// the named fixture files (status 200) and 404 for anything else. The query
// part is optional in a key and compared in canonical (sorted) form; a key
// without a query matches any query string.
func ServeFixtures(t testing.TB, routes map[string]string) http.Handler {
	t.Helper()
	canon := map[string][]byte{}
	for key, file := range routes {
		canon[canonKey(key)] = Fixture(t, file)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.EscapedPath()
		if p := "/api/v1"; len(path) >= len(p) && path[:len(p)] == p {
			path = path[len(p):]
		}
		for _, k := range []string{
			r.Method + " " + path + "?" + r.URL.Query().Encode(),
			r.Method + " " + path,
		} {
			if body, ok := canon[k]; ok {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(body)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"not found"}`))
	})
}

// canonKey normalises "METHOD /path?query" so the query is sorted.
func canonKey(key string) string {
	method, rest := key, ""
	for i := 0; i < len(key); i++ {
		if key[i] == ' ' {
			method, rest = key[:i], key[i+1:]
			break
		}
	}
	if rest == "" {
		return key
	}
	path, query := rest, ""
	for i := 0; i < len(rest); i++ {
		if rest[i] == '?' {
			path, query = rest[:i], rest[i+1:]
			break
		}
	}
	if query == "" {
		return method + " " + path
	}
	return method + " " + path + "?" + canonQuery(query)
}

func canonQuery(q string) string {
	v, err := url.ParseQuery(q)
	if err != nil {
		return q
	}
	return v.Encode()
}
