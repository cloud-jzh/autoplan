package audit

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"sort"
	"strings"

	"github.com/lyming99/autoplan/backend/internal/repository/sqlite"
)

var (
	ErrAuditInvalid    = errors.New("migration_audit_invalid")
	ErrAuditIncomplete = errors.New("migration_audit_incomplete")
)

type Row interface {
	Scan(...any) error
}

type Rows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close() error
}

type Queryer interface {
	QueryContext(context.Context, string, ...any) (Rows, error)
	QueryRowContext(context.Context, string, ...any) Row
}

type SQLQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type sqlAdapter struct{ source SQLQueryer }

func WrapSQL(source SQLQueryer) Queryer { return sqlAdapter{source: source} }

func (adapter sqlAdapter) QueryContext(ctx context.Context, query string, args ...any) (Rows, error) {
	return adapter.source.QueryContext(ctx, query, args...)
}

func (adapter sqlAdapter) QueryRowContext(ctx context.Context, query string, args ...any) Row {
	return adapter.source.QueryRowContext(ctx, query, args...)
}

type Options struct {
	Phase                     string
	MigrationVersion          int
	ExpectedTables            []string
	ExpectedIndexes           []string
	AuthorizedRoots           []string
	ProjectWorkspacesAreRoots bool
	MaximumRecordIDs          int
	Paths                     PathInspector
}

type Auditor struct {
	database Queryer
	options  Options
}

func New(database Queryer, options Options) (*Auditor, error) {
	phase := safeLabel(options.Phase)
	if database == nil || phase == "" || phase == "invalid" || options.MigrationVersion < 0 {
		return nil, ErrAuditInvalid
	}
	options.Phase = phase
	if options.ExpectedTables == nil {
		options.ExpectedTables = append([]string(nil), sqlite.RequiredTables...)
	}
	if options.ExpectedIndexes == nil {
		options.ExpectedIndexes = append([]string(nil), sqlite.RequiredIndexes...)
	}
	var err error
	if options.ExpectedTables, err = normalizedIdentifiers(options.ExpectedTables); err != nil {
		return nil, err
	}
	if options.ExpectedIndexes, err = normalizedIdentifiers(options.ExpectedIndexes); err != nil {
		return nil, err
	}
	if options.MaximumRecordIDs <= 0 || options.MaximumRecordIDs > 1000 {
		options.MaximumRecordIDs = 50
	}
	if options.Paths == nil {
		options.Paths = OSPathInspector{}
	}
	return &Auditor{database: database, options: options}, nil
}

func (auditor *Auditor) Audit(ctx context.Context) (Report, error) {
	report := Report{
		FormatVersion: ReportFormatVersion, AuditVersion: AuditVersion,
		Phase: auditor.options.Phase, MigrationVersion: auditor.options.MigrationVersion,
		Integrity: []IntegrityResult{}, Tables: []TableMetric{}, Relations: []RelationMetric{},
		AuthorizedRoots: []string{}, Paths: []PathMetric{}, Aggregates: []AggregateMetric{}, Findings: []Finding{},
	}
	if err := ctx.Err(); err != nil {
		return report, err
	}
	incomplete := false
	if err := auditor.auditIntegrity(ctx, &report); err != nil {
		incomplete = true
		report.Findings = append(report.Findings, blocking("integrity", "integrity_check_unreadable", "", "", 1, nil))
	}
	availableTables, err := auditor.auditSchema(ctx, &report)
	if err != nil {
		incomplete = true
		report.Findings = append(report.Findings, blocking("schema", "schema_inventory_unreadable", "", "", 1, nil))
	}
	if err := auditRelations(ctx, auditor.database, availableTables, auditor.options.MaximumRecordIDs, &report); err != nil {
		incomplete = true
		report.Findings = append(report.Findings, blocking("relation", "relation_audit_incomplete", "", "", 1, nil))
	}
	if err := auditPaths(ctx, auditor.database, availableTables, auditor.options, &report); err != nil {
		incomplete = true
		report.Findings = append(report.Findings, blocking("path", "path_audit_incomplete", "", "", 1, nil))
	}
	if err := auditAggregates(ctx, auditor.database, availableTables, auditor.options.MaximumRecordIDs, &report); err != nil {
		incomplete = true
		report.Findings = append(report.Findings, blocking("aggregate", "aggregate_audit_incomplete", "", "", 1, nil))
	}
	report.Normalize()
	if incomplete {
		return report, ErrAuditIncomplete
	}
	return report, nil
}

