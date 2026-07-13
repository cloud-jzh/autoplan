package secrets

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maximumAuthorizationBytes = 64 << 10

type authorization struct {
	SchemaVersion      int    `json:"schema_version"`
	Purpose            string `json:"purpose"`
	SanitizedCopy      bool   `json:"sanitized_copy"`
	DatabaseFile       string `json:"database_file"`
	DatabaseSHA256     string `json:"database_sha256"`
	BackupDirectory    string `json:"backup_directory"`
	SecretStorage      string `json:"secret_storage_directory"`
	KeyDirectory       string `json:"key_directory"`
	PreparedAt         string `json:"prepared_at"`
	NodeSQLJSClosed    bool   `json:"node_sqljs_closed"`
	ProductionDatabase bool   `json:"production_database_opened"`
}

func loadAuthorization(request Request) (authorization, error) {
	if request.Authorization == "" || !filepath.IsAbs(request.Authorization) ||
		!withinOrEqual(request.Authorization, request.AllowedRoot) {
		return authorization{}, ErrUnauthorized
	}
	info, err := os.Lstat(request.Authorization)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > maximumAuthorizationBytes {
		return authorization{}, ErrUnauthorized
	}
	resolvedRoot, err := filepath.EvalSymlinks(request.AllowedRoot)
	if err != nil {
		return authorization{}, ErrUnauthorized
	}
	resolvedAuthorization, err := filepath.EvalSymlinks(request.Authorization)
	if err != nil || !samePath(resolvedAuthorization, request.Authorization) || !withinOrEqual(resolvedAuthorization, resolvedRoot) {
		return authorization{}, ErrUnauthorized
	}
	content, err := os.ReadFile(request.Authorization)
	if err != nil {
		return authorization{}, ErrUnauthorized
	}
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	decoder.DisallowUnknownFields()
	var value authorization
	if err := decoder.Decode(&value); err != nil {
		return authorization{}, ErrUnauthorized
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return authorization{}, ErrUnauthorized
	}
	if value.SchemaVersion != 1 || value.Purpose != "p08-secret-copy" || !value.SanitizedCopy ||
		value.DatabaseFile != filepath.Base(request.Database) || !lowerHex(value.DatabaseSHA256, 64) ||
		!safeDirectoryName(value.BackupDirectory) || !safeDirectoryName(value.SecretStorage) || !safeDirectoryName(value.KeyDirectory) ||
		!value.NodeSQLJSClosed || value.ProductionDatabase {
		return authorization{}, ErrUnauthorized
	}
	if _, err := time.Parse(time.RFC3339Nano, value.PreparedAt); err != nil {
		return authorization{}, ErrUnauthorized
	}
	if filepath.Base(request.BackupDirectory) != value.BackupDirectory ||
		filepath.Base(request.SecretStorageRoot) != value.SecretStorage || filepath.Base(request.KeyRoot) != value.KeyDirectory {
		return authorization{}, ErrUnauthorized
	}
	return value, nil
}

func validateAuthorizationHash(value authorization, actual string) error {
	if value.DatabaseSHA256 != actual {
		return ErrUnauthorized
	}
	return nil
}

func safeDirectoryName(value string) bool {
	return value != "" && filepath.Base(value) == value && value != "." && value != ".." && !strings.ContainsAny(value, "\\/\x00")
}

func lowerHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, r := range value {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func withinOrEqual(value, root string) bool {
	if value == "" || root == "" {
		return false
	}
	value = filepath.Clean(value)
	root = filepath.Clean(root)
	relative, err := filepath.Rel(root, value)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator))
}

func hashFile(name string) (string, int64, error) {
	file, err := os.Open(name)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", 0, ErrUnauthorized
	}
	digest := sha256.New()
	count, err := io.Copy(digest, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(digest.Sum(nil)), count, nil
}
