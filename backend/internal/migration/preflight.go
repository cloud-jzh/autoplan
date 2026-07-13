package migration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	maximumSourceBytes = int64(8 << 30)
	minimumHeadroom    = int64(16 << 20)
)

var (
	ErrPreflightUnsafeTarget      = errors.New("preflight_unsafe_target")
	ErrPreflightSourceInvalid     = errors.New("preflight_source_invalid")
	ErrPreflightSidecarActive     = errors.New("preflight_active_sidecar")
	ErrPreflightBackupInvalid     = errors.New("preflight_backup_directory_invalid")
	ErrPreflightInsufficientSpace = errors.New("preflight_insufficient_space")
	ErrPreflightPrerequisite      = errors.New("preflight_prerequisite_failed")
	ErrPreflightSourceChanged     = errors.New("preflight_source_changed")
	ErrPreflightFutureVersion     = errors.New("preflight_future_version")
)

type AvailableBytesFunc func(string) (int64, error)
type EvidenceCheckFunc func(string) error

type PreflightOptions struct {
	Target            string
	AllowedRoot       string
	BackupDirectory   string
	RepositoryRoot    string
	DeclaredResources []DeclaredResource
	SanitizedCopy     bool
	RestoreMode       bool
	AvailableBytes    AvailableBytesFunc
	EvidenceCheck     EvidenceCheckFunc
}

// DeclaredResource is an explicit non-database artifact that belongs to a
// cutover set (for example an attachment or a plan file).  Discovery is
// deliberately not implicit: callers must declare every artifact they expect
// to preserve so a missing resource is a blocking failure rather than a
// silently incomplete backup.
type DeclaredResource struct {
	Role string
	Path string
}

// ResourceReport contains the verified resource identity used by the backup
// stage.  It is an internal hand-off, not a transport response.
type ResourceReport struct {
	Role       string
	Path       string
	Size       int64
	SHA256     string
	SourceInfo os.FileInfo
	Directory  bool
	Entries    []ResourceEntry
}

// ResourceEntry is intentionally kept inside the migration boundary.  Its
// absolute path is never included in a backup manifest or maintenance status.
type ResourceEntry struct {
	Path       string
	Relative   string
	Size       int64
	SHA256     string
	SourceInfo os.FileInfo
}

type PreflightReport struct {
	Target           string
	BackupDirectory  string
	Size             int64
	SHA256           string
	UserVersion      int
	RequiredBytes    int64
	StableDatabaseID string
	SourceInfo       os.FileInfo
	Resources        []ResourceReport
}

