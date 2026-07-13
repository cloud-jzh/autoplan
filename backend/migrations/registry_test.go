package migrations

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestCanonicalMigrationSQLIsCheckoutIndependent(t *testing.T) {
	want := "CREATE TABLE example (id INTEGER);\nSELECT 1;\n"
	inputs := []string{
		"CREATE TABLE example (id INTEGER);\nSELECT 1;\n",
		"CREATE TABLE example (id INTEGER);\r\nSELECT 1;\r\n",
		"CREATE TABLE example (id INTEGER);\rSELECT 1;\r",
	}
	for _, input := range inputs {
		if got := canonicalMigrationSQL(input); got != want {
			t.Fatalf("canonical migration SQL mismatch: %q", got)
		}
	}
}

func TestRegistryUsesHistoricalChecksumsForEveryCheckout(t *testing.T) {
	registry := NewRegistry(NewCatalog())
	if err := registry.Validate(); err != nil {
		t.Fatalf("registry validation failed: %v", err)
	}
	for _, migration := range registry.Migrations() {
		digest := sha256.Sum256([]byte(migration.SQL))
		if got := hex.EncodeToString(digest[:]); got != migration.Checksum {
			t.Fatalf("migration %d checksum = %s, want %s", migration.Version, got, migration.Checksum)
		}
		if strings.Contains(migration.SQL, "\r") {
			t.Fatalf("migration %d contains a non-canonical line ending", migration.Version)
		}
	}
}
