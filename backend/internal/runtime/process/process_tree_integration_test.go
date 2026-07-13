package process

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type processTreeFixture struct {
	Platform string `json:"platform"`
	Launch   struct {
		Executable string   `json:"executable"`
		Args       []string `json:"args"`
		Shell      bool     `json:"shell"`
		CWD        string   `json:"cwd"`
	} `json:"launch"`
	Scenarios []struct {
		ID              string `json:"id"`
		RawOutputStored *bool  `json:"raw_output_persisted"`
	} `json:"scenarios"`
}

func TestP12ProcessTreeFixturesRemainFakeAndShellFree(t *testing.T) {
	for _, name := range []string{"unix-process-tree.json", "windows-process-tree.json"} {
		var fixture processTreeFixture
		if err := json.Unmarshal(loadProcessFixture(t, name), &fixture); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if fixture.Platform == "" || fixture.Launch.Executable == "" || fixture.Launch.Shell || fixture.Launch.CWD != "<fixture-workspace>" || len(fixture.Launch.Args) == 0 {
			t.Fatalf("unsafe process fixture %s: %#v", name, fixture)
		}
		for _, scenario := range fixture.Scenarios {
			if scenario.ID == "" {
				t.Fatalf("unnamed process scenario in %s", name)
			}
			if scenario.RawOutputStored != nil && *scenario.RawOutputStored {
				t.Fatalf("raw output persistence allowed in %s/%s", name, scenario.ID)
			}
		}
	}
}

func TestP12TreeTerminationIsNilSafeBeforeAnyChildLaunch(t *testing.T) {
	if err := terminateTree(nil, false); err != nil {
		t.Fatalf("graceful nil termination: %v", err)
	}
	if err := terminateTree(nil, true); err != nil {
		t.Fatalf("forced nil termination: %v", err)
	}
}

// TestP12ProcessTreeHelper is a fake executable made from the current Go test
// binary. It starts no shell or CLI and receives a minimal environment; the
// parent helper deliberately blocks after creating one descendant so the
// platform tree terminator must reap both levels.
func TestP12ProcessTreeHelper(t *testing.T) {
	if os.Getenv("AUTOPLAN_P12_PROCESS_HELPER") != "1" {
		return
	}
	if strings.HasSuffix(strings.Join(os.Args, " "), " child") {
		select {}
	}
	child := exec.Command(os.Args[0], "-test.run=^TestP12ProcessTreeHelper$", "--", "child")
	child.Env = helperEnvironment()
	if err := child.Start(); err != nil {
		os.Exit(70)
	}
	select {}
}

func TestP12ForceTreeTerminationReapsFakeDescendants(t *testing.T) {
	command := exec.Command(os.Args[0], "-test.run=^TestP12ProcessTreeHelper$", "--", "parent")
	command.Env = helperEnvironment()
	prepareTree(command)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	reaped := false
	t.Cleanup(func() {
		if !reaped {
			_ = terminateTree(command, true)
		}
	})
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		t.Fatalf("fake tree exited before termination: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := terminateTree(command, true); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
		// A forced tree kill normally makes Wait return a non-nil exit error.
		// Reaching Wait is the no-zombie assertion for the direct child.
		reaped = true
	case <-time.After(5 * time.Second):
		t.Fatal("fake process tree was not reaped")
	}
}

func helperEnvironment() []string {
	result := []string{"AUTOPLAN_P12_PROCESS_HELPER=1"}
	for _, name := range []string{"SystemRoot", "SYSTEMROOT", "WINDIR", "ComSpec", "PATH", "Path"} {
		if value := os.Getenv(name); value != "" {
			result = append(result, name+"="+value)
		}
	}
	return result
}

func loadProcessFixture(t *testing.T, name string) []byte {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if info, statErr := os.Stat(filepath.Join(directory, "go.mod")); statErr == nil && !info.IsDir() {
			bytes, readErr := os.ReadFile(filepath.Join(filepath.Dir(directory), "fixtures", "migration", "p12", "process-fixtures", name))
			if readErr != nil {
				t.Fatal(readErr)
			}
			return bytes
		}
		next := filepath.Dir(directory)
		if next == directory {
			t.Fatal("repository root unavailable")
		}
		directory = next
	}
}
