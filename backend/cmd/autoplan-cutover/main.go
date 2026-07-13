// autoplan-cutover is intentionally fixture-only.  It validates that callers
// did not point it at Electron userData or a live database, then reports that
// an explicitly wired runtime is required.  The actual one-way orchestration
// is exposed by internal/application/maintenance and is injected by the
// trusted host; this command never guesses a real database target.
package main

import (
	"encoding/json"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type commandStatus struct {
	Mode             string `json:"mode"`
	Stage            string `json:"stage"`
	Code             string `json:"code"`
	MutationsBlocked bool   `json:"mutations_blocked"`
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout))
}

func run(args []string, output io.Writer) int {
	flags := flag.NewFlagSet("autoplan-cutover", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	fixtureRoot := flags.String("fixture-root", "", "")
	target := flags.String("target", "", "")
	backupDirectory := flags.String("backup-dir", "", "")
	repositoryRoot := flags.String("repository-root", "", "")
	sanitizedCopy := flags.Bool("sanitized-copy", false, "")
	if flags.Parse(args) != nil || flags.NArg() != 0 || !*sanitizedCopy ||
		!safeFixtureInput(*fixtureRoot, *target, *backupDirectory, *repositoryRoot) {
		writeStatus(output, "cutover_input_rejected")
		return 2
	}
	// A production host must construct maintenance.Service with concrete Node
	// drain/release, SQLite audit/migration, and smoke dependencies.  Returning
	// a stable blocked result is safer than opening an unregistered SQL driver
	// or accepting a real userData database from a command line.
	writeStatus(output, "cutover_runtime_unavailable")
	return 2
}

func safeFixtureInput(fixtureRoot, target, backupDirectory, repositoryRoot string) bool {
	if !filepath.IsAbs(fixtureRoot) || !filepath.IsAbs(target) || !filepath.IsAbs(backupDirectory) || !filepath.IsAbs(repositoryRoot) {
		return false
	}
	root := filepath.Clean(fixtureRoot)
	target = filepath.Clean(target)
	backupDirectory = filepath.Clean(backupDirectory)
	if !within(target, root) || !withinOrEqual(backupDirectory, root) || containsUserData(target) || containsUserData(root) {
		return false
	}
	base := strings.ToLower(filepath.Base(target))
	return base != "autoplan.sqlite" && (strings.HasSuffix(base, ".sqlite.copy") || strings.HasSuffix(base, ".sqlite.backup") ||
		strings.HasSuffix(base, ".sqlite.bak") || strings.HasSuffix(base, ".copy") || strings.HasSuffix(base, ".backup") || strings.HasSuffix(base, ".bak"))
}

func within(target, root string) bool {
	relative, err := filepath.Rel(root, target)
	return err == nil && relative != "." && relative != ".." && !filepath.IsAbs(relative) && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func withinOrEqual(target, root string) bool {
	return strings.EqualFold(filepath.Clean(target), filepath.Clean(root)) || within(target, root)
}

func containsUserData(value string) bool {
	value = strings.ToLower(filepath.ToSlash(value))
	return strings.Contains(value, "/appdata/roaming/autoplan") || strings.Contains(value, "/library/application support/autoplan") || strings.Contains(value, "/.config/autoplan")
}

func writeStatus(output io.Writer, code string) {
	_ = json.NewEncoder(output).Encode(commandStatus{Mode: "maintenance", Stage: "failed", Code: code, MutationsBlocked: true})
}