func (auditor *Auditor) auditIntegrity(ctx context.Context, report *Report) error {
	rows, err := auditor.database.QueryContext(ctx, "PRAGMA integrity_check")
	if err != nil {
		return err
	}
	count := int64(0)
	ok := true
	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err != nil {
			_ = rows.Close()
			return err
		}
		count++
		if result != "ok" {
			ok = false
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if count != 1 {
		ok = false
	}
	report.Integrity = append(report.Integrity, IntegrityResult{Check: "integrity_check", OK: ok, Count: count, Code: boolCode(ok, "ok", "integrity_failed")})
	if !ok {
		report.Findings = append(report.Findings, blocking("integrity", "integrity_failed", "", "", count, nil))
	}

	foreignRows, err := auditor.database.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	foreignCount := int64(0)
	type foreignViolationGroup struct {
		count       int64
		identifiers []string
	}
	foreignViolations := make(map[string]*foreignViolationGroup)
	for foreignRows.Next() {
		var table, parent string
		var rowID sql.NullInt64
		var foreignKeyID int64
		if err := foreignRows.Scan(&table, &rowID, &parent, &foreignKeyID); err != nil {
			_ = foreignRows.Close()
			return err
		}
		foreignCount++
		group := foreignViolations[table]
		if group == nil {
			group = &foreignViolationGroup{}
			foreignViolations[table] = group
		}
		group.count++
		if len(group.identifiers) < auditor.options.MaximumRecordIDs {
			rowKey := "without-rowid"
			if rowID.Valid {
				rowKey = fmt.Sprint(rowID.Int64)
			}
			group.identifiers = append(group.identifiers, recordIdentifier(table,
				rowKey+"\x00"+parent+"\x00"+fmt.Sprint(foreignKeyID)))
		}
	}
	if err := foreignRows.Err(); err != nil {
		_ = foreignRows.Close()
		return err
	}
	if err := foreignRows.Close(); err != nil {
		return err
	}
	foreignOK := foreignCount == 0
	report.Integrity = append(report.Integrity, IntegrityResult{Check: "foreign_key_check", OK: foreignOK, Count: foreignCount, Code: boolCode(foreignOK, "ok", "foreign_key_violation")})
	if !foreignOK {
		tables := make([]string, 0, len(foreignViolations))
		for table := range foreignViolations {
			tables = append(tables, table)
		}
		sort.Strings(tables)
		for _, table := range tables {
			group := foreignViolations[table]
			report.Findings = append(report.Findings, blocking("integrity", "foreign_key_violation",
				schemaObjectLabel(table), "", group.count, group.identifiers))
		}
	}
	return nil
}

func (auditor *Auditor) auditSchema(ctx context.Context, report *Report) (map[string]struct{}, error) {
	rows, err := auditor.database.QueryContext(ctx,
		"SELECT type, name, tbl_name, COALESCE(sql, '') FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%' ORDER BY type, name")
	if err != nil {
		return nil, err
	}
	tableSQL := make(map[string]string)
	tableIndexes := make(map[string][]string)
	actualIndexes := make([]string, 0)
	for rows.Next() {
		var kind, name, table, statement string
		if err := rows.Scan(&kind, &name, &table, &statement); err != nil {
			_ = rows.Close()
			return nil, err
		}
		switch kind {
		case "table":
			tableSQL[name] = statement
		case "index":
			actualIndexes = append(actualIndexes, name)
			tableIndexes[table] = append(tableIndexes[table], normalizeSQL(statement))
		case "trigger", "view":
			report.Findings = append(report.Findings, blocking("schema", "unexpected_schema_object",
				schemaObjectLabel(table), schemaObjectLabel(name), 1, nil))
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	expectedTables := stringSet(auditor.options.ExpectedTables)
	for _, expected := range auditor.options.ExpectedTables {
		if _, ok := tableSQL[expected]; !ok {
			report.Findings = append(report.Findings, blocking("schema", "missing_table", expected, "", 1, nil))
		}
	}
	for table := range tableSQL {
		if _, ok := expectedTables[table]; !ok {
			report.Findings = append(report.Findings, blocking("schema", "unexpected_table", schemaObjectLabel(table), "", 1, nil))
		}
	}
	expectedIndexes := stringSet(auditor.options.ExpectedIndexes)
	actualIndexSet := stringSet(actualIndexes)
	for _, expected := range auditor.options.ExpectedIndexes {
		if _, ok := actualIndexSet[expected]; !ok {
			report.Findings = append(report.Findings, blocking("schema", "missing_index", "", expected, 1, nil))
		}
	}
	for _, index := range actualIndexes {
		if _, ok := expectedIndexes[index]; !ok {
			report.Findings = append(report.Findings, blocking("schema", "unexpected_index", "", schemaObjectLabel(index), 1, nil))
		}
	}
	tables := make([]string, 0, len(tableSQL))
	for table := range tableSQL {
		tables = append(tables, table)
	}
	sort.Strings(tables)
	for _, table := range tables {
		if !safeIdentifier(table) {
			report.Findings = append(report.Findings, blocking("schema", "unsafe_table_identifier",
				schemaObjectLabel(table), "", 1, nil))
			continue
		}
		indexes := append([]string(nil), tableIndexes[table]...)
		sort.Strings(indexes)
		schemaParts := append([]string{normalizeSQL(tableSQL[table])}, indexes...)
		metric, err := auditor.tableMetric(ctx, table, strings.Join(schemaParts, "\n"))
		if err != nil {
			return stringSet(tables), err
		}
		report.Tables = append(report.Tables, metric)
	}
	return stringSet(tables), nil
}

func (auditor *Auditor) tableMetric(ctx context.Context, table, schemaSQL string) (TableMetric, error) {
	metric := TableMetric{Table: table, SchemaSHA256: sha256Text(normalizeSQL(schemaSQL))}
	if err := auditor.database.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM \"%s\"", table)).Scan(&metric.RowCount); err != nil {
		return TableMetric{}, err
	}
	rows, err := auditor.database.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(\"%s\")", table))
	if err != nil {
		return TableMetric{}, err
	}
	type primaryColumn struct {
		position int
		name     string
	}
	primary := make([]primaryColumn, 0, 3)
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return TableMetric{}, err
		}
		if primaryKey > 0 {
			primary = append(primary, primaryColumn{position: primaryKey, name: name})
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return TableMetric{}, err
	}
	if err := rows.Close(); err != nil {
		return TableMetric{}, err
	}
	sort.Slice(primary, func(i, j int) bool { return primary[i].position < primary[j].position })
	names := make([]string, 0, len(primary))
	for _, column := range primary {
		names = append(names, column.name)
	}
	metric.PrimaryKey = strings.Join(names, ",")
	if len(primary) > 0 && metric.RowCount > 0 {
		parts := make([]string, 0, len(primary))
		ascending := make([]string, 0, len(primary))
		descending := make([]string, 0, len(primary))
		for _, column := range primary {
			if !safeIdentifier(column.name) {
				return TableMetric{}, ErrAuditInvalid
			}
			parts = append(parts, fmt.Sprintf(
				"printf('%%d:%%s', length(CAST(\"%s\" AS TEXT)), CAST(\"%s\" AS TEXT))",
				column.name, column.name,
			))
			ascending = append(ascending, fmt.Sprintf("\"%s\" ASC", column.name))
			descending = append(descending, fmt.Sprintf("\"%s\" DESC", column.name))
		}
		expression := strings.Join(parts, " || ")
		var minimum, maximum string
		minimumQuery := fmt.Sprintf("SELECT %s FROM \"%s\" ORDER BY %s LIMIT 1", expression, table, strings.Join(ascending, ", "))
		maximumQuery := fmt.Sprintf("SELECT %s FROM \"%s\" ORDER BY %s LIMIT 1", expression, table, strings.Join(descending, ", "))
		if err := auditor.database.QueryRowContext(ctx, minimumQuery).Scan(&minimum); err != nil {
			return TableMetric{}, err
		}
		if err := auditor.database.QueryRowContext(ctx, maximumQuery).Scan(&maximum); err != nil {
			return TableMetric{}, err
		}
		metric.PrimaryMinRecordID = recordIdentifier(table, minimum)
		metric.PrimaryMaxRecordID = recordIdentifier(table, maximum)
	}
	return metric, nil
}