func Preflight(ctx context.Context, options PreflightOptions) (PreflightReport, error) {
	if err := ctx.Err(); err != nil {
		return PreflightReport{}, err
	}
	if !options.SanitizedCopy || options.Target == "" || options.AllowedRoot == "" ||
		options.BackupDirectory == "" || !filepath.IsAbs(options.Target) ||
		!filepath.IsAbs(options.AllowedRoot) || !filepath.IsAbs(options.BackupDirectory) {
		return PreflightReport{}, ErrPreflightUnsafeTarget
	}
	target := filepath.Clean(options.Target)
	root := filepath.Clean(options.AllowedRoot)
	backupDirectory := filepath.Clean(options.BackupDirectory)
	if !within(target, root) || !withinOrEqual(backupDirectory, root) || unsafeDatabaseName(target) ||
		containsUserDataMarker(target) || containsUserDataMarker(root) {
		return PreflightReport{}, ErrPreflightUnsafeTarget
	}
	resolvedRoot, err := secureDirectory(root)
	if err != nil {
		return PreflightReport{}, ErrPreflightUnsafeTarget
	}
	resolvedTarget, info, err := secureRegularFile(target)
	if err != nil || !samePath(target, resolvedTarget) || !within(resolvedTarget, resolvedRoot) {
		return PreflightReport{}, ErrPreflightUnsafeTarget
	}
	resolvedBackup, err := secureDirectory(backupDirectory)
	if err != nil || !samePath(backupDirectory, resolvedBackup) || !withinOrEqual(resolvedBackup, resolvedRoot) {
		return PreflightReport{}, ErrPreflightBackupInvalid
	}
	if info.Size() < 0 || info.Size() > maximumSourceBytes {
		return PreflightReport{}, ErrPreflightSourceInvalid
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal", ".autoplan-owner.lock", ".p04-migrate.lock"} {
		if _, err := os.Lstat(target + suffix); err == nil || !os.IsNotExist(err) {
			return PreflightReport{}, ErrPreflightSidecarActive
		}
	}
	if options.EvidenceCheck != nil {
		if err := options.EvidenceCheck(options.RepositoryRoot); err != nil {
			return PreflightReport{}, ErrPreflightPrerequisite
		}
	} else if err := CheckStageEvidence(options.RepositoryRoot); err != nil {
		return PreflightReport{}, ErrPreflightPrerequisite
	}
	digest, version, err := inspectSQLite(ctx, target, info.Size())
	if err != nil {
		if !options.RestoreMode {
			return PreflightReport{}, err
		}
		digest, _, err = hashRegularFile(ctx, target)
		if err != nil {
			return PreflightReport{}, ErrPreflightSourceInvalid
		}
		version = 0
	}
	if version > 1 {
		return PreflightReport{}, ErrPreflightFutureVersion
	}
	resources, resourceBytes, err := inspectDeclaredResources(ctx, options.DeclaredResources, root, resolvedRoot, target, backupDirectory)
	if err != nil {
		return PreflightReport{}, err
	}
	requiredBytes := info.Size()*3 + resourceBytes*2 + minimumHeadroom
	if options.AvailableBytes != nil {
		available, err := options.AvailableBytes(backupDirectory)
		if err != nil || available < requiredBytes {
			return PreflightReport{}, ErrPreflightInsufficientSpace
		}
	}
	after, err := os.Stat(target)
	if err != nil || !os.SameFile(info, after) || info.Size() != after.Size() || info.ModTime() != after.ModTime() {
		return PreflightReport{}, ErrPreflightSourceChanged
	}
	for _, resource := range resources {
		after, err := os.Stat(resource.Path)
		if err != nil || !os.SameFile(resource.SourceInfo, after) || resource.SourceInfo.ModTime() != after.ModTime() ||
			(!resource.Directory && resource.Size != after.Size()) {
			return PreflightReport{}, ErrPreflightSourceChanged
		}
		if resource.Directory {
			entries, size, digest, err := inspectResourceDirectory(ctx, resource.Path)
			if err != nil || size != resource.Size || digest != resource.SHA256 || !sameResourceEntries(resource.Entries, entries) {
				return PreflightReport{}, ErrPreflightSourceChanged
			}
		}
	}
	return PreflightReport{
		Target: target, BackupDirectory: backupDirectory, Size: info.Size(), SHA256: digest,
		UserVersion: version, RequiredBytes: requiredBytes, StableDatabaseID: digest[:16], SourceInfo: info,
		Resources: resources,
	}, nil
}

func sameResourceEntries(left, right []ResourceEntry) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].Relative != right[index].Relative || left[index].Size != right[index].Size || left[index].SHA256 != right[index].SHA256 {
			return false
		}
	}
	return true
}

func inspectDeclaredResources(ctx context.Context, declared []DeclaredResource, root, resolvedRoot, target, backupDirectory string) ([]ResourceReport, int64, error) {
	resources := make([]ResourceReport, 0, len(declared))
	seenRoles := make(map[string]struct{}, len(declared))
	seenPaths := make(map[string]struct{}, len(declared))
	var total int64
	for _, declaredResource := range declared {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		role := strings.TrimSpace(declaredResource.Role)
		resourcePath := filepath.Clean(declaredResource.Path)
		if role == "" || !safeIdentifier(role) || declaredResource.Path == "" || !filepath.IsAbs(declaredResource.Path) ||
			!within(resourcePath, root) || containsUserDataMarker(resourcePath) || samePath(resourcePath, target) ||
			withinOrEqual(resourcePath, backupDirectory) || withinOrEqual(backupDirectory, resourcePath) {
			return nil, 0, ErrPreflightUnsafeTarget
		}
		if _, duplicate := seenRoles[role]; duplicate {
			return nil, 0, ErrPreflightSourceInvalid
		}
		resource, err := inspectDeclaredResource(ctx, role, resourcePath, resolvedRoot)
		if err != nil {
			return nil, 0, err
		}
		if _, duplicate := seenPaths[resource.Path]; duplicate {
			return nil, 0, ErrPreflightSourceInvalid
		}
		if total > maximumSourceBytes-resource.Size {
			return nil, 0, ErrPreflightSourceInvalid
		}
		seenRoles[role] = struct{}{}
		seenPaths[resource.Path] = struct{}{}
		total += resource.Size
		resources = append(resources, resource)
	}
	return resources, total, nil
}

