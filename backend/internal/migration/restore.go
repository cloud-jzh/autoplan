package migration

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var (
	ErrRestoreUnsafe       = errors.New("restore_unsafe_target")
	ErrRestoreFailed       = errors.New("restore_failed")
	ErrRestoreVerification = errors.New("restore_verification_failed")
)

type DatabaseVerifyFunc func(context.Context, string) error

type RestoreMode string

const (
	// RestoreModeIndependentCopy creates a new target that did not exist when
	// the operation began.  It is the only rollback route before the first
	// official Go mutation.
	RestoreModeIndependentCopy RestoreMode = "independent_copy"
	// RestoreModeTruncatingReplace is deliberately opt-in and requires a
	// durable, caller-provided truncation record.  It is never selected by an
	// environment variable or an implicit retry.
	RestoreModeTruncatingReplace RestoreMode = "truncating_replace"
)

type TruncationConfirmation struct {
	Confirmed         bool
	Point             string
	AffectedMutations []string
}

// RestoreArtifactTarget maps every non-database manifest artifact to an
// independent destination.  Roles are stable labels; source paths never
// appear in this request or in its result.
type RestoreArtifactTarget struct {
	Role   string
	Target string
}

type RestoreOptions struct {
	ManifestPath      string
	BackupRoot        string
	Target            string
	AllowedTargetRoot string
	RunID             string
	VerifyDatabase    DatabaseVerifyFunc
	Mode              RestoreMode
	Truncation        TruncationConfirmation
	ArtifactTargets   []RestoreArtifactTarget
}

type RestoreResult struct {
	ManifestID             string
	SHA256                 string
	Size                   int64
	UserVersion            int
	Mode                   RestoreMode
	TruncationPoint        string
	AffectedMutationCount  int
	AffectedMutationSHA256 string
	Artifacts              []RestoredArtifact
}

type RestoredArtifact struct {
	Role   string
	SHA256 string
	Size   int64
}

func RestoreBackup(ctx context.Context, options RestoreOptions) (RestoreResult, error) {
	// The pre-P09 migration command still owns its legacy restore contract.
	// P09 rollback orchestration must call RestoreBackupForRollback below, which
	// forbids this compatibility path and requires an explicit boundary mode.
	if options.Mode == "" {
		return restoreReplacingTarget(ctx, options)
	}
	return RestoreBackupForRollback(ctx, options)
}

// RestoreBackupForRollback is the fail-closed recovery API used by the P09
// maintenance boundary.  There is no implicit replacement mode here.
func RestoreBackupForRollback(ctx context.Context, options RestoreOptions) (RestoreResult, error) {
	if err := validateRestoreMode(options); err != nil {
		return RestoreResult{}, err
	}
	switch options.Mode {
	case RestoreModeIndependentCopy:
		return restoreIndependentCopy(ctx, options)
	case RestoreModeTruncatingReplace:
		return restoreReplacingTarget(ctx, options)
	default:
		return RestoreResult{}, ErrRestoreUnsafe
	}
}

func validateRestoreMode(options RestoreOptions) error {
	if options.Mode != RestoreModeIndependentCopy && options.Mode != RestoreModeTruncatingReplace {
		return ErrRestoreUnsafe
	}
	if options.Mode != RestoreModeTruncatingReplace {
		return nil
	}
	if !options.Truncation.Confirmed || !safeIdentifier(options.Truncation.Point) ||
		len(options.Truncation.AffectedMutations) == 0 || len(options.Truncation.AffectedMutations) > 1000 {
		return ErrRestoreUnsafe
	}
	for _, mutation := range options.Truncation.AffectedMutations {
		if !safeIdentifier(mutation) {
			return ErrRestoreUnsafe
		}
	}
	return nil
}

