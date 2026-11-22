package actions

import "github.com/AndreyQuantum/forgejo-mcp/internal/tools"

// Register adds every tool of this area to r.
func Register(r *tools.Registry) {
	registerRun(r)
}

// registerRun registers the run tool.
func registerRun(r *tools.Registry) {
	r.Add(runSpec())
}
