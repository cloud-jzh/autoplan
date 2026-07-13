// Package sqlite implements a dependency-free, immutable SQLite reader for
// the three P03 tables. It parses a closed database snapshot in memory and has
// no SQL execution, journal, schema mutation, attachment, or extension capability.
// This is the structural equivalent of mode=ro, immutable=1, and query_only:
// there is no database engine or writable handle through which SQL can run.
package sqlite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/lyming99/autoplan/backend/internal/repository"
)

type TargetKind string

const (
	TargetFixture      TargetKind = "fixture"
	TargetDatabaseCopy TargetKind = "database-copy"
	maximumDatabaseBytes          = int64(512 << 20)
)

type Options struct {
	Path              string
	AllowedRoot       string
	Kind              TargetKind
	DeclaredSanitized bool
}

type Reader struct {
	mu        sync.RWMutex
	path      string
	info      os.FileInfo
	size      int64
	modified  int64
	digest    [sha256.Size]byte
	closed    bool
	projects  []repository.Project
	settings  []repository.Setting
	states    map[int64]repository.ProjectState
}

type database struct {
	data       []byte
	pageSize   int
	usableSize int
	pageCount  int
	tables     map[string]table
}

type table struct {
	name     string
	rootPage uint32
	columns  []column
}

type column struct {
	name       string
	affinity   string
	notNull    bool
	primaryKey bool
	hasDefault bool
	defaultVal value
}

type valueKind uint8

const (
	kindNull valueKind = iota
	kindInteger
	kindFloat
	kindText
	kindBlob
)

type value struct {
	kind    valueKind
	integer int64
	float   float64
	text    string
	blob    []byte
}

type tableRow struct {
	rowID  int64
	values []value
}

type schemaEntry struct {
	kind     string
	name     string
	table    string
	rootPage int64
	sql      string
}

