package content

import "github.com/AndreyQuantum/forgejo-mcp/internal/tools"

// Register adds every tool of this area to r.
func Register(r *tools.Registry) {
	registerRelease(r)
	registerFile(r)
	registerCommit(r)
}
