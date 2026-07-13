package main

import (
	"context"
	"os"

	"github.com/lyming99/autoplan/backend/internal/bootstrap"
)

func main() {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "mcp-stdio" {
		os.Exit(bootstrap.RunMCPStdioCommand(context.Background(), args[1:], os.Stdin, os.Stdout, os.Stderr))
	}
	os.Exit(bootstrap.RunDaemonCommand(context.Background(), args, os.Stdin, os.Stdout, os.Stderr))
}
