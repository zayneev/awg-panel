package main

import (
	"os"

	"github.com/zayneev/awg-panel/internal/cli"
)

var version = "dev"

func main() {
	os.Exit(cli.Execute(version))
}
