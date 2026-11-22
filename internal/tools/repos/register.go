package repos

import "github.com/AndreyQuantum/forgejo-mcp/internal/tools"

// Register adds every tool of this area to r.
func Register(r *tools.Registry) {
	registerRepo(r)
	registerBranch(r)
	registerTag(r)
	registerWiki(r)
}
