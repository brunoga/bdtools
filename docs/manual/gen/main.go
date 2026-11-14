// Command gen writes the manual's generated pages beside it (go generate
// in docs/manual): the documents in docs/ made HTML by manual.Build.
package main

import (
	"fmt"
	"os"

	"github.com/brunoga/bdtools/docs/manual"
)

func main() {
	out, err := manual.Build(os.DirFS(".."))
	if err != nil {
		fmt.Fprintln(os.Stderr, "gen:", err)
		os.Exit(1)
	}
	for name, b := range out {
		if err := os.WriteFile(name, b, 0o644); err != nil { //nolint:gosec // the manual's pages
			fmt.Fprintln(os.Stderr, "gen:", err)
			os.Exit(1)
		}
	}
}
