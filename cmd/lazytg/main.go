// Package main is the lazytg CLI entry point.
package main

import (
	"os"

	"github.com/kar43lov/lazytg/cmd/lazytg/cmd"
)

func main() {
	os.Exit(cmd.Execute())
}
