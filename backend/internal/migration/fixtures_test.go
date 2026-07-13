package migration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"testing"

	"github.com/lyming99/autoplan/backend/migrations"
)

type generatedFixtureArtifact struct {
	ID                        string `json:"id"`
	File                      string `json:"file"`
	Classification            string `json:"classification"`
	SourceUserVersion         *int   `json:"source_user_version"`
	ExpectedTargetUserVersion *int   `json:"expected_target_user_version"`
	ExpectedResult            string `json:"expected_result"`
	ExpectedCode              any    `json:"expected_code"`
	ByteSize                  int64  `json:"byte_size"`
	SHA256                    string `json:"sha256"`
}

type generatedFixtureManifest struct {
	FormatVersion    int                        `json:"format_version"`
	FixtureSet       string                     `json:"fixture_set"`
	GeneratedBy      string                     `json:"generated_by"`
	RecipeSHA256     string                     `json:"recipe_sha256"`
	NodeSourceSHA256 string                     `json:"node_schema_source_sha256"`
	MigrationSHA256  string                     `json:"migration_source_sha256"`
	FixedTime        string                     `json:"fixed_time"`
	DatabaseContent  bool                       `json:"database_content_in_manifest"`
	Artifacts        []generatedFixtureArtifact `json:"artifacts"`
}

func p04RepositoryRoot(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate fixture test source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", ".."))
}

func digestFileForFixtureTest(t *testing.T, name string) string {
	t.Helper()
	content, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

func generateP04Fixtures(t *testing.T) (string, generatedFixtureManifest) {
	t.Helper()
	root := p04RepositoryRoot(t)
	output := filepath.Join(t.TempDir(), "generated")
	trackedInputs := []string{
		filepath.Join(root, "fixtures", "migration", "p04", "manifest.json"),
		filepath.Join(root, "src", "database.js"),
		filepath.Join(root, "backend", "migrations", "0001_schema_v1.sql"),
	}
	before := make(map[string]string, len(trackedInputs))
	for _, input := range trackedInputs {
		before[input] = digestFileForFixtureTest(t, input)
	}
	command := exec.CommandContext(
		context.Background(),
		"node",
		filepath.Join(root, "scripts", "migration-p04", "generate-fixtures.js"),
		"--output",
		output,
	)
	command.Dir = root
	if result, err := command.CombinedOutput(); err != nil {
		t.Fatalf("fixture generator failed: %v (%s)", err, bytes.TrimSpace(result))
	}
	for _, input := range trackedInputs {
		if actual := digestFileForFixtureTest(t, input); actual != before[input] {
			t.Fatalf("fixture generator modified source input %s", filepath.Base(input))
		}
	}
	manifestBytes, err := os.ReadFile(filepath.Join(output, "generated-manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(manifestBytes, []byte(output)) || bytes.Contains(manifestBytes, []byte(root)) {
		t.Fatal("generated fixture manifest disclosed an absolute path")
	}
	var manifest generatedFixtureManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatal(err)
	}
	return output, manifest
}

func TestGeneratedFixtureManifestPinsCompleteDeterministicInventory(t *testing.T) {
	output, manifest := generateP04Fixtures(t)
	if manifest.FormatVersion != 1 || manifest.FixtureSet != "autoplan-p04-copy-migration" ||
		manifest.DatabaseContent || manifest.FixedTime != "2026-07-11T04:00:00.000Z" ||
		manifest.MigrationSHA256 != migrations.SchemaV1Checksum || len(manifest.Artifacts) != 18 {
		t.Fatalf("unexpected generated manifest metadata: %#v", manifest)
	}
	seen := make(map[string]bool, len(manifest.Artifacts))
	for _, artifact := range manifest.Artifacts {
		if seen[artifact.ID] || filepath.Base(artifact.File) != artifact.File ||
			len(artifact.SHA256) != 64 || artifact.ByteSize < 0 {
			t.Fatalf("invalid generated artifact: %#v", artifact)
		}
		seen[artifact.ID] = true
		name := filepath.Join(output, artifact.File)
		info, err := os.Stat(name)
		if err != nil || !info.Mode().IsRegular() || info.Size() != artifact.ByteSize {
			t.Fatalf("artifact size mismatch for %s: %v", artifact.ID, err)
		}
		if digestFileForFixtureTest(t, name) != artifact.SHA256 {
			t.Fatalf("artifact checksum mismatch for %s", artifact.ID)
		}
	}
	required := []string{
		"empty-file", "empty-sqlite", "initial-single-project", "ensure-column-intermediate",
		"scan-files-old-primary-key", "no-intake-plan-links", "project-ai-configs",
		"chat-without-conversation", "current-node-valid", "valid-edge-data", "schema-v1",
		"orphan-relations", "invalid-paths", "foreign-key-conflict",
		"schema-checksum-drift", "schema-object-drift", "corrupt-page", "truncated-file",
	}
	sort.Strings(required)
	actual := make([]string, 0, len(seen))
	for id := range seen {
		actual = append(actual, id)
	}
	sort.Strings(actual)
	if !equalStrings(actual, required) {
		t.Fatalf("fixture inventory = %v, want %v", actual, required)
	}
}

func TestGeneratedFixtureHeadersMatchDeclaredSourceVersions(t *testing.T) {
	output, manifest := generateP04Fixtures(t)
	home, _ := os.UserHomeDir()
	for _, artifact := range manifest.Artifacts {
		content, err := os.ReadFile(filepath.Join(output, artifact.File))
		if err != nil {
			t.Fatal(err)
		}
		if home != "" && bytes.Contains(content, []byte(home)) {
			t.Fatalf("fixture %s contains the real home path", artifact.ID)
		}
		if artifact.ID == "empty-file" {
			if len(content) != 0 || artifact.SourceUserVersion == nil || *artifact.SourceUserVersion != 0 {
				t.Fatalf("empty fixture declaration mismatch: %#v", artifact)
			}
			continue
		}
		if artifact.Classification == "invalid-file" {
			continue
		}
		if len(content) < 64 || !bytes.Equal(content[:16], []byte("SQLite format 3\x00")) ||
			artifact.SourceUserVersion == nil {
			t.Fatalf("fixture %s is not declared SQLite", artifact.ID)
		}
		version := int(binary.BigEndian.Uint32(content[60:64]))
		if version != *artifact.SourceUserVersion {
			t.Fatalf("fixture %s user_version = %d, want %d", artifact.ID, version, *artifact.SourceUserVersion)
		}
		if artifact.ExpectedResult == "migrated" && artifact.ExpectedTargetUserVersion == nil {
			t.Fatalf("fixture %s lacks migration target", artifact.ID)
		}
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