func inspectDeclaredResource(ctx context.Context, role, resourcePath, resolvedRoot string) (ResourceReport, error) {
	info, err := os.Lstat(resourcePath)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		return ResourceReport{}, ErrPreflightSourceInvalid
	}
	if info.Mode().IsRegular() {
		resolvedPath, regularInfo, err := secureRegularFile(resourcePath)
		if err != nil || !samePath(resourcePath, resolvedPath) || !within(resolvedPath, resolvedRoot) || regularInfo.Size() < 0 || regularInfo.Size() > maximumSourceBytes {
			return ResourceReport{}, ErrPreflightSourceInvalid
		}
		digest, size, err := hashRegularFile(ctx, resourcePath)
		if err != nil || size != regularInfo.Size() {
			return ResourceReport{}, ErrPreflightSourceInvalid
		}
		return ResourceReport{
			Role: role, Path: resourcePath, Size: size, SHA256: digest, SourceInfo: regularInfo,
			Entries: []ResourceEntry{{Path: resourcePath, Relative: filepath.Base(resourcePath), Size: size, SHA256: digest, SourceInfo: regularInfo}},
		}, nil
	}
	if !info.IsDir() {
		return ResourceReport{}, ErrPreflightSourceInvalid
	}
	resolvedPath, err := secureDirectory(resourcePath)
	if err != nil || !samePath(resourcePath, resolvedPath) || !within(resolvedPath, resolvedRoot) {
		return ResourceReport{}, ErrPreflightSourceInvalid
	}
	entries, total, digest, err := inspectResourceDirectory(ctx, resourcePath)
	if err != nil || total > maximumSourceBytes {
		return ResourceReport{}, ErrPreflightSourceInvalid
	}
	return ResourceReport{Role: role, Path: resourcePath, Size: total, SHA256: digest, SourceInfo: info, Directory: true, Entries: entries}, nil
}

func inspectResourceDirectory(ctx context.Context, root string) ([]ResourceEntry, int64, string, error) {
	entries := make([]ResourceEntry, 0)
	var total int64
	var visit func(string, string) error
	visit = func(current, relative string) error {
		children, err := os.ReadDir(current)
		if err != nil {
			return err
		}
		for _, child := range children {
			if err := ctx.Err(); err != nil {
				return err
			}
			path := filepath.Join(current, child.Name())
			childRelative := filepath.Join(relative, child.Name())
			info, err := os.Lstat(path)
			if err != nil || info.Mode()&os.ModeSymlink != 0 {
				return ErrPreflightSourceInvalid
			}
			if info.IsDir() {
				if err := visit(path, childRelative); err != nil {
					return err
				}
				continue
			}
			if !info.Mode().IsRegular() || info.Size() < 0 || total > maximumSourceBytes-info.Size() {
				return ErrPreflightSourceInvalid
			}
			digest, size, err := hashRegularFile(ctx, path)
			if err != nil || size != info.Size() {
				return ErrPreflightSourceInvalid
			}
			total += size
			entries = append(entries, ResourceEntry{Path: path, Relative: childRelative, Size: size, SHA256: digest, SourceInfo: info})
		}
		return nil
	}
	if err := visit(root, ""); err != nil {
		return nil, 0, "", err
	}
	digest := sha256.New()
	for _, entry := range entries {
		_, _ = io.WriteString(digest, filepath.ToSlash(entry.Relative)+"\x00"+entry.SHA256+"\x00")
	}
	return entries, total, hex.EncodeToString(digest.Sum(nil)), nil
}

func inspectSQLite(ctx context.Context, target string, size int64) (string, int, error) {
	file, err := os.Open(target)
	if err != nil {
		return "", 0, ErrPreflightSourceInvalid
	}
	defer file.Close()
	hasher := sha256.New()
	content := make([]byte, 100)
	read, readErr := io.ReadFull(file, content)
	if readErr != nil && readErr != io.ErrUnexpectedEOF && readErr != io.EOF {
		return "", 0, ErrPreflightSourceInvalid
	}
	if size != 0 && (read < 100 || !bytes.Equal(content[:16], []byte("SQLite format 3\x00"))) {
		return "", 0, ErrPreflightSourceInvalid
	}
	if size != 0 {
		pageSize := int64(binary.BigEndian.Uint16(content[16:18]))
		if pageSize == 1 {
			pageSize = 65536
		}
		schemaFormat := binary.BigEndian.Uint32(content[44:48])
		pageCount := int64(binary.BigEndian.Uint32(content[28:32]))
		if pageSize < 512 || pageSize > 65536 || pageSize&(pageSize-1) != 0 ||
			(content[18] != 1 && content[18] != 2) || (content[19] != 1 && content[19] != 2) ||
			schemaFormat < 1 || schemaFormat > 4 || size%pageSize != 0 || pageCount <= 0 || pageCount > size/pageSize {
			return "", 0, ErrPreflightSourceInvalid
		}
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", 0, ErrPreflightSourceInvalid
	}
	buffer := make([]byte, 128*1024)
	for {
		if err := ctx.Err(); err != nil {
			return "", 0, err
		}
		count, readErr := file.Read(buffer)
		if count > 0 {
			_, _ = hasher.Write(buffer[:count])
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", 0, ErrPreflightSourceInvalid
		}
	}
	version := 0
	if size >= 64 {
		version = int(binary.BigEndian.Uint32(content[60:64]))
	}
	return hex.EncodeToString(hasher.Sum(nil)), version, nil
}

