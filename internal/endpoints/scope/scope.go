// Package scope derives the token scope category of an API operation. It has
// no embed and no internal imports so the generator and the runtime share it.
package scope

// Category returns the scope category (for example "repository", "issue",
// "admin") of an operation, from its method, path template and swagger tag.
// WP5 implements the rules of api-platform.md.
func Category(method, path, tag string) string { panic("WP5") }
