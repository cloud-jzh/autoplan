package mcp

import (
	"os"
	"strings"
	"testing"
)

func TestP13BAdaptersDoNotReachRepositoryOrRuntimeImplementations(t *testing.T) {
	for _, relative := range []string{"tools/catalog.go", "tools/handlers.go", "tools/mapper.go"} {
		content, err := os.ReadFile(relative)
		if err != nil {
			t.Fatalf("read %s: %v", relative, err)
		}
		for _, forbidden := range []string{
			"internal/repository", "repository/sqlite", "runtime/process", "database/sql", "exec.Command",
			"os.Open(", "os.ReadFile(", "os.WriteFile(", "os.Stat(", "os.Lstat(",
		} {
			if strings.Contains(string(content), forbidden) {
				t.Fatalf("%s contains forbidden dependency %q", relative, forbidden)
			}
		}
	}
}