func restoreReplacingTarget(ctx context.Context, options RestoreOptions) (RestoreResult, error) {
	if err := ctx.Err(); err != nil {
		return RestoreResult{}, err
	}
	if len(options.ArtifactTargets) != 0 {
		return RestoreResult{}, ErrRestoreUnsafe
	}
	if !filepath.IsAbs(options.Target) || !filepath.IsAbs(options.AllowedTargetRoot) ||
		!within(options.Target, options.AllowedTargetRoot) || unsafeDatabaseName(options.Target) ||
		containsUserDataMarker(options.Target) || containsUserDataMarker(options.AllowedTargetRoot) || !safeIdentifier(options.RunID) {
		return RestoreResult{}, ErrRestoreUnsafe
	}
	if !filepath.IsAbs(options.BackupRoot) || withinOrEqual(options.Target, options.BackupRoot) || withinOrEqual(options.BackupRoot, options.Target) {
		return RestoreResult{}, ErrRestoreUnsafe
	}
	resolvedTarget, targetInfo, err := secureRegularFile(options.Target)
	if err != nil || !samePath(resolvedTarget, options.Target) {
		return RestoreResult{}, ErrRestoreUnsafe
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal", ".autoplan-owner.lock"} {
		if _, err := os.Lstat(options.Target + suffix); err == nil || !os.IsNotExist(err) {
			return RestoreResult{}, ErrRestoreUnsafe
		}
	}
	manifest, _, err := LoadAndVerifyManifest(ctx, options.ManifestPath, options.BackupRoot)
	if err != nil {
		return RestoreResult{}, err
	}
	for _, artifact := range manifest.Artifacts {
		if artifact.Role != "database" {
			// A post-write replacement must never silently restore only the
			// database while dropping attachment/plan artifacts.  The recovery
			// boundary remains in maintenance for a forward repair instead.
			return RestoreResult{}, ErrRestoreUnsafe
		}
	}
	var databaseArtifact BackupArtifact
	for _, artifact := range manifest.Artifacts {
		if artifact.Role == "database" {
			databaseArtifact = artifact
			break
		}
	}
	backupPath := filepath.Join(options.BackupRoot, databaseArtifact.File)
	staging := options.Target + ".restore." + options.RunID + ".tmp"
	displaced := options.Target + ".restore." + options.RunID + ".previous"
	if _, err := os.Lstat(staging); err == nil || !os.IsNotExist(err) {
		return RestoreResult{}, ErrRestoreUnsafe
	}
	if _, err := os.Lstat(displaced); err == nil || !os.IsNotExist(err) {
		return RestoreResult{}, ErrRestoreUnsafe
	}
	artifact, err := exclusiveCopy(ctx, backupPath, staging, "restore-staging")
	if err != nil {
		return RestoreResult{}, ErrRestoreFailed
	}
	stagingOwned := true
	defer func() {
		if stagingOwned {
			_ = os.Remove(staging)
		}
	}()
	if artifact.SHA256 != databaseArtifact.SHA256 || artifact.Size != databaseArtifact.Size {
		return RestoreResult{}, ErrRestoreVerification
	}
	_, version, err := inspectSQLite(ctx, staging, artifact.Size)
	if err != nil || version != manifest.SourceUserVersion {
		return RestoreResult{}, ErrRestoreVerification
	}
	if options.VerifyDatabase != nil {
		if err := options.VerifyDatabase(ctx, staging); err != nil {
			return RestoreResult{}, ErrRestoreVerification
		}
	}
	_, beforeSize, err := hashRegularFile(ctx, options.Target)
	if err != nil || beforeSize != targetInfo.Size() {
		return RestoreResult{}, ErrRestoreFailed
	}
	if err := os.Rename(options.Target, displaced); err != nil {
		return RestoreResult{}, ErrRestoreFailed
	}
	replaced := false
	defer func() {
		if !replaced {
			_ = os.Rename(displaced, options.Target)
		}
	}()
	if err := os.Rename(staging, options.Target); err != nil {
		return RestoreResult{}, ErrRestoreFailed
	}
	stagingOwned = false
	finalSHA, finalSize, err := hashRegularFile(ctx, options.Target)
	if err != nil || finalSHA != databaseArtifact.SHA256 || finalSize != databaseArtifact.Size {
		_ = os.Remove(options.Target)
		return RestoreResult{}, ErrRestoreVerification
	}
	if options.VerifyDatabase != nil {
		if err := options.VerifyDatabase(ctx, options.Target); err != nil {
			_ = os.Remove(options.Target)
			return RestoreResult{}, ErrRestoreVerification
		}
	}
	if err := syncDirectory(filepath.Dir(options.Target)); err != nil {
		_ = os.Remove(options.Target)
		return RestoreResult{}, ErrRestoreFailed
	}
	if err := os.Remove(displaced); err != nil {
		_ = os.Remove(options.Target)
		if os.Rename(displaced, options.Target) == nil {
			replaced = true
		}
		return RestoreResult{}, ErrRestoreFailed
	}
	replaced = true
	return restoreResult(manifest, finalSHA, finalSize, version, options, nil), nil
}

