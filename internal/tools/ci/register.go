package ci

import "github.com/AndreyQuantum/forgejo-mcp/internal/tools"

// Register adds every tool of this area to r.
func Register(r *tools.Registry) {
	registerCIFailure(r)
	registerCIWait(r)
	registerChecks(r)
	registerActionsConfig(r)
}
