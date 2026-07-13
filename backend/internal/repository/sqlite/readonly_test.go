package sqlite

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lyming99/autoplan/backend/internal/repository"
)

const testPageSize = 4096

type fixtureValue struct {
	kind valueKind
	i64  int64
	text string
}

func TestOpenRejectsUnsafeTargets(t *testing.T) {
	root := t.TempDir()
	valid := writeFixtureDatabase(t, root, "fixture.sqlite", nil)
	production := writeBytes(t, root, "autoplan.sqlite", []byte("SQLite format 3\x00"))
	outsideRoot := t.TempDir()
	outside := writeBytes(t, outsideRoot, "outside.sqlite", []byte("SQLite format 3\x00"))
	copyPath := writeFixtureDatabase(t, root, "fixture.copy", nil)

	tests := []struct {
		name    string
		options Options
	}{
		{"empty", Options{}},
		{"uri", Options{Path: "file:" + valid + "?mode=ro", AllowedRoot: root, Kind: TargetFixture}},
		{"production name", Options{Path: production, AllowedRoot: root, Kind: TargetFixture}},
		{"outside root", Options{Path: outside, AllowedRoot: root, Kind: TargetFixture}},
		{"copy not declared", Options{Path: copyPath, AllowedRoot: root, Kind: TargetDatabaseCopy}},
		{"fixture as copy", Options{Path: valid, AllowedRoot: root, Kind: TargetDatabaseCopy, DeclaredSanitized: true}},
		{"unknown kind", Options{Path: valid, AllowedRoot: root, Kind: "other"}},
	}
	for _, item := range tests {
		t.Run(item.name, func(t *testing.T) {
			var before [sha256.Size]byte
			if item.options.Path != "" && !strings.HasPrefix(item.options.Path, "file:") {
				if _, statErr := os.Stat(item.options.Path); statErr == nil {
					before = fileHash(t, item.options.Path)
				}
			}
			if _, err := Open(context.Background(), item.options); !errors.Is(err, repository.ErrUnsafePath) {
				t.Fatalf("expected stable unsafe-path error, got %v", err)
			} else if strings.Contains(err.Error(), root) || strings.Contains(strings.ToLower(err.Error()), "token") {
				t.Fatal("unsafe-path error leaked path or credential material")
			}
			if before != ([sha256.Size]byte{}) && before != fileHash(t, item.options.Path) {
				t.Fatal("rejected target bytes changed")
			}
		})
	}

	if err := os.WriteFile(valid+"-wal", []byte("active"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), Options{Path: valid, AllowedRoot: root, Kind: TargetFixture});
		!errors.Is(err, repository.ErrUnsafePath) {
		t.Fatalf("active WAL was not rejected: %v", err)
	}
}