func restoreIndependentCopy(ctx context.Context, options RestoreOptions) (RestoreResult, error) {
	if err := ctx.Err(); err != nil {
		return RestoreResult{}, err
	}
	if err := validateIndependentTarget(options.Target, options.AllowedTargetRoot, true); err != nil || !safeIdentifier(options.RunID) {
		return RestoreResult{}, ErrRestoreUnsafe
	}
	if !filepath.IsAbs(options.BackupRoot) || withinOrEqual(options.Target, options.BackupRoot) || withinOrEqual(options.BackupRoot, options.Target) {
		return RestoreResult{}, ErrRestoreUnsafe
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal", ".autoplan-owner.lock"} {
		if _, err := os.Lstat(options.Target + suffix); err == nil || !os.IsNotExist(err) {
			return RestoreResult{}, ErrRestoreUnsafe
		}
	}
	manifest, _, err := LoadAndVerifyManifest(ctx, options.ManifestPath, options.BackupRoot)
	if err != nil {
		return RestoreResult{}, err
	}
	databaseArtifact, ok := manifestArtifact(manifest, "database")
	if !ok {
		return RestoreResult{}, ErrRestoreVerification
	}
	staging := options.Target + ".restore." + options.RunID + ".tmp"
	if _, err := os.Lstat(staging); err == nil || !os.IsNotExist(err) {
		return RestoreResult{}, ErrRestoreUnsafe
	}
	artifactPath := filepath.Join(options.BackupRoot, databaseArtifact.File)
	artifact, err := exclusiveCopy(ctx, artifactPath, staging, "restore-staging")
	if err != nil {
		return RestoreResult{}, ErrRestoreFailed
	}
	stagingOwned := true
	defer func() {
		if stagingOwned {
			_ = os.Remove(staging)
		}
	}()
	if artifact.SHA256 != databaseArtifact.SHA256 || artifact.Size != databaseArtifact.Size {
		return RestoreResult{}, ErrRestoreVerification
	}
	_, version, err := inspectSQLite(ctx, staging, artifact.Size)
	if err != nil || version != manifest.SourceUserVersion {
		return RestoreResult{}, ErrRestoreVerification
	}
	if options.VerifyDatabase != nil {
		if err := options.VerifyDatabase(ctx, staging); err != nil {
			return RestoreResult{}, ErrRestoreVerification
		}
	}
	plans, err := stageIndependentArtifacts(ctx, options, manifest)
	if err != nil {
		return RestoreResult{}, err
	}
	committed := make([]artifactPlan, 0, len(plans)+1)
	defer func() {
		for index := len(plans) - 1; index >= 0; index-- {
			if plans[index].stagingOwned {
				_ = os.RemoveAll(plans[index].staging)
			}
		}
		for index := len(committed) - 1; index >= 0; index-- {
			_ = os.RemoveAll(committed[index].target)
		}
	}()
	if err := os.Rename(staging, options.Target); err != nil {
		return RestoreResult{}, ErrRestoreFailed
	}
	stagingOwned = false
	committed = append(committed, artifactPlan{target: options.Target})
	for index := range plans {
		if err := os.Rename(plans[index].staging, plans[index].target); err != nil {
			return RestoreResult{}, ErrRestoreFailed
		}
		plans[index].stagingOwned = false
		committed = append(committed, artifactPlan{target: plans[index].target})
	}
	finalSHA, finalSize, err := hashRegularFile(ctx, options.Target)
	if err != nil || finalSHA != databaseArtifact.SHA256 || finalSize != databaseArtifact.Size {
		return RestoreResult{}, ErrRestoreVerification
	}
	if options.VerifyDatabase != nil {
		if err := options.VerifyDatabase(ctx, options.Target); err != nil {
			return RestoreResult{}, ErrRestoreVerification
		}
	}
	if err := syncDirectory(filepath.Dir(options.Target)); err != nil {
		return RestoreResult{}, ErrRestoreFailed
	}
	result := restoreResult(manifest, finalSHA, finalSize, version, options, restoredArtifacts(plans))
	committed = nil
	return result, nil
}

