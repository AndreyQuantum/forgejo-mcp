package format

// Pick projects a decoded JSON value (any) onto the jq-lite paths of
// section 9.3: expr := path [":" alias]; path := seg ("." seg)*;
// seg := name | name "[]" | name "[" int "]" | "*". A root array is applied
// per element and rendered as JSON Lines; a root object as one object.
// Invalid syntax returns an error "pick: <reason> at <col>".
func Pick(v any, paths []string) (string, error) { panic("WP3") }