func TestOpenRejectsSymlinkComponents(t *testing.T) {
	root := t.TempDir()
	realDirectory := filepath.Join(root, "real")
	if err := os.Mkdir(realDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFixtureDatabase(t, realDirectory, "fixture.sqlite", nil)
	link := filepath.Join(root, "linked")
	if err := os.Symlink(realDirectory, link); err != nil {
		// Windows without Developer Mode may deny symlink creation. The path
		// validator itself remains platform-independent and is exercised when
		// the host permits creation.
		t.Log("host denied synthetic symlink creation")
		return
	}
	target := filepath.Join(link, "fixture.sqlite")
	if _, err := Open(context.Background(), Options{Path: target, AllowedRoot: root, Kind: TargetFixture});
		!errors.Is(err, repository.ErrUnsafePath) {
		t.Fatalf("symlink component was accepted: %v", err)
	}
}

func TestOpenAcceptsFixtureAndDeclaredCopyWithoutChangingBytes(t *testing.T) {
	root := t.TempDir()
	fixture := writeFixtureDatabase(t, root, "fixture.sqlite", nil)
	copyPath := writeFixtureDatabase(t, root, "fixture.sqlite.copy", nil)
	for _, options := range []Options{
		{Path: fixture, AllowedRoot: root, Kind: TargetFixture},
		{Path: copyPath, AllowedRoot: root, Kind: TargetDatabaseCopy, DeclaredSanitized: true},
	} {
		before := fileHash(t, options.Path)
		reader, err := Open(context.Background(), options)
		if err != nil {
			t.Fatalf("open failed: %v", err)
		}
		if err := reader.Check(context.Background()); err != nil {
			t.Fatalf("check failed: %v", err)
		}
		if err := reader.Close(); err != nil {
			t.Fatalf("close failed: %v", err)
		}
		if before != fileHash(t, options.Path) {
			t.Fatal("read-only repository changed fixture bytes")
		}
	}
	readOnly := writeFixtureDatabase(t, root, "read-only.sqlite", nil)
	if err := os.Chmod(readOnly, 0o400); err != nil {
		t.Fatal(err)
	}
	reader, err := Open(context.Background(), Options{Path: readOnly, AllowedRoot: root, Kind: TargetFixture})
	if err != nil {
		t.Fatalf("read-only file was rejected: %v", err)
	}
	_ = reader.Close()
}

func TestOpenRejectsCorruptionAndFrozenSchemaDrift(t *testing.T) {
	root := t.TempDir()
	corrupt := writeBytes(t, root, "corrupt.sqlite", make([]byte, testPageSize))
	if _, err := Open(context.Background(), Options{Path: corrupt, AllowedRoot: root, Kind: TargetFixture});
		!errors.Is(err, repository.ErrInvalidStore) {
		t.Fatalf("corrupt database was accepted: %v", err)
	}

	drifted := writeFixtureDatabase(t, root, "drift.sqlite", func(schema string) string {
		return strings.Replace(schema, "description TEXT NOT NULL DEFAULT ''", "description BLOB", 1)
	})
	if _, err := Open(context.Background(), Options{Path: drifted, AllowedRoot: root, Kind: TargetFixture});
		!errors.Is(err, repository.ErrSchemaDrift) {
		t.Fatalf("schema drift was accepted: %v", err)
	}
}

func TestContextCloseAndSourceChangeFailClosed(t *testing.T) {
	root := t.TempDir()
	fixture := writeFixtureDatabase(t, root, "fixture.sqlite", nil)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Open(cancelled, Options{Path: fixture, AllowedRoot: root, Kind: TargetFixture}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled open returned %v", err)
	}

	reader, err := Open(context.Background(), Options{Path: fixture, AllowedRoot: root, Kind: TargetFixture})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.ListProjects(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled query returned %v", err)
	}
	content, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	content[len(content)-1] ^= 1
	if err := os.WriteFile(fixture, content, 0o600); err != nil {
		t.Fatal(err)
	}
	changed := time.Now().Add(time.Second)
	if err := os.Chtimes(fixture, changed, changed); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.ListProjects(context.Background()); !errors.Is(err, repository.ErrSourceChanged) {
		t.Fatalf("changed source returned %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.ListProjects(context.Background()); !errors.Is(err, repository.ErrClosed) {
		t.Fatalf("closed repository returned %v", err)
	}
}

func writeFixtureDatabase(t *testing.T, root, name string, mutate func(string) string) string {
	t.Helper()
	schemas := map[string]string{
		"settings": "CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL)",
		"projects": "CREATE TABLE projects (id INTEGER PRIMARY KEY, name TEXT NOT NULL, workspace_path TEXT NOT NULL DEFAULT '', description TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, updated_at TEXT NOT NULL)",
		"project_states": projectStateSchema(),
	}
	if mutate != nil {
		for key, schema := range schemas {
			schemas[key] = mutate(schema)
		}
	}
	pages := make([]byte, testPageSize*4)
	copy(pages[:16], []byte("SQLite format 3\x00"))
	binary.BigEndian.PutUint16(pages[16:18], testPageSize)
	pages[18], pages[19] = 1, 1
	pages[21], pages[22], pages[23] = 64, 32, 32
	binary.BigEndian.PutUint32(pages[24:28], 1)
	binary.BigEndian.PutUint32(pages[28:32], 4)
	binary.BigEndian.PutUint32(pages[40:44], 1)
	binary.BigEndian.PutUint32(pages[44:48], 4)
	binary.BigEndian.PutUint32(pages[56:60], 1)
	binary.BigEndian.PutUint32(pages[92:96], 1)
	binary.BigEndian.PutUint32(pages[96:100], 3045000)

	schemaCells := [][]byte{
		fixtureCell(1, fixtureText("table"), fixtureText("settings"), fixtureText("settings"), fixtureInteger(2), fixtureText(schemas["settings"])),
		fixtureCell(2, fixtureText("table"), fixtureText("projects"), fixtureText("projects"), fixtureInteger(3), fixtureText(schemas["projects"])),
		fixtureCell(3, fixtureText("table"), fixtureText("project_states"), fixtureText("project_states"), fixtureInteger(4), fixtureText(schemas["project_states"])),
	}
	writeLeafPage(t, pages[:testPageSize], 100, schemaCells)
	writeLeafPage(t, pages[testPageSize:2*testPageSize], 0, [][]byte{
		fixtureCell(1, fixtureText("mcp.authToken"), fixtureText("fixture-not-a-token")),
		fixtureCell(2, fixtureText("mcp.enabled"), fixtureText("false")),
	})
	writeLeafPage(t, pages[2*testPageSize:3*testPageSize], 0, [][]byte{
		projectCell(1, "Synthetic Alpha", "alpha", "", "2026-01-02T03:04:07.000Z"),
		projectCell(2, "Synthetic Beta", "beta", "coverage", "2026-01-02T03:04:08.000Z"),
		projectCell(3, "Synthetic Gamma", "gamma", "tie", "2026-01-02T03:04:08.000Z"),
	})
	writeLeafPage(t, pages[3*testPageSize:], 0, [][]byte{projectStateCell()})
	return writeBytes(t, root, name, pages)
}

func projectStateSchema() string {
	return "CREATE TABLE project_states (" + strings.Join([]string{
		"project_id INTEGER PRIMARY KEY", "running INTEGER NOT NULL DEFAULT 0", "phase TEXT NOT NULL DEFAULT 'idle'",
		"interval_seconds INTEGER NOT NULL DEFAULT 5", "validation_command TEXT NOT NULL DEFAULT ''", "project_prompt TEXT NOT NULL DEFAULT ''",
		"agent_cli_provider TEXT NOT NULL DEFAULT 'codex'", "agent_cli_command TEXT NOT NULL DEFAULT ''", "codex_reasoning_effort TEXT",
		"plan_generation_strategy TEXT NOT NULL DEFAULT 'external-cli-markdown'", "plan_generation_provider TEXT",
		"plan_generation_command TEXT NOT NULL DEFAULT ''", "plan_generation_model TEXT NOT NULL DEFAULT ''", "plan_generation_codex_reasoning_effort TEXT",
		"plan_generation_claude_base_url TEXT NOT NULL DEFAULT ''", "plan_generation_claude_auth_token TEXT NOT NULL DEFAULT ''",
		"plan_generation_claude_model TEXT NOT NULL DEFAULT ''", "plan_execution_strategy TEXT NOT NULL DEFAULT 'external-cli'",
		"plan_execution_provider TEXT", "plan_execution_command TEXT NOT NULL DEFAULT ''", "plan_execution_model TEXT NOT NULL DEFAULT ''",
		"plan_execution_codex_reasoning_effort TEXT", "plan_execution_claude_base_url TEXT NOT NULL DEFAULT ''",
		"plan_execution_claude_auth_token TEXT NOT NULL DEFAULT ''", "plan_execution_claude_model TEXT NOT NULL DEFAULT ''",
		"last_issue_hash TEXT", "last_error TEXT", "env_vars TEXT NOT NULL DEFAULT ''", "updated_at TEXT NOT NULL",
		"plan_generation_claude_config_id INTEGER NOT NULL DEFAULT 0", "plan_execution_claude_config_id INTEGER NOT NULL DEFAULT 0",
	}, ", ") + ")"
}

func projectCell(id int64, name, workspace, description, updated string) []byte {
	return fixtureCell(id, fixtureNull(), fixtureText(name), fixtureText(workspace), fixtureText(description),
		fixtureText("2026-01-02T03:04:05.000Z"), fixtureText(updated))
}

func projectStateCell() []byte {
	values := []fixtureValue{
		fixtureNull(), fixtureInteger(0), fixtureText("idle"), fixtureInteger(9), fixtureText(""), fixtureText("Synthetic prompt"),
		fixtureText("claude"), fixtureText(""), fixtureNull(), fixtureText("external-cli-structured"), fixtureText("claude"),
		fixtureText(""), fixtureText(""), fixtureNull(), fixtureText("https://example.invalid/claude"), fixtureText("fixture-secret"),
		fixtureText("synthetic-model"), fixtureText("external-cli"), fixtureText("codex"), fixtureText(""), fixtureText(""),
		fixtureText("medium"), fixtureText(""), fixtureText(""), fixtureText(""), fixtureNull(), fixtureNull(),
		fixtureText("[{\"name\":\"FIXTURE\",\"value\":\"private\"}]"), fixtureText("2026-01-02T03:04:07.000Z"),
		fixtureInteger(0), fixtureInteger(0),
	}
	return fixtureCell(1, values...)
}

func fixtureNull() fixtureValue { return fixtureValue{kind: kindNull} }
func fixtureInteger(value int64) fixtureValue { return fixtureValue{kind: kindInteger, i64: value} }
func fixtureText(value string) fixtureValue { return fixtureValue{kind: kindText, text: value} }

func fixtureCell(rowID int64, values ...fixtureValue) []byte {
	record := fixtureRecord(values)
	result := append(encodeVarint(uint64(len(record))), encodeVarint(uint64(rowID))...)
	return append(result, record...)
}

func fixtureRecord(values []fixtureValue) []byte {
	serials := make([]byte, 0, len(values))
	body := make([]byte, 0)
	for _, item := range values {
		switch item.kind {
		case kindNull:
			serials = append(serials, 0)
		case kindInteger:
			if item.i64 == 0 {
				serials = append(serials, 8)
			} else if item.i64 == 1 {
				serials = append(serials, 9)
			} else {
				serials = append(serials, 1)
				body = append(body, byte(item.i64))
			}
		case kindText:
			serials = append(serials, encodeVarint(uint64(13+2*len([]byte(item.text))))...)
			body = append(body, []byte(item.text)...)
		default:
			panic("unsupported fixture value")
		}
	}
	headerSize := len(serials) + 1
	for len(encodeVarint(uint64(headerSize)))+len(serials) != headerSize {
		headerSize = len(encodeVarint(uint64(headerSize))) + len(serials)
	}
	header := append(encodeVarint(uint64(headerSize)), serials...)
	return append(header, body...)
}

func encodeVarint(value uint64) []byte {
	if value <= 0x7f {
		return []byte{byte(value)}
	}
	groups := make([]byte, 0, 9)
	for value > 0 {
		groups = append(groups, byte(value&0x7f))
		value >>= 7
	}
	result := make([]byte, len(groups))
	for index := range groups {
		result[index] = groups[len(groups)-1-index]
		if index < len(groups)-1 {
			result[index] |= 0x80
		}
	}
	return result
}

func writeLeafPage(t *testing.T, page []byte, header int, cells [][]byte) {
	t.Helper()
	page[header] = 0x0d
	binary.BigEndian.PutUint16(page[header+3:header+5], uint16(len(cells)))
	contentStart := len(page)
	for index, cell := range cells {
		contentStart -= len(cell)
		if contentStart < header+8+2*len(cells) {
			t.Fatal("test fixture page overflow")
		}
		copy(page[contentStart:], cell)
		binary.BigEndian.PutUint16(page[header+8+index*2:header+10+index*2], uint16(contentStart))
	}
	binary.BigEndian.PutUint16(page[header+5:header+7], uint16(contentStart))
}

func writeBytes(t *testing.T, root, name string, content []byte) string {
	t.Helper()
	target := filepath.Join(root, name)
	if err := os.WriteFile(target, content, 0o600); err != nil {
		t.Fatal(err)
	}
	return target
}

func fileHash(t *testing.T, target string) [sha256.Size]byte {
	t.Helper()
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(content)
}
