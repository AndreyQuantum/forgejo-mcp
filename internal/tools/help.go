package tools

// HelpText renders the implicit `help` action: the action table of the tool
// (`name [R|W|D] params(required marked *) - summary`) or, when action is
// non-empty, the full parameter list of that one action. Generated from
// Action.Params/Required/Summary, never hand-written twice.
func HelpText(s Spec, allowed []Action, action string) string { panic("WP4") }
