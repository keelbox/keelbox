// Command keelbox provisions an Ubuntu machine as a box for AI coding
// agents, from one YAML config.
package main

import (
	"os"

	"github.com/keelbox/keelbox/internal/cli"
)

func main() { os.Exit(cli.Main(os.Args[1:], os.Stdout, os.Stderr)) }