func blocking(category, code, table, column string, count int64, ids []string) Finding {
	return Finding{Classification: ClassificationBlocking, Category: category, Code: code,
		Table: table, Column: column, Count: count, RecordIDs: append([]string(nil), ids...)}
}

func recordIdentifier(namespace, key string) string {
	digest := sha256.Sum256([]byte(namespace + "\x00" + key))
	return "rid-" + hex.EncodeToString(digest[:8])
}

func sha256Text(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

type recordSetHasher struct{ state hash.Hash }

func newRecordSetHasher() *recordSetHasher { return &recordSetHasher{state: sha256.New()} }

func (hasher *recordSetHasher) Add(identifier string) {
	_, _ = hasher.state.Write([]byte(identifier))
	_, _ = hasher.state.Write([]byte{0})
}

func (hasher *recordSetHasher) Sum() string { return hex.EncodeToString(hasher.state.Sum(nil)) }

func normalizeSQL(value string) string { return strings.Join(strings.Fields(value), " ") }

func boolCode(value bool, yes, no string) string {
	if value {
		return yes
	}
	return no
}

func stringSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func normalizedIdentifiers(values []string) ([]string, error) {
	result := append([]string(nil), values...)
	sort.Strings(result)
	for index, value := range result {
		if !safeIdentifier(value) || (index > 0 && result[index-1] == value) {
			return nil, ErrAuditInvalid
		}
	}
	return result, nil
}

func schemaObjectLabel(value string) string {
	if safeIdentifier(value) {
		return value
	}
	return "object-" + sha256Text(value)[:16]
}

func queryTablesAvailable(query string, available map[string]struct{}) bool {
	if available == nil {
		return false
	}
	normalized := strings.NewReplacer(
		"(", " ", ")", " ", ",", " ", "\n", " ", "\r", " ", "\t", " ",
	).Replace(query)
	tokens := stringSet(strings.Fields(normalized))
	for _, table := range sqlite.RequiredTables {
		if _, referenced := tokens[table]; referenced {
			if _, exists := available[table]; !exists {
				return false
			}
		}
	}
	return true
}

func safeIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for index, char := range value {
		if !((char >= 'a' && char <= 'z') || char == '_' || (index > 0 && char >= '0' && char <= '9')) {
			return false
		}
	}
	return true
}
