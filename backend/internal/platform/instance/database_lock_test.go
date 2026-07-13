package instance

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

var helperHeldDatabaseLock *DatabaseLock

func TestDatabaseLockRejectsAliasesAndAllowsIndependentCopies(t *testing.T) {
	directory := t.TempDir()
	firstPath := filepath.Join(directory, "first.sqlite.copy")
	secondPath := filepath.Join(directory, "second.sqlite.copy")
	for _, target := range []string{firstPath, secondPath} {
		if err := os.WriteFile(target, []byte("synthetic"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	first, err := AcquireDatabaseLock(context.Background(), DatabaseLockOptions{Target: firstPath, Timeout: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close(context.Background())

	alias := filepath.Join(directory, "child", "..", filepath.Base(firstPath))
	if _, err := AcquireDatabaseLock(context.Background(), DatabaseLockOptions{Target: alias, Timeout: 30 * time.Millisecond}); !errors.Is(err, ErrDatabaseOwnerLocked) {
		t.Fatalf("alias lock error = %v", err)
	}
	if runtime.GOOS == "windows" {
		if _, err := AcquireDatabaseLock(context.Background(), DatabaseLockOptions{Target: strings.ToUpper(firstPath), Timeout: 30 * time.Millisecond}); !errors.Is(err, ErrDatabaseOwnerLocked) {
			t.Fatalf("case alias lock error = %v", err)
		}
	}

	link := filepath.Join(directory, "first-link.sqlite.copy")
	if err := os.Symlink(firstPath, link); err == nil {
		if _, err := AcquireDatabaseLock(context.Background(), DatabaseLockOptions{Target: link, Timeout: 30 * time.Millisecond}); !errors.Is(err, ErrDatabaseOwnerLocked) {
			t.Fatalf("symlink alias lock error = %v", err)
		}
	}

	second, err := AcquireDatabaseLock(context.Background(), DatabaseLockOptions{Target: secondPath, Timeout: 50 * time.Millisecond})
	if err != nil {
		t.Fatalf("independent copy lock error = %v", err)
	}
	if second.DatabaseID() == first.DatabaseID() {
		t.Fatal("independent copies share a database identity")
	}
	if err := second.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestDatabaseLockMetadataIsSanitizedAndStaleMetadataDoesNotOwnLock(t *testing.T) {
	target := filepath.Join(t.TempDir(), "database.sqlite.copy")
	if err := os.WriteFile(target, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	identity, err := resolveDatabaseIdentity(target, false)
	if err != nil {
		t.Fatal(err)
	}
	metadata := identity.canonicalPath + ".autoplan-owner.lock"
	stale := databaseOwnerRecord{
		Version: databaseOwnerProtocolVersion, DatabaseID: identity.databaseID,
		PIDDigest: "pid-digest", OwnerDigest: "stale-owner", Ports: identity.ports,
	}
	content, _ := json.Marshal(stale)
	if err := os.WriteFile(metadata, append(content, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	lock, err := AcquireDatabaseLock(context.Background(), DatabaseLockOptions{Target: target, Timeout: 50 * time.Millisecond})
	if err != nil {
		t.Fatalf("stale metadata blocked acquisition: %v", err)
	}
	active, err := os.ReadFile(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(active), target) || strings.Contains(string(active), filepath.Dir(target)) ||
		strings.Contains(string(active), "stale-owner") {
		t.Fatalf("owner metadata is not sanitized: %s", active)
	}
	if err := lock.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(metadata); !os.IsNotExist(err) {
		t.Fatalf("owned metadata was not removed: %v", err)
	}
}

func TestDatabaseLockDoesNotDeleteReplacementMetadata(t *testing.T) {
	target := filepath.Join(t.TempDir(), "database.sqlite.copy")
	if err := os.WriteFile(target, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	lock, err := AcquireDatabaseLock(context.Background(), DatabaseLockOptions{Target: target, Timeout: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	replacement := []byte("{\"version\":1,\"owner_digest\":\"replacement\"}\n")
	if err := os.WriteFile(lock.metadata, replacement, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(context.Background()); !errors.Is(err, ErrDatabaseOwnerRelease) {
		t.Fatalf("replacement close error = %v", err)
	}
	actual, err := os.ReadFile(lock.metadata)
	if err != nil || string(actual) != string(replacement) {
		t.Fatalf("replacement metadata changed: %q, %v", actual, err)
	}
	_ = os.Remove(lock.metadata)
}

func TestDatabaseLockIsCrossProcessAndReleasedAfterCrash(t *testing.T) {
	if os.Getenv("AUTOPLAN_DATABASE_LOCK_HELPER") == "1" {
		target := os.Getenv("AUTOPLAN_DATABASE_LOCK_TARGET")
		lock, err := AcquireDatabaseLock(context.Background(), DatabaseLockOptions{Target: target, Timeout: time.Second})
		if err != nil {
			os.Exit(11)
		}
		helperHeldDatabaseLock = lock
		fmt.Println("ready")
		for {
			time.Sleep(time.Minute)
		}
	}

	target := filepath.Join(t.TempDir(), "database.sqlite.copy")
	if err := os.WriteFile(target, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestDatabaseLockIsCrossProcessAndReleasedAfterCrash$")
	command.Env = append(os.Environ(),
		"AUTOPLAN_DATABASE_LOCK_HELPER=1",
		"AUTOPLAN_DATABASE_LOCK_TARGET="+target,
	)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "ready" {
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatalf("helper did not acquire lock: %q, %v", scanner.Text(), scanner.Err())
	}
	if _, err := AcquireDatabaseLock(context.Background(), DatabaseLockOptions{Target: target, Timeout: 50 * time.Millisecond}); !errors.Is(err, ErrDatabaseOwnerLocked) {
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatalf("second process lock error = %v", err)
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = command.Wait()

	lock, err := AcquireDatabaseLock(context.Background(), DatabaseLockOptions{Target: target, Timeout: time.Second})
	if err != nil {
		t.Fatalf("crashed owner left an active lock: %v", err)
	}
	if err := lock.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}