func secureDirectory(name string) (string, error) {
	info, err := os.Lstat(name)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", ErrPreflightUnsafeTarget
	}
	resolved, err := filepath.EvalSymlinks(name)
	if err != nil {
		return "", ErrPreflightUnsafeTarget
	}
	return filepath.Clean(resolved), nil
}

func secureRegularFile(name string) (string, os.FileInfo, error) {
	info, err := os.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", nil, ErrPreflightUnsafeTarget
	}
	resolved, err := filepath.EvalSymlinks(name)
	if err != nil {
		return "", nil, ErrPreflightUnsafeTarget
	}
	return filepath.Clean(resolved), info, nil
}

func unsafeDatabaseName(name string) bool {
	base := strings.ToLower(filepath.Base(name))
	if base == "autoplan.sqlite" {
		return true
	}
	return !(strings.HasSuffix(base, ".sqlite") || strings.HasSuffix(base, ".sqlite.copy") ||
		strings.HasSuffix(base, ".sqlite.backup") || strings.HasSuffix(base, ".sqlite.bak") ||
		strings.HasSuffix(base, ".copy") || strings.HasSuffix(base, ".backup") || strings.HasSuffix(base, ".bak"))
}

func containsUserDataMarker(name string) bool {
	value := strings.ToLower(filepath.ToSlash(name))
	return strings.Contains(value, "/appdata/roaming/autoplan") ||
		strings.Contains(value, "/library/application support/autoplan") ||
		strings.Contains(value, "/.config/autoplan")
}

func samePath(left, right string) bool {
	return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
}

func within(target, root string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(target))
	return err == nil && relative != "." && relative != ".." && !filepath.IsAbs(relative) &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func withinOrEqual(target, root string) bool {
	return samePath(target, root) || within(target, root)
}

type stageSummary struct {
	SchemaVersion      int    `json:"schemaVersion"`
	Status             string `json:"status"`
	OK                 bool   `json:"ok"`
	SourceHashesStable bool   `json:"sourceHashesStable"`
}

type evidenceManifest struct {
	SchemaVersion         int  `json:"schemaVersion"`
	ImmutableRunDirectory bool `json:"immutableRunDirectory"`
	Artifacts             []struct {
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
	} `json:"artifacts"`
}

func CheckStageEvidence(repositoryRoot string) error {
	if repositoryRoot == "" || !filepath.IsAbs(repositoryRoot) {
		return ErrPreflightPrerequisite
	}
	for _, stage := range []string{"p02", "p03"} {
		runs := filepath.Join(repositoryRoot, "docs", "migration", stage, "evidence", "runs")
		entries, err := os.ReadDir(runs)
		if err != nil {
			return ErrPreflightPrerequisite
		}
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			if entry.IsDir() {
				names = append(names, entry.Name())
			}
		}
		if len(names) == 0 {
			return ErrPreflightPrerequisite
		}
		sort.Sort(sort.Reverse(sort.StringSlice(names)))
		run := filepath.Join(runs, names[0])
		summaryBytes, err := os.ReadFile(filepath.Join(run, "summary.json"))
		if err != nil {
			return ErrPreflightPrerequisite
		}
		var summary stageSummary
		if err := json.Unmarshal(summaryBytes, &summary); err != nil || summary.SchemaVersion != 1 ||
			summary.Status != "completed" || !summary.OK || !summary.SourceHashesStable {
			return ErrPreflightPrerequisite
		}
		manifestBytes, err := os.ReadFile(filepath.Join(run, "evidence-manifest.json"))
		if err != nil {
			return ErrPreflightPrerequisite
		}
		var manifest evidenceManifest
		if err := json.Unmarshal(manifestBytes, &manifest); err != nil || manifest.SchemaVersion != 1 || !manifest.ImmutableRunDirectory {
			return ErrPreflightPrerequisite
		}
		digest := sha256.Sum256(summaryBytes)
		expected := hex.EncodeToString(digest[:])
		matched := false
		for _, artifact := range manifest.Artifacts {
			if artifact.Path == "summary.json" && artifact.SHA256 == expected {
				matched = true
			}
		}
		if !matched {
			return ErrPreflightPrerequisite
		}
	}
	return nil
}
