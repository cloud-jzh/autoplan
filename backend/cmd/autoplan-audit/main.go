// autoplan-audit is intentionally fail-closed until it is handed a live,
// owner-locked runtime by bootstrap. It never discovers a database or an
// attachment root from user profiles, environment defaults, or cwd.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"os"
)

type report struct {
	SchemaVersion int    `json:"schema_version"`
	Command       string `json:"command"`
	Mode          string `json:"mode"`
	Status        string `json:"status"`
	Code          string `json:"code"`
}

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout))
}

func run(ctx context.Context, args []string, output io.Writer) int {
	if ctx.Err() != nil {
		_ = json.NewEncoder(output).Encode(report{SchemaVersion: 1, Command: "autoplan-audit", Mode: "read_only", Status: "blocked", Code: "context_cancelled"})
		return 3
	}
	flags := flag.NewFlagSet("autoplan-audit", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	repair := flags.Bool("repair", false, "explicitly request whitelist repair")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		_ = json.NewEncoder(output).Encode(report{SchemaVersion: 1, Command: "autoplan-audit", Mode: "read_only", Status: "blocked", Code: "invalid_arguments"})
		return 2
	}
	mode := "read_only"
	if *repair {
		mode = "repair"
	}
	// Opening a database or attachment root requires the P05 authorized-copy
	// proof and owner lock. This command deliberately has no fallback that
	// could discover or mutate a real Electron profile.
	_ = json.NewEncoder(output).Encode(report{
		SchemaVersion: 1, Command: "autoplan-audit", Mode: mode,
		Status: "blocked", Code: "owner_locked_runtime_required",
	})
	return 3
}
