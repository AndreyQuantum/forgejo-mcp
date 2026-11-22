package format

// StripNoise applies the generic noise stripping of section 9.2 to a decoded
// JSON value: drops null/""/[]/{} values and url-ish keys, collapses nested
// user and repository objects, unwraps list wrappers (the unwrapped total is
// returned, -1 when the value was not a wrapper).
func StripNoise(v any) (out any, total int64) { panic("WP3") }
