package migration

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

var (
	ErrBackupExists       = errors.New("backup_destination_exists")
	ErrBackupFailed       = errors.New("backup_failed")
	ErrBackupVerification = errors.New("backup_verification_failed")
	ErrManifestInvalid    = errors.New("backup_manifest_invalid")
)

type BackupOptions struct {
	Preflight PreflightReport
	Clock     Clock
	RunID     string
}

type BackupArtifact struct {
	Role   string `json:"role"`
	File   string `json:"file"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type BackupManifest struct {
	SchemaVersion     int              `json:"schema_version"`
	ManifestID        string           `json:"manifest_id"`
	CreatedAt         string           `json:"created_at"`
	SourceID          string           `json:"source_id"`
	SourceSHA256      string           `json:"source_sha256"`
	SourceSize        int64            `json:"source_size"`
	SourceUserVersion int              `json:"source_user_version"`
	Artifacts         []BackupArtifact `json:"artifacts"`
	DatabaseContent   bool             `json:"database_content_in_manifest"`
}

type BackupSet struct {
	ManifestPath   string
	ManifestID     string
	ManifestSHA256 string
	Artifacts      []BackupArtifact
}

type backupSource struct {
	role     string
	path     string
	resource *ResourceReport
}

func CreateBackup(ctx context.Context, options BackupOptions) (result BackupSet, returnErr error) {
	if err := ctx.Err(); err != nil {
		return BackupSet{}, err
	}
	if options.Clock == nil || options.RunID == "" || !safeIdentifier(options.RunID) ||
		options.Preflight.Target == "" || options.Preflight.BackupDirectory == "" {
		return BackupSet{}, ErrBackupFailed
	}
	timestamp := options.Clock.Now().UTC().Format("20060102T150405.000000000Z")
	prefix := "autoplan-p04." + options.Preflight.StableDatabaseID + "." + timestamp + "." + options.RunID
	manifestID := prefix + ".manifest.json"
	manifestPath := filepath.Join(options.Preflight.BackupDirectory, manifestID)
	created := make([]string, 0, 4)
	committed := false
	defer func() {
		if !committed {
			for index := len(created) - 1; index >= 0; index-- {
				_ = os.Remove(created[index])
			}
		}
	}()

	sources := []backupSource{
		{role: "database", path: options.Preflight.Target},
		{role: "legacy-backup", path: options.Preflight.Target + ".bak"},
		{role: "mirror", path: options.Preflight.Target + ".mirror"},
	}
	for index := range options.Preflight.Resources {
		resource := &options.Preflight.Resources[index]
		if resource.Role == "" || !safeIdentifier(resource.Role) || resource.Path == "" {
			return BackupSet{}, ErrBackupFailed
		}
		sources = append(sources, backupSource{role: "resource-" + resource.Role, path: resource.Path, resource: resource})
	}
	artifacts := make([]BackupArtifact, 0, len(sources))
	for _, source := range sources {
		info, err := os.Lstat(source.path)
		if err != nil {
			if os.IsNotExist(err) && source.resource == nil && source.role != "database" {
				continue
			}
			return BackupSet{}, ErrBackupFailed
		}
		if info.Mode()&os.ModeSymlink != 0 || (source.resource == nil && !info.Mode().IsRegular()) ||
			(source.resource != nil && source.resource.Directory != info.IsDir()) {
			return BackupSet{}, ErrBackupFailed
		}
		if source.resource != nil && !resourceSnapshotMatches(ctx, *source.resource) {
			return BackupSet{}, ErrBackupVerification
		}
		extension := ".bak"
		if source.resource != nil && source.resource.Directory {
			extension = ".tar"
		}
		fileName := prefix + "." + source.role + extension
		destination := filepath.Join(options.Preflight.BackupDirectory, fileName)
		var artifact BackupArtifact
		if source.resource != nil && source.resource.Directory {
			artifact, err = exclusiveArchive(ctx, *source.resource, destination, source.role)
		} else {
			artifact, err = exclusiveCopy(ctx, source.path, destination, source.role)
		}
		if err != nil {
			return BackupSet{}, err
		}
		if source.resource != nil && !resourceSnapshotMatches(ctx, *source.resource) {
			_ = os.Remove(destination)
			return BackupSet{}, ErrBackupVerification
		}
		created = append(created, destination)
		if source.role == "database" && (artifact.SHA256 != options.Preflight.SHA256 || artifact.Size != options.Preflight.Size) {
			return BackupSet{}, ErrBackupVerification
		}
		if strings.HasPrefix(source.role, "resource-") {
			matched := false
			for _, resource := range options.Preflight.Resources {
				if source.role == "resource-"+resource.Role && (resource.Directory || (artifact.SHA256 == resource.SHA256 && artifact.Size == resource.Size)) {
					matched = true
					break
				}
			}
			if !matched {
				return BackupSet{}, ErrBackupVerification
			}
		}
		artifacts = append(artifacts, artifact)
	}
	manifest := BackupManifest{
		SchemaVersion: 1, ManifestID: manifestID,
		CreatedAt: options.Clock.Now().UTC().Format(time.RFC3339Nano),
		SourceID:  options.Preflight.StableDatabaseID, SourceSHA256: options.Preflight.SHA256,
		SourceSize: options.Preflight.Size, SourceUserVersion: options.Preflight.UserVersion,
		Artifacts: artifacts, DatabaseContent: false,
	}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return BackupSet{}, ErrBackupFailed
	}
	encoded = append(encoded, '\n')
	manifestFile, err := os.OpenFile(manifestPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return BackupSet{}, ErrBackupExists
		}
		return BackupSet{}, ErrBackupFailed
	}
	created = append(created, manifestPath)
	if _, err := manifestFile.Write(encoded); err != nil || manifestFile.Sync() != nil || manifestFile.Close() != nil {
		_ = manifestFile.Close()
		return BackupSet{}, ErrBackupFailed
	}
	if err := syncDirectory(options.Preflight.BackupDirectory); err != nil {
		return BackupSet{}, ErrBackupFailed
	}
	digest := sha256.Sum256(encoded)
	loaded, loadedDigest, err := LoadAndVerifyManifest(ctx, manifestPath, options.Preflight.BackupDirectory)
	if err != nil || loaded.ManifestID != manifestID || loadedDigest != hex.EncodeToString(digest[:]) {
		return BackupSet{}, ErrBackupVerification
	}
	committed = true
	return BackupSet{
		ManifestPath: manifestPath, ManifestID: manifestID,
		ManifestSHA256: loadedDigest, Artifacts: append([]BackupArtifact(nil), artifacts...),
	}, nil
}

func resourceSnapshotMatches(ctx context.Context, resource ResourceReport) bool {
	info, err := os.Stat(resource.Path)
	if err != nil || !os.SameFile(resource.SourceInfo, info) || resource.SourceInfo.ModTime() != info.ModTime() ||
		(!resource.Directory && resource.Size != info.Size()) {
		return false
	}
	if !resource.Directory {
		digest, size, err := hashRegularFile(ctx, resource.Path)
		return err == nil && digest == resource.SHA256 && size == resource.Size
	}
	entries, size, digest, err := inspectResourceDirectory(ctx, resource.Path)
	return err == nil && size == resource.Size && digest == resource.SHA256 && sameResourceEntries(resource.Entries, entries)
}

func exclusiveArchive(ctx context.Context, resource ResourceReport, destination, role string) (BackupArtifact, error) {
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return BackupArtifact{}, ErrBackupExists
		}
		return BackupArtifact{}, ErrBackupFailed
	}
	hasher := sha256.New()
	writer := tar.NewWriter(io.MultiWriter(output, hasher))
	archiveRoot := filepath.ToSlash(filepath.Base(resource.Path))
	writeErr := writer.WriteHeader(&tar.Header{Name: archiveRoot + "/", Mode: 0o700, Typeflag: tar.TypeDir})
	for _, entry := range resource.Entries {
		if writeErr != nil {
			break
		}
		if err := ctx.Err(); err != nil {
			writeErr = err
			break
		}
		if digest, size, err := hashRegularFile(ctx, entry.Path); err != nil || digest != entry.SHA256 || size != entry.Size {
			writeErr = ErrBackupVerification
			break
		}
		if writeErr = writer.WriteHeader(&tar.Header{
			Name: filepath.ToSlash(filepath.Join(archiveRoot, entry.Relative)), Mode: 0o600, Size: entry.Size, Typeflag: tar.TypeReg,
		}); writeErr != nil {
			break
		}
		input, err := os.Open(entry.Path)
		if err != nil {
			writeErr = ErrBackupFailed
			break
		}
		_, copyErr := copyWithContext(ctx, writer, input)
		closeErr := input.Close()
		if copyErr != nil || closeErr != nil {
			writeErr = ErrBackupFailed
			break
		}
	}
	if closeErr := writer.Close(); writeErr == nil && closeErr != nil {
		writeErr = closeErr
	}
	syncErr := output.Sync()
	closeErr := output.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		_ = os.Remove(destination)
		if errors.Is(writeErr, ErrBackupVerification) {
			return BackupArtifact{}, ErrBackupVerification
		}
		return BackupArtifact{}, ErrBackupFailed
	}
	digest := hex.EncodeToString(hasher.Sum(nil))
	verified, size, err := hashRegularFile(ctx, destination)
	if err != nil || verified != digest {
		_ = os.Remove(destination)
		return BackupArtifact{}, ErrBackupVerification
	}
	return BackupArtifact{Role: role, File: filepath.Base(destination), Size: size, SHA256: digest}, nil
}

func exclusiveCopy(ctx context.Context, source, destination, role string) (BackupArtifact, error) {
	input, err := os.Open(source)
	if err != nil {
		return BackupArtifact{}, ErrBackupFailed
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return BackupArtifact{}, ErrBackupExists
		}
		return BackupArtifact{}, ErrBackupFailed
	}
	hasher := sha256.New()
	written, copyErr := copyWithContext(ctx, io.MultiWriter(output, hasher), input)
	syncErr := output.Sync()
	closeErr := output.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil {
		_ = os.Remove(destination)
		return BackupArtifact{}, ErrBackupFailed
	}
	digest := hex.EncodeToString(hasher.Sum(nil))
	verified, size, err := hashRegularFile(ctx, destination)
	if err != nil || size != written || verified != digest {
		_ = os.Remove(destination)
		return BackupArtifact{}, ErrBackupVerification
	}
	return BackupArtifact{Role: role, File: filepath.Base(destination), Size: written, SHA256: digest}, nil
}

func LoadAndVerifyManifest(ctx context.Context, manifestPath, allowedRoot string) (BackupManifest, string, error) {
	if !filepath.IsAbs(manifestPath) || !filepath.IsAbs(allowedRoot) || !withinOrEqual(manifestPath, allowedRoot) {
		return BackupManifest{}, "", ErrManifestInvalid
	}
	resolvedRoot, err := secureDirectory(allowedRoot)
	if err != nil {
		return BackupManifest{}, "", ErrManifestInvalid
	}
	resolvedManifest, info, err := secureRegularFile(manifestPath)
	if err != nil || !samePath(resolvedManifest, manifestPath) || !within(resolvedManifest, resolvedRoot) || info.Size() > 1<<20 {
		return BackupManifest{}, "", ErrManifestInvalid
	}
	content, err := os.ReadFile(manifestPath)
	if err != nil {
		return BackupManifest{}, "", ErrManifestInvalid
	}
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	decoder.DisallowUnknownFields()
	var manifest BackupManifest
	if err := decoder.Decode(&manifest); err != nil || manifest.SchemaVersion != 1 ||
		manifest.ManifestID != filepath.Base(manifestPath) || manifest.DatabaseContent ||
		!strings.HasSuffix(manifest.ManifestID, ".manifest.json") ||
		!safeIdentifier(strings.TrimSuffix(manifest.ManifestID, ".manifest.json")) ||
		!isLowerHex(manifest.SourceID, 16) || !isLowerHex(manifest.SourceSHA256, 64) ||
		manifest.SourceSize < 0 || manifest.SourceUserVersion < 0 || manifest.SourceUserVersion > 1 ||
		!strings.HasSuffix(manifest.CreatedAt, "Z") || len(manifest.Artifacts) == 0 {
		return BackupManifest{}, "", ErrManifestInvalid
	}
	if _, err := time.Parse(time.RFC3339Nano, manifest.CreatedAt); err != nil {
		return BackupManifest{}, "", ErrManifestInvalid
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return BackupManifest{}, "", ErrManifestInvalid
	}
	seen := make(map[string]bool)
	mainCount := 0
	mainMatchesSource := false
	for _, artifact := range manifest.Artifacts {
		if err := ctx.Err(); err != nil {
			return BackupManifest{}, "", err
		}
		if artifact.File == "" || filepath.Base(artifact.File) != artifact.File || seen[artifact.File] ||
			artifact.Size < 0 || !isLowerHex(artifact.SHA256, 64) || !validArtifactRole(artifact.Role) {
			return BackupManifest{}, "", ErrManifestInvalid
		}
		seen[artifact.File] = true
		if artifact.Role == "database" {
			mainCount++
			mainMatchesSource = artifact.SHA256 == manifest.SourceSHA256 && artifact.Size == manifest.SourceSize
		}
		artifactPath := filepath.Join(allowedRoot, artifact.File)
		resolvedArtifact, _, resolveErr := secureRegularFile(artifactPath)
		if resolveErr != nil || !samePath(artifactPath, resolvedArtifact) || !within(resolvedArtifact, resolvedRoot) {
			return BackupManifest{}, "", ErrManifestInvalid
		}
		actual, size, err := hashRegularFile(ctx, artifactPath)
		if err != nil || actual != artifact.SHA256 || size != artifact.Size {
			return BackupManifest{}, "", ErrBackupVerification
		}
	}
	if mainCount != 1 || !mainMatchesSource {
		return BackupManifest{}, "", ErrManifestInvalid
	}
	digest := sha256.Sum256(content)
	return manifest, hex.EncodeToString(digest[:]), nil
}

func hashRegularFile(ctx context.Context, name string) (string, int64, error) {
	file, err := os.Open(name)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", 0, ErrBackupVerification
	}
	hasher := sha256.New()
	written, err := copyWithContext(ctx, hasher, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hasher.Sum(nil)), written, nil
}

func copyWithContext(ctx context.Context, destination io.Writer, source io.Reader) (int64, error) {
	buffer := make([]byte, 128*1024)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		read, readErr := source.Read(buffer)
		if read > 0 {
			written, writeErr := destination.Write(buffer[:read])
			total += int64(written)
			if writeErr != nil || written != read {
				return total, ErrBackupFailed
			}
		}
		if readErr == io.EOF {
			return total, nil
		}
		if readErr != nil {
			return total, readErr
		}
	}
}

func syncDirectory(name string) error {
	if runtime.GOOS == "windows" {
		// Windows does not expose a directory FlushFileBuffers handle through
		// os.File; each artifact and manifest was already individually synced.
		return nil
	}
	directory, err := os.Open(name)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func safeIdentifier(value string) bool {
	if value == "" || len(value) > 240 {
		return false
	}
	for _, char := range value {
		if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || strings.ContainsRune("._-", char)) {
			return false
		}
	}
	return true
}

func isLowerHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, char := range value {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return false
		}
	}
	return true
}

func validArtifactRole(role string) bool {
	if role == "database" || role == "legacy-backup" || role == "mirror" {
		return true
	}
	return strings.HasPrefix(role, "resource-") && safeIdentifier(strings.TrimPrefix(role, "resource-"))
}
