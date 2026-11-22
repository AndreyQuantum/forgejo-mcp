package tools

// Blank imports of every third-party dependency so go.mod is final after WP0
// and no later work package needs to touch it.
import (
	_ "github.com/google/jsonschema-go/jsonschema"
	_ "github.com/modelcontextprotocol/go-sdk/mcp"
	_ "golang.org/x/sync/errgroup"
	_ "gopkg.in/yaml.v3"
)
