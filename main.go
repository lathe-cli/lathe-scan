package main

import (
	"os"

	"github.com/lathe-cli/lathe-scan/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:]))
}
