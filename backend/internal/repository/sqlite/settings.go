package sqlite

import (
	"context"
	"database/sql"
	"sort"
	"strings"

	"github.com/lyming99/autoplan/backend/internal/repository"
)

var writableSettingKeys = map[string]struct{}{
	"mcp.enabled": {}, "mcp.transport": {}, "mcp.host": {}, "mcp.port": {},
	"mcp.path": {}, "mcp.authToken": {}, "mcp.portExplicit": {},
}

func decodeSettings(rows []tableRow) ([]repository.Setting, error) {
	settings := make([]repository.Setting, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		if len(row.values) != 2 {
			return nil, repository.ErrSchemaDrift
		}
		key, keyOK := textValue(row.values[0])
		settingValue, valueOK := textValue(row.values[1])
		if !keyOK || !valueOK {
			return nil, repository.ErrSchemaDrift
		}
		if _, duplicate := seen[key]; duplicate {
			return nil, repository.ErrSchemaDrift
		}
		seen[key] = struct{}{}
		settings = append(settings, repository.Setting{Key: key, Value: settingValue})
	}
	sort.SliceStable(settings, func(left, right int) bool { return settings[left].Key < settings[right].Key })
	return settings, nil
}

func (reader *Reader) ListSettings(ctx context.Context, prefix string) ([]repository.Setting, error) {
	reader.mu.RLock()
	defer reader.mu.RUnlock()
	if err := reader.guard(ctx); err != nil {
		return nil, err
	}
	result := make([]repository.Setting, 0)
	for _, setting := range reader.settings {
		if strings.HasPrefix(setting.Key, prefix) {
			result = append(result, setting)
		}
	}
	return result, nil
}

func integerValue(item value) (int64, bool) {
	return item.integer, item.kind == kindInteger
}

func textValue(item value) (string, bool) {
	return item.text, item.kind == kindText
}

func nullableTextValue(item value) (*string, bool) {
	if item.kind == kindNull {
		return nil, true
	}
	if item.kind != kindText {
		return nil, false
	}
	result := item.text
	return &result, true
}

func (transaction *writeTransaction) ListSettings(ctx context.Context, prefix string) ([]repository.Setting, error) {
	rows, err := transaction.tx.QueryContext(ctx,
		"SELECT key, value, version FROM settings WHERE key LIKE ? ORDER BY key ASC", prefix+"%")
	if err != nil {
		return nil, safeSQLError(ctx, err)
	}
	defer rows.Close()
	settings := make([]repository.Setting, 0)
	for rows.Next() {
		var setting repository.Setting
		if err := rows.Scan(&setting.Key, &setting.Value, &setting.Version); err != nil {
			return nil, safeSQLError(ctx, err)
		}
		settings = append(settings, setting)
	}
	if err := rows.Err(); err != nil {
		return nil, safeSQLError(ctx, err)
	}
	return settings, nil
}

func (transaction *writeTransaction) getSetting(ctx context.Context, key string) (repository.Setting, bool, error) {
	var setting repository.Setting
	err := transaction.tx.QueryRowContext(ctx,
		"SELECT key, value, version FROM settings WHERE key = ?", key).
		Scan(&setting.Key, &setting.Value, &setting.Version)
	if err == sql.ErrNoRows {
		return repository.Setting{}, false, nil
	}
	if err != nil {
		return repository.Setting{}, false, safeSQLError(ctx, err)
	}
	return setting, true, nil
}

func (transaction *writeTransaction) PutSetting(
	ctx context.Context,
	mutation repository.SettingMutation,
) (repository.Setting, bool, error) {
	if _, allowed := writableSettingKeys[mutation.Key]; !allowed {
		return repository.Setting{}, false, repository.ErrSettingNotWritable
	}
	if mutation.ExpectedVersion <= 0 {
		return repository.Setting{}, false, repository.ErrVersionRequired
	}
	current, found, err := transaction.getSetting(ctx, mutation.Key)
	if err != nil {
		return repository.Setting{}, false, err
	}
	if !found {
		if mutation.ExpectedVersion != 1 {
			return repository.Setting{}, false, repository.ErrVersionConflict
		}
		_, err := transaction.tx.ExecContext(ctx,
			"INSERT INTO settings (key, value, version) VALUES (?, ?, 1)", mutation.Key, mutation.Value)
		if err != nil {
			if safeSQLError(ctx, err) == repository.ErrDuplicate {
				return repository.Setting{}, false, repository.ErrVersionConflict
			}
			return repository.Setting{}, false, safeSQLError(ctx, err)
		}
		if err := transaction.wrote("settings:create"); err != nil {
			return repository.Setting{}, false, err
		}
		return repository.Setting{Key: mutation.Key, Value: mutation.Value, Version: 1}, true, nil
	}
	if current.Version != mutation.ExpectedVersion {
		return repository.Setting{}, false, repository.ErrVersionConflict
	}
	if current.Value == mutation.Value {
		return current, false, nil
	}
	result, err := transaction.tx.ExecContext(ctx,
		`UPDATE settings SET value = ?, version = version + 1 WHERE key = ? AND version = ?`,
		mutation.Value, mutation.Key, mutation.ExpectedVersion)
	if err != nil {
		return repository.Setting{}, false, safeSQLError(ctx, err)
	}
	if err := requireOneRow(result); err != nil {
		if err == repository.ErrNotFound {
			return repository.Setting{}, false, repository.ErrVersionConflict
		}
		return repository.Setting{}, false, err
	}
	if err := transaction.wrote("settings:update"); err != nil {
		return repository.Setting{}, false, err
	}
	current.Value = mutation.Value
	current.Version++
	return current, true, nil
}