func validateIndependentTarget(target, root string, database bool) error {
	if !filepath.IsAbs(target) || !filepath.IsAbs(root) || !within(target, root) || containsUserDataMarker(target) || containsUserDataMarker(root) {
		return ErrRestoreUnsafe
	}
	if database && unsafeDatabaseName(target) {
		return ErrRestoreUnsafe
	}
	resolvedRoot, err := secureDirectory(root)
	if err != nil || !samePath(resolvedRoot, root) {
		return ErrRestoreUnsafe
	}
	parent := filepath.Dir(target)
	resolvedParent, err := secureDirectory(parent)
	if err != nil || !samePath(resolvedParent, parent) || !withinOrEqual(resolvedParent, resolvedRoot) {
		return ErrRestoreUnsafe
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		return ErrRestoreUnsafe
	}
	return nil
}

type artifactPlan struct {
	artifact     BackupArtifact
	target       string
	staging      string
	directory    bool
	stagingOwned bool
}

func stageIndependentArtifacts(ctx context.Context, options RestoreOptions, manifest BackupManifest) ([]artifactPlan, error) {
	artifacts := make([]BackupArtifact, 0)
	for _, artifact := range manifest.Artifacts {
		if artifact.Role != "database" {
			artifacts = append(artifacts, artifact)
		}
	}
	if len(artifacts) == 0 {
		if len(options.ArtifactTargets) != 0 {
			return nil, ErrRestoreUnsafe
		}
		return nil, nil
	}
	targets := make(map[string]string, len(options.ArtifactTargets))
	for _, target := range options.ArtifactTargets {
		if !validArtifactRole(target.Role) || target.Target == "" || targets[target.Role] != "" {
			return nil, ErrRestoreUnsafe
		}
		targets[target.Role] = target.Target
	}
	if len(targets) != len(artifacts) {
		return nil, ErrRestoreUnsafe
	}
	plans := make([]artifactPlan, 0, len(artifacts))
	for _, artifact := range artifacts {
		target, exists := targets[artifact.Role]
		if !exists || !filepath.IsAbs(target) || samePath(target, options.Target) ||
			withinOrEqual(target, options.BackupRoot) || withinOrEqual(options.BackupRoot, target) ||
			validateIndependentTarget(target, options.AllowedTargetRoot, false) != nil {
			return nil, ErrRestoreUnsafe
		}
		for _, previous := range plans {
			if samePath(target, previous.target) || withinOrEqual(target, previous.target) || withinOrEqual(previous.target, target) {
				return nil, ErrRestoreUnsafe
			}
		}
		directory := strings.HasSuffix(strings.ToLower(artifact.File), ".tar")
		staging := target + ".restore." + options.RunID + ".tmp"
		if _, err := os.Lstat(staging); err == nil || !os.IsNotExist(err) {
			return nil, ErrRestoreUnsafe
		}
		plan := artifactPlan{artifact: artifact, target: target, staging: staging, directory: directory, stagingOwned: true}
		artifactPath := filepath.Join(options.BackupRoot, artifact.File)
		if err := verifyRestoreArtifact(ctx, artifactPath, artifact); err != nil {
			cleanupArtifactStages(plans)
			return nil, err
		}
		if directory {
			if err := extractResourceArchive(ctx, artifactPath, staging); err != nil {
				cleanupArtifactStages(plans)
				return nil, err
			}
			if err := verifyRestoreArtifact(ctx, artifactPath, artifact); err != nil {
				_ = os.RemoveAll(staging)
				cleanupArtifactStages(plans)
				return nil, err
			}
		} else {
			copied, err := exclusiveCopy(ctx, artifactPath, staging, "restore-resource")
			if err != nil || copied.SHA256 != artifact.SHA256 || copied.Size != artifact.Size {
				cleanupArtifactStages(plans)
				return nil, ErrRestoreVerification
			}
		}
		plans = append(plans, plan)
	}
	return plans, nil
}