func Open(ctx context.Context, options Options) (*Reader, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	target, err := authorizePath(options)
	if err != nil {
		return nil, err
	}
	if activeSidecar(target) {
		return nil, repository.ErrUnsafePath
	}
	file, err := os.Open(target)
	if err != nil {
		return nil, repository.ErrInvalidStore
	}
	info, statErr := file.Stat()
	if statErr != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maximumDatabaseBytes {
		_ = file.Close()
		return nil, repository.ErrInvalidStore
	}
	content, readErr := io.ReadAll(io.LimitReader(file, maximumDatabaseBytes+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || int64(len(content)) != info.Size() {
		return nil, repository.ErrInvalidStore
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	after, err := os.Stat(target)
	if err != nil || !os.SameFile(info, after) || after.Size() != info.Size() || after.ModTime() != info.ModTime() || activeSidecar(target) {
		return nil, repository.ErrSourceChanged
	}
	parsed, err := parseDatabase(ctx, content)
	if err != nil {
		return nil, err
	}
	result := &Reader{
		path: target, info: info, size: info.Size(), modified: info.ModTime().UnixNano(),
		digest: sha256.Sum256(content), states: make(map[int64]repository.ProjectState),
	}
	if err := result.load(ctx, parsed); err != nil {
		return nil, err
	}
	return result, nil
}

func (reader *Reader) Check(ctx context.Context) error {
	reader.mu.RLock()
	defer reader.mu.RUnlock()
	if reader.closed {
		return repository.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := os.Stat(reader.path)
	if err != nil || !os.SameFile(reader.info, info) || info.Size() != reader.size ||
		info.ModTime().UnixNano() != reader.modified || activeSidecar(reader.path) {
		return repository.ErrSourceChanged
	}
	file, err := os.Open(reader.path)
	if err != nil {
		return repository.ErrSourceChanged
	}
	content, readErr := io.ReadAll(io.LimitReader(file, maximumDatabaseBytes+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || sha256.Sum256(content) != reader.digest {
		return repository.ErrSourceChanged
	}
	return ctx.Err()
}

func (reader *Reader) Close() error {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	if reader.closed {
		return nil
	}
	reader.closed = true
	reader.projects = nil
	reader.settings = nil
	reader.states = nil
	reader.path = ""
	reader.info = nil
	return nil
}

func authorizePath(options Options) (string, error) {
	if options.Path == "" || options.AllowedRoot == "" || !filepath.IsAbs(options.Path) ||
		!filepath.IsAbs(options.AllowedRoot) || strings.ContainsAny(options.Path, "\x00?#") ||
		strings.HasPrefix(strings.ToLower(options.Path), "file:") {
		return "", repository.ErrUnsafePath
	}
	target := filepath.Clean(options.Path)
	root := filepath.Clean(options.AllowedRoot)
	if filepath.Clean(target) == filepath.Clean(root) || !within(target, root) || unsafeUserDataPath(target) ||
		strings.EqualFold(filepath.Base(target), "autoplan.sqlite") {
		return "", repository.ErrUnsafePath
	}
	rootInfo, err := os.Lstat(root)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return "", repository.ErrUnsafePath
	}
	targetInfo, err := os.Lstat(target)
	if err != nil || !targetInfo.Mode().IsRegular() || targetInfo.Mode()&os.ModeSymlink != 0 {
		return "", repository.ErrUnsafePath
	}
	if !pathComponentsAreRegular(root, target) {
		return "", repository.ErrUnsafePath
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", repository.ErrUnsafePath
	}
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil || !within(resolvedTarget, resolvedRoot) {
		return "", repository.ErrUnsafePath
	}
	switch options.Kind {
	case TargetFixture:
		if !strings.HasSuffix(strings.ToLower(filepath.Base(target)), ".sqlite") {
			return "", repository.ErrUnsafePath
		}
	case TargetDatabaseCopy:
		base := strings.ToLower(filepath.Base(target))
		if !options.DeclaredSanitized || !(strings.HasSuffix(base, ".copy") ||
			strings.HasSuffix(base, ".backup") || strings.HasSuffix(base, ".bak")) {
			return "", repository.ErrUnsafePath
		}
	default:
		return "", repository.ErrUnsafePath
	}
	return resolvedTarget, nil
}

func within(target, root string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(target))
	return err == nil && relative != "." && relative != ".." && !filepath.IsAbs(relative) &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func unsafeUserDataPath(target string) bool {
	normalized := strings.ToLower(filepath.ToSlash(target))
	markers := []string{
		"/appdata/roaming/autoplan/", "/appdata/local/autoplan/",
		"/library/application support/autoplan/", "/.config/autoplan/",
	}
	for _, marker := range markers {
		if strings.Contains(normalized+"/", marker) {
			return true
		}
	}
	return false
}

func pathComponentsAreRegular(root, target string) bool {
	current := filepath.Clean(target)
	for {
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return false
		}
		if current == filepath.Clean(root) {
			return true
		}
		parent := filepath.Dir(current)
		if parent == current || !withinOrEqual(parent, root) {
			return false
		}
		current = parent
	}
}

func withinOrEqual(target, root string) bool {
	return filepath.Clean(target) == filepath.Clean(root) || within(target, root)
}

func activeSidecar(target string) bool {
	for _, suffix := range []string{"-wal", "-shm", "-journal", ".lock", ".autoplan-server.lock"} {
		if _, err := os.Lstat(target + suffix); err == nil || !os.IsNotExist(err) {
			return true
		}
	}
	return false
}

func parseDatabase(ctx context.Context, content []byte) (*database, error) {
	if len(content) < 100 || !bytes.Equal(content[:16], []byte("SQLite format 3\x00")) {
		return nil, repository.ErrInvalidStore
	}
	pageSize := int(binary.BigEndian.Uint16(content[16:18]))
	if pageSize == 1 {
		pageSize = 65536
	}
	if pageSize < 512 || pageSize > 65536 || pageSize&(pageSize-1) != 0 || len(content)%pageSize != 0 {
		return nil, repository.ErrInvalidStore
	}
	headerPageCount := int(binary.BigEndian.Uint32(content[28:32]))
	actualPageCount := len(content) / pageSize
	if content[18] != 1 || content[19] != 1 || binary.BigEndian.Uint32(content[44:48]) != 4 ||
		binary.BigEndian.Uint32(content[56:60]) != 1 || (headerPageCount != 0 && headerPageCount != actualPageCount) {
		return nil, repository.ErrInvalidStore
	}
	reserved := int(content[20])
	if reserved >= pageSize-480 {
		return nil, repository.ErrInvalidStore
	}
	result := &database{
		data: content, pageSize: pageSize, usableSize: pageSize - reserved,
		pageCount: actualPageCount, tables: make(map[string]table),
	}
	rows, err := result.readTableRows(ctx, 1)
	if err != nil {
		return nil, err
	}
	entries, err := decodeSchema(rows)
	if err != nil {
		return nil, err
	}
	rootPages := make(map[int64]string)
	for _, entry := range entries {
		if entry.kind == "trigger" && isRequiredTable(entry.table) {
			return nil, repository.ErrSchemaDrift
		}
		if entry.kind != "table" || !isRequiredTable(entry.name) {
			continue
		}
		if entry.rootPage <= 1 || entry.rootPage > int64(result.pageCount) {
			return nil, repository.ErrSchemaDrift
		}
		columns, err := validateTableSchema(entry.name, entry.sql)
		if err != nil {
			return nil, err
		}
		if _, duplicate := result.tables[entry.name]; duplicate {
			return nil, repository.ErrSchemaDrift
		}
		if _, duplicate := rootPages[entry.rootPage]; duplicate {
			return nil, repository.ErrSchemaDrift
		}
		rootPages[entry.rootPage] = entry.name
		result.tables[entry.name] = table{name: entry.name, rootPage: uint32(entry.rootPage), columns: columns}
	}
	for _, name := range []string{"projects", "settings", "project_states"} {
		if _, exists := result.tables[name]; !exists {
			return nil, repository.ErrSchemaDrift
		}
	}
	return result, nil
}

func (db *database) readRows(ctx context.Context, name string) ([]tableRow, error) {
	tableInfo, exists := db.tables[name]
	if !exists {
		return nil, repository.ErrSchemaDrift
	}
	rows, err := db.readTableRows(ctx, tableInfo.rootPage)
	if err != nil {
		return nil, err
	}
	for index := range rows {
		if len(rows[index].values) > len(tableInfo.columns) {
			return nil, repository.ErrSchemaDrift
		}
		for len(rows[index].values) < len(tableInfo.columns) {
			columnInfo := tableInfo.columns[len(rows[index].values)]
			if !columnInfo.hasDefault && columnInfo.notNull {
				return nil, repository.ErrSchemaDrift
			}
			rows[index].values = append(rows[index].values, cloneValue(columnInfo.defaultVal))
		}
		for columnIndex, columnInfo := range tableInfo.columns {
			if columnInfo.primaryKey && columnInfo.affinity == "INTEGER" && rows[index].values[columnIndex].kind == kindNull {
				rows[index].values[columnIndex] = value{kind: kindInteger, integer: rows[index].rowID}
			}
			if err := validateStoredValue(rows[index].values[columnIndex], columnInfo); err != nil {
				return nil, err
			}
		}
		canonical := expectedSchema()[name]
		reordered := make([]value, len(canonical))
		for canonicalIndex, canonicalColumn := range canonical {
			physicalIndex := columnIndex(tableInfo.columns, canonicalColumn.name)
			if physicalIndex < 0 {
				return nil, repository.ErrSchemaDrift
			}
			reordered[canonicalIndex] = rows[index].values[physicalIndex]
		}
		rows[index].values = reordered
	}
	return rows, nil
}

func columnIndex(columns []column, name string) int {
	for index, item := range columns {
		if item.name == name {
			return index
		}
	}
	return -1
}

func validateStoredValue(item value, columnInfo column) error {
	if item.kind == kindNull {
		if columnInfo.notNull || columnInfo.primaryKey {
			return repository.ErrSchemaDrift
		}
		return nil
	}
	switch columnInfo.affinity {
	case "INTEGER":
		if item.kind != kindInteger {
			return repository.ErrSchemaDrift
		}
	case "TEXT":
		if item.kind != kindText {
			return repository.ErrSchemaDrift
		}
	default:
		return repository.ErrSchemaDrift
	}
	return nil
}

func cloneValue(item value) value {
	copyValue := item
	copyValue.blob = append([]byte(nil), item.blob...)
	return copyValue
}

func (db *database) readTableRows(ctx context.Context, rootPage uint32) ([]tableRow, error) {
	rows := make([]tableRow, 0)
	visited := make(map[uint32]struct{})
	var walk func(uint32) error
	walk = func(pageNumber uint32) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if pageNumber == 0 || int(pageNumber) > db.pageCount || len(visited) >= db.pageCount {
			return repository.ErrInvalidStore
		}
		if _, duplicate := visited[pageNumber]; duplicate {
			return repository.ErrInvalidStore
		}
		visited[pageNumber] = struct{}{}
		page, header, err := db.page(pageNumber)
		if err != nil {
			return err
		}
		pageType := page[header]
		cellCount := int(binary.BigEndian.Uint16(page[header+3 : header+5]))
		if cellCount < 0 || cellCount > db.usableSize/2 {
			return repository.ErrInvalidStore
		}
		switch pageType {
		case 0x05:
			if header+12+cellCount*2 > len(page) {
				return repository.ErrInvalidStore
			}
			for index := 0; index < cellCount; index++ {
				pointer := int(binary.BigEndian.Uint16(page[header+12+index*2 : header+14+index*2]))
				if pointer < 0 || pointer+4 > db.usableSize {
					return repository.ErrInvalidStore
				}
				if err := walk(binary.BigEndian.Uint32(page[pointer : pointer+4])); err != nil {
					return err
				}
			}
			return walk(binary.BigEndian.Uint32(page[header+8 : header+12]))
		case 0x0d:
			if header+8+cellCount*2 > len(page) {
				return repository.ErrInvalidStore
			}
			for index := 0; index < cellCount; index++ {
				pointer := int(binary.BigEndian.Uint16(page[header+8+index*2 : header+10+index*2]))
				row, err := db.readLeafCell(pageNumber, pointer)
				if err != nil {
					return err
				}
				rows = append(rows, row)
			}
			return nil
		default:
			return repository.ErrInvalidStore
		}
	}
	if err := walk(rootPage); err != nil {
		return nil, err
	}
	sort.SliceStable(rows, func(left, right int) bool { return rows[left].rowID < rows[right].rowID })
	return rows, nil
}

func (db *database) page(number uint32) ([]byte, int, error) {
	if number == 0 || int(number) > db.pageCount {
		return nil, 0, repository.ErrInvalidStore
	}
	start := (int(number) - 1) * db.pageSize
	page := db.data[start : start+db.pageSize]
	header := 0
	if number == 1 {
		header = 100
	}
	if header+12 > db.usableSize {
		return nil, 0, repository.ErrInvalidStore
	}
	return page, header, nil
}

func (db *database) readLeafCell(pageNumber uint32, offset int) (tableRow, error) {
	page, _, err := db.page(pageNumber)
	if err != nil || offset < 0 || offset >= db.usableSize {
		return tableRow{}, repository.ErrInvalidStore
	}
	payloadSize, payloadBytes, err := readVarint(page[offset:db.usableSize])
	if err != nil || payloadSize > uint64(maximumDatabaseBytes) {
		return tableRow{}, repository.ErrInvalidStore
	}
	rowIDBits, rowIDBytes, err := readVarint(page[offset+payloadBytes : db.usableSize])
	if err != nil {
		return tableRow{}, repository.ErrInvalidStore
	}
	cellStart := offset + payloadBytes + rowIDBytes
	local := db.localPayloadSize(int(payloadSize))
	if cellStart+local > db.usableSize {
		return tableRow{}, repository.ErrInvalidStore
	}
	payload := append([]byte(nil), page[cellStart:cellStart+local]...)
	if local < int(payloadSize) {
		if cellStart+local+4 > db.usableSize {
			return tableRow{}, repository.ErrInvalidStore
		}
		next := binary.BigEndian.Uint32(page[cellStart+local : cellStart+local+4])
		remaining := int(payloadSize) - local
		seen := make(map[uint32]struct{})
		for remaining > 0 {
			if next == 0 || int(next) > db.pageCount {
				return tableRow{}, repository.ErrInvalidStore
			}
			if _, duplicate := seen[next]; duplicate {
				return tableRow{}, repository.ErrInvalidStore
			}
			seen[next] = struct{}{}
			overflow, _, err := db.page(next)
			if err != nil || db.usableSize < 4 {
				return tableRow{}, repository.ErrInvalidStore
			}
			next = binary.BigEndian.Uint32(overflow[:4])
			take := db.usableSize - 4
			if take > remaining {
				take = remaining
			}
			payload = append(payload, overflow[4:4+take]...)
			remaining -= take
		}
	}
	values, err := decodeRecord(payload)
	if err != nil {
		return tableRow{}, err
	}
	return tableRow{rowID: int64(rowIDBits), values: values}, nil
}

func (db *database) localPayloadSize(payload int) int {
	maxLocal := db.usableSize - 35
	if payload <= maxLocal {
		return payload
	}
	minLocal := ((db.usableSize - 12) * 32 / 255) - 23
	local := minLocal + (payload-minLocal)%(db.usableSize-4)
	if local > maxLocal {
		return minLocal
	}
	return local
}

func readVarint(data []byte) (uint64, int, error) {
	var result uint64
	for index := 0; index < 9; index++ {
		if index >= len(data) {
			return 0, 0, repository.ErrInvalidStore
		}
		current := data[index]
		if index == 8 {
			return result<<8 | uint64(current), 9, nil
		}
		result = result<<7 | uint64(current&0x7f)
		if current&0x80 == 0 {
			return result, index + 1, nil
		}
	}
	return 0, 0, repository.ErrInvalidStore
}

func decodeRecord(payload []byte) ([]value, error) {
	headerSize, headerBytes, err := readVarint(payload)
	if err != nil || headerSize < uint64(headerBytes) || headerSize > uint64(len(payload)) {
		return nil, repository.ErrInvalidStore
	}
	serials := make([]uint64, 0)
	position := headerBytes
	for position < int(headerSize) {
		serial, consumed, err := readVarint(payload[position:int(headerSize)])
		if err != nil {
			return nil, err
		}
		serials = append(serials, serial)
		position += consumed
	}
	if position != int(headerSize) {
		return nil, repository.ErrInvalidStore
	}
	position = int(headerSize)
	values := make([]value, 0, len(serials))
	for _, serial := range serials {
		item, consumed, err := decodeSerial(serial, payload[position:])
		if err != nil {
			return nil, err
		}
		values = append(values, item)
		position += consumed
	}
	if position != len(payload) {
		return nil, repository.ErrInvalidStore
	}
	return values, nil
}

func decodeSerial(serial uint64, data []byte) (value, int, error) {
	switch serial {
	case 0:
		return value{kind: kindNull}, 0, nil
	case 1, 2, 3, 4, 5, 6:
		lengths := [...]int{0, 1, 2, 3, 4, 6, 8}
		length := lengths[serial]
		if len(data) < length {
			return value{}, 0, repository.ErrInvalidStore
		}
		var integer int64
		for _, octet := range data[:length] {
			integer = integer<<8 | int64(octet)
		}
		if length < 8 && data[0]&0x80 != 0 {
			integer |= ^int64(0) << uint(length*8)
		}
		return value{kind: kindInteger, integer: integer}, length, nil
	case 7:
		if len(data) < 8 {
			return value{}, 0, repository.ErrInvalidStore
		}
		return value{kind: kindFloat, float: math.Float64frombits(binary.BigEndian.Uint64(data[:8]))}, 8, nil
	case 8:
		return value{kind: kindInteger, integer: 0}, 0, nil
	case 9:
		return value{kind: kindInteger, integer: 1}, 0, nil
	case 10, 11:
		return value{}, 0, repository.ErrInvalidStore
	default:
		length := int((serial - 12) / 2)
		if serial > uint64(maximumDatabaseBytes*2+13) || length < 0 || len(data) < length {
			return value{}, 0, repository.ErrInvalidStore
		}
		if serial%2 == 0 {
			return value{kind: kindBlob, blob: append([]byte(nil), data[:length]...)}, length, nil
		}
		if !validUTF8(data[:length]) {
			return value{}, 0, repository.ErrInvalidStore
		}
		return value{kind: kindText, text: string(data[:length])}, length, nil
	}
}

func validUTF8(data []byte) bool {
	return utf8.Valid(data)
}

func decodeSchema(rows []tableRow) ([]schemaEntry, error) {
	entries := make([]schemaEntry, 0, len(rows))
	for _, row := range rows {
		if len(row.values) != 5 || row.values[0].kind != kindText || row.values[1].kind != kindText ||
			row.values[2].kind != kindText || row.values[3].kind != kindInteger ||
			(row.values[4].kind != kindText && row.values[4].kind != kindNull) {
			return nil, repository.ErrInvalidStore
		}
		entries = append(entries, schemaEntry{
			kind: row.values[0].text, name: row.values[1].text, table: row.values[2].text,
			rootPage: row.values[3].integer, sql: row.values[4].text,
		})
	}
	return entries, nil
}

func isRequiredTable(name string) bool {
	return name == "projects" || name == "settings" || name == "project_states"
}

func validateTableSchema(name, sql string) ([]column, error) {
	expected := expectedSchema()[name]
	actual, suffix, err := parseCreateTable(sql)
	if err != nil || len(actual) != len(expected) || strings.Trim(strings.TrimSpace(suffix), ";") != "" {
		return nil, repository.ErrSchemaDrift
	}
	seen := make(map[string]struct{}, len(actual))
	for _, left := range actual {
		if _, duplicate := seen[left.name]; duplicate {
			return nil, repository.ErrSchemaDrift
		}
		seen[left.name] = struct{}{}
		index := columnIndex(expected, left.name)
		if index < 0 {
			return nil, repository.ErrSchemaDrift
		}
		right := expected[index]
		if left.name != right.name || left.affinity != right.affinity || left.notNull != right.notNull ||
			left.primaryKey != right.primaryKey || left.hasDefault != right.hasDefault ||
			(left.hasDefault && !equalValue(left.defaultVal, right.defaultVal)) {
			return nil, repository.ErrSchemaDrift
		}
	}
	return actual, nil
}

func parseCreateTable(sql string) ([]column, string, error) {
	trimmed := strings.TrimSpace(sql)
	if !strings.HasPrefix(strings.ToUpper(trimmed), "CREATE"+" TABLE ") {
		return nil, "", repository.ErrSchemaDrift
	}
	open := strings.Index(trimmed, "(")
	if open < 0 {
		return nil, "", repository.ErrSchemaDrift
	}
	closeIndex := matchingParenthesis(trimmed, open)
	if closeIndex < 0 {
		return nil, "", repository.ErrSchemaDrift
	}
	parts, err := splitDefinitions(trimmed[open+1 : closeIndex])
	if err != nil {
		return nil, "", err
	}
	columns := make([]column, 0, len(parts))
	for _, part := range parts {
		upper := strings.ToUpper(strings.TrimSpace(part))
		if strings.HasPrefix(upper, "CONSTRAINT ") || strings.HasPrefix(upper, "PRIMARY KEY") ||
			strings.HasPrefix(upper, "UNIQUE ") || strings.HasPrefix(upper, "CHECK ") ||
			strings.HasPrefix(upper, "FOREIGN KEY") {
			return nil, "", repository.ErrSchemaDrift
		}
		item, err := parseColumn(part)
		if err != nil {
			return nil, "", err
		}
		columns = append(columns, item)
	}
	return columns, trimmed[closeIndex+1:], nil
}

func parseColumn(definition string) (column, error) {
	fields := strings.Fields(strings.TrimSpace(definition))
	if len(fields) < 2 {
		return column{}, repository.ErrSchemaDrift
	}
	name := unquoteIdentifier(fields[0])
	affinity := strings.ToUpper(fields[1])
	if name == "" || (affinity != "INTEGER" && affinity != "TEXT") {
		return column{}, repository.ErrSchemaDrift
	}
	upper := strings.ToUpper(strings.Join(fields[2:], " "))
	result := column{
		name: name, affinity: affinity,
		notNull: strings.Contains(" "+upper+" ", " NOT NULL "),
		primaryKey: strings.Contains(" "+upper+" ", " PRIMARY KEY "),
	}
	if index := keywordIndex(fields, "DEFAULT"); index >= 0 {
		if index+1 >= len(fields) {
			return column{}, repository.ErrSchemaDrift
		}
		defaultValue, err := parseDefault(fields[index+1], affinity)
		if err != nil {
			return column{}, err
		}
		result.hasDefault = true
		result.defaultVal = defaultValue
	}
	return result, nil
}

func keywordIndex(fields []string, keyword string) int {
	for index, field := range fields {
		if strings.EqualFold(field, keyword) {
			return index
		}
	}
	return -1
}

func parseDefault(raw, affinity string) (value, error) {
	trimmed := strings.Trim(strings.TrimSpace(raw), "()")
	if strings.EqualFold(trimmed, "NULL") {
		return value{kind: kindNull}, nil
	}
	if affinity == "TEXT" && len(trimmed) >= 2 && trimmed[0] == '\'' && trimmed[len(trimmed)-1] == '\'' {
		return value{kind: kindText, text: strings.ReplaceAll(trimmed[1:len(trimmed)-1], "''", "'")}, nil
	}
	if affinity == "INTEGER" {
		negative := false
		if strings.HasPrefix(trimmed, "-") {
			negative = true
			trimmed = trimmed[1:]
		}
		if trimmed == "" {
			return value{}, repository.ErrSchemaDrift
		}
		var number int64
		for _, character := range trimmed {
			if character < '0' || character > '9' || number > (int64(^uint64(0)>>1)-int64(character-'0'))/10 {
				return value{}, repository.ErrSchemaDrift
			}
			number = number*10 + int64(character-'0')
		}
		if negative {
			number = -number
		}
		return value{kind: kindInteger, integer: number}, nil
	}
	return value{}, repository.ErrSchemaDrift
}

func matchingParenthesis(value string, open int) int {
	depth := 0
	quote := rune(0)
	for index, character := range value[open:] {
		absolute := open + index
		if quote != 0 {
			if character == quote {
				quote = 0
			}
			continue
		}
		if character == '\'' || character == '"' || character == '`' {
			quote = character
			continue
		}
		if character == '(' {
			depth++
		} else if character == ')' {
			depth--
			if depth == 0 {
				return absolute
			}
		}
	}
	return -1
}

func splitDefinitions(value string) ([]string, error) {
	parts := make([]string, 0)
	start, depth := 0, 0
	quote := rune(0)
	for index, character := range value {
		if quote != 0 {
			if character == quote {
				quote = 0
			}
			continue
		}
		if character == '\'' || character == '"' || character == '`' {
			quote = character
			continue
		}
		switch character {
		case '(':
			depth++
		case ')':
			depth--
			if depth < 0 {
				return nil, repository.ErrSchemaDrift
			}
		case ',':
			if depth == 0 {
				parts = append(parts, value[start:index])
				start = index + 1
			}
		}
	}
	if quote != 0 || depth != 0 {
		return nil, repository.ErrSchemaDrift
	}
	parts = append(parts, value[start:])
	return parts, nil
}

func unquoteIdentifier(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') ||
		(value[0] == '`' && value[len(value)-1] == '`') || (value[0] == '[' && value[len(value)-1] == ']')) {
		value = value[1 : len(value)-1]
	}
	for _, character := range value {
		if character != '_' && !unicode.IsLetter(character) && !unicode.IsDigit(character) {
			return ""
		}
	}
	return value
}

func equalValue(left, right value) bool {
	return left.kind == right.kind && left.integer == right.integer && left.float == right.float &&
		left.text == right.text && bytes.Equal(left.blob, right.blob)
}

func expectedSchema() map[string][]column {
	nullText := func(name string) column { return column{name: name, affinity: "TEXT"} }
	requiredText := func(name string) column { return column{name: name, affinity: "TEXT", notNull: true} }
	defaultText := func(name, defaultValue string) column {
		return column{name: name, affinity: "TEXT", notNull: true, hasDefault: true, defaultVal: value{kind: kindText, text: defaultValue}}
	}
	defaultInteger := func(name string, defaultValue int64) column {
		return column{name: name, affinity: "INTEGER", notNull: true, hasDefault: true, defaultVal: value{kind: kindInteger, integer: defaultValue}}
	}
	return map[string][]column{
		"settings": {
			{name: "key", affinity: "TEXT", primaryKey: true}, requiredText("value"),
		},
		"projects": {
			{name: "id", affinity: "INTEGER", primaryKey: true}, requiredText("name"),
			defaultText("workspace_path", ""), defaultText("description", ""),
			requiredText("created_at"), requiredText("updated_at"),
		},
		"project_states": {
			{name: "project_id", affinity: "INTEGER", primaryKey: true}, defaultInteger("running", 0),
			defaultText("phase", "idle"), defaultInteger("interval_seconds", 5),
			defaultText("validation_command", ""), defaultText("project_prompt", ""),
			defaultText("agent_cli_provider", "codex"), defaultText("agent_cli_command", ""), nullText("codex_reasoning_effort"),
			defaultText("plan_generation_strategy", "external-cli-markdown"), nullText("plan_generation_provider"),
			defaultText("plan_generation_command", ""), defaultText("plan_generation_model", ""),
			nullText("plan_generation_codex_reasoning_effort"), defaultText("plan_generation_claude_base_url", ""),
			defaultText("plan_generation_claude_auth_token", ""), defaultText("plan_generation_claude_model", ""),
			defaultText("plan_execution_strategy", "external-cli"), nullText("plan_execution_provider"),
			defaultText("plan_execution_command", ""), defaultText("plan_execution_model", ""),
			nullText("plan_execution_codex_reasoning_effort"), defaultText("plan_execution_claude_base_url", ""),
			defaultText("plan_execution_claude_auth_token", ""), defaultText("plan_execution_claude_model", ""),
			nullText("last_issue_hash"), nullText("last_error"), defaultText("env_vars", ""), requiredText("updated_at"),
			defaultInteger("plan_generation_claude_config_id", 0), defaultInteger("plan_execution_claude_config_id", 0),
		},
	}
}
