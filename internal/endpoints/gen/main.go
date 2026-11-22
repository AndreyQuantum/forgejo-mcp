// Command gen converts a Forgejo swagger.json into internal/endpoints/index.json.
// WP5 implements it; flags: -in (file or http(s) URL), -version, -out.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "gen: not implemented (WP5)")
	os.Exit(1)
}