func verifyRestoreArtifact(ctx context.Context, path string, artifact BackupArtifact) error {
	digest, size, err := hashRegularFile(ctx, path)
	if err != nil || digest != artifact.SHA256 || size != artifact.Size {
		return ErrRestoreVerification
	}
	return nil
}

func cleanupArtifactStages(plans []artifactPlan) {
	for index := len(plans) - 1; index >= 0; index-- {
		if plans[index].stagingOwned {
			_ = os.RemoveAll(plans[index].staging)
		}
	}
}

func extractResourceArchive(ctx context.Context, archivePath, destination string) error {
	input, err := os.Open(archivePath)
	if err != nil {
		return ErrRestoreFailed
	}
	defer input.Close()
	if err := os.Mkdir(destination, 0o700); err != nil {
		return ErrRestoreFailed
	}
	owned := true
	defer func() {
		if owned {
			_ = os.RemoveAll(destination)
		}
	}()
	reader := tar.NewReader(input)
	root := ""
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return ErrRestoreVerification
		}
		name := filepath.ToSlash(header.Name)
		parts := strings.Split(name, "/")
		if len(parts) == 0 || parts[0] == "" || parts[0] == "." || parts[0] == ".." {
			return ErrRestoreVerification
		}
		if root == "" {
			root = parts[0]
		}
		if parts[0] != root {
			return ErrRestoreVerification
		}
		relative := filepath.Join(parts[1:]...)
		if relative == "." || relative == "" {
			if header.Typeflag != tar.TypeDir {
				return ErrRestoreVerification
			}
			continue
		}
		output := filepath.Join(destination, relative)
		if !within(output, destination) || header.Size < 0 {
			return ErrRestoreVerification
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(output, 0o700); err != nil {
				return ErrRestoreFailed
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(output), 0o700); err != nil {
				return ErrRestoreFailed
			}
			file, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return ErrRestoreVerification
			}
			written, copyErr := copyWithContext(ctx, file, reader)
			syncErr := file.Sync()
			closeErr := file.Close()
			if copyErr != nil || syncErr != nil || closeErr != nil || written != header.Size {
				return ErrRestoreVerification
			}
		default:
			return ErrRestoreVerification
		}
	}
	if root == "" {
		return ErrRestoreVerification
	}
	owned = false
	return nil
}

func manifestArtifact(manifest BackupManifest, role string) (BackupArtifact, bool) {
	for _, artifact := range manifest.Artifacts {
		if artifact.Role == role {
			return artifact, true
		}
	}
	return BackupArtifact{}, false
}

func restoredArtifacts(plans []artifactPlan) []RestoredArtifact {
	result := make([]RestoredArtifact, 0, len(plans))
	for _, plan := range plans {
		result = append(result, RestoredArtifact{Role: plan.artifact.Role, SHA256: plan.artifact.SHA256, Size: plan.artifact.Size})
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Role < result[right].Role })
	return result
}

func restoreResult(manifest BackupManifest, sha string, size int64, version int, options RestoreOptions, artifacts []RestoredArtifact) RestoreResult {
	result := RestoreResult{ManifestID: manifest.ManifestID, SHA256: sha, Size: size, UserVersion: version, Mode: options.Mode, Artifacts: artifacts}
	if options.Mode == RestoreModeTruncatingReplace {
		result.TruncationPoint = options.Truncation.Point
		result.AffectedMutationCount = len(options.Truncation.AffectedMutations)
		hasher := sha256.New()
		for _, mutation := range options.Truncation.AffectedMutations {
			_, _ = io.WriteString(hasher, mutation+"\x00")
		}
		result.AffectedMutationSHA256 = hex.EncodeToString(hasher.Sum(nil))
	}
	return result
}
