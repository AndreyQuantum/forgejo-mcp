// Package endpoints embeds the generated index of every Forgejo API operation
// (DESIGN.md section 9.5). WP0 commits a placeholder index; WP5 generates it.
package endpoints

//go:generate go run ./gen -in $SWAGGER -version $VER -out index.json

import (
	_ "embed"
	"encoding/json"
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
	Q   [][]string `json:"q,omitempty"`  // [name, type letter, enumOrNote?]
	B   [][]any    `json:"b,omitempty"`  // [name, type letter, required 0|1]
	SC  string     `json:"sc,omitempty"` // token scope category
	Dep bool       `json:"dep,omitempty"`
	W   int        `json:"w,omitempty"` // last path variable is multi-segment
}

// Index is the embedded document.
type Index struct {
	V             string `json:"v"`
	N             int    `json:"n"`
	SwaggerSHA256 string `json:"swagger_sha256"`
	Ops           []Op   `json:"ops"`
}

var (
	once sync.Once
	idx  *Index
	idxE error
)

// Load returns the parsed embedded index.
func Load() (*Index, error) {
	once.Do(func() {
		idx = &Index{}
		idxE = json.Unmarshal(indexJSON, idx)
	})
	return idx, idxE
}

// Search finds operations by space-separated terms (section 9.5). WP5.
func Search(q, tag, method string, limit, page int) (ops []Op, total int) { panic("WP5") }

// Lookup returns the operation with exactly this method and path template, or nil.
func Lookup(method, template string) *Op { panic("WP5") }

// Match finds the operation for a concrete request path (segment-wise match
// on the templates; {var} matches one segment, a wild last variable matches
// the remainder). Returns nil when unknown. WP5.
func Match(method, path string) *Op { panic("WP5") }
