// Command canon rewrites the schema YAML under a directory with its top-level
// entries sorted by name, so that two trees can be diffed without the diff
// depending on the order dbt happened to list its nodes in. scripts/parity.sh
// runs it over both sides before comparing them.
//
//	go run ./scripts/canon DIR...
//
// See internal/canon for why the order cannot be compared directly.
package main

import (
	"fmt"
	"os"

	"github.com/rognerud/dbt-ditto/internal/canon"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: canon DIR...")
		os.Exit(2)
	}
	for _, root := range os.Args[1:] {
		if err := canon.Dir(root); err != nil {
			fmt.Fprintf(os.Stderr, "canon: %v\n", err)
			os.Exit(1)
		}
	}
}
