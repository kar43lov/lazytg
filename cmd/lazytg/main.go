// Package main is the lazytg CLI entry point.
package main

import (
	"os"

	"github.com/pgmac/lazytg/cmd/lazytg/cmd"
)

func main() {
	os.Exit(cmd.Execute())
}
