package tools

// InstructionsInfo carries the values interpolated into the server
// `instructions` text (section 4.1).
type InstructionsInfo struct {
	Host        string
	Version     string // "" before the first tool call
	DefaultRepo string // "" = none
	Toolsets    map[string]bool
	WaitMax     int // seconds
}

// Instructions renders the <= 700 character `instructions` text of section
// 4.1. Sentences about tools in hidden toolsets are omitted.
func Instructions(in InstructionsInfo) string { panic("WP4") }
