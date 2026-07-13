// Package prerequisite verifies immutable P00 and P01 completion evidence
// before the sidecar initializes any runtime or persistence dependency.
package prerequisite

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

const (
	p00Evidence = "docs/migration/p00/evidence/runs"
	p01Evidence = "docs/migration/p01/evidence/runs"
)

var p00RequiredSources = []string{
	"docs/migration/p00/capability-matrix.json",
	"docs/migration/p00/contract-baseline.json",
}

var p01RequiredSources = []string{
	"scripts/migration-baseline/inventory-ipc.js",
	"scripts/migration-p01/check-renderer-boundary.js",
	"src/renderer/lib/api/client.ts",
	"src/renderer/lib/api/provider.tsx",
	"src/renderer/lib/api/transport.ts",
	"src/renderer/lib/desktop/bridge.ts",
}

// Report contains stable, non-sensitive failure codes suitable for startup
// output. It intentionally excludes filesystem paths and underlying errors.
type Report struct {
	OK      bool     `json:"ok"`
	Reasons []string `json:"reasons,omitempty"`
}

type fileRecord struct {
	Path    string `json:"path"`
	SHA256  string `json:"sha256"`
	Missing bool   `json:"missing"`
}

type commandEvaluation struct {
	Accepted bool `json:"accepted"`
}

type commandResult struct {
	ID         string            `json:"id"`
	Evaluation commandEvaluation `json:"evaluation"`
}

type evidenceCompleteness struct {
	Complete bool `json:"complete"`
}

type summary struct {
	SchemaVersion          int                  `json:"schemaVersion"`
	Status                 string               `json:"status"`
	OK                     bool                 `json:"ok"`
	SourceHashesStable     bool                 `json:"sourceHashesStable"`
	ExpectationsHashStable bool                 `json:"expectationsHashStable"`
	EvidenceCompleteness   evidenceCompleteness `json:"evidenceCompleteness"`
	SourceHashesEnd        []fileRecord         `json:"sourceHashesEnd"`
	CommandResults         []commandResult      `json:"commandResults"`
}

type manifest struct {
	SchemaVersion         int  `json:"schemaVersion"`
	ImmutableRunDirectory bool `json:"immutableRunDirectory"`
	Artifacts             []struct {
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
	} `json:"artifacts"`
}

// Check verifies the latest P00 and P01 evidence and current guarded sources.
func Check(repositoryRoot string) Report {
	reasons := make([]string, 0, 2)
	if err := checkP00(repositoryRoot); err != nil {
		reasons = append(reasons, "p00_"+reasonCode(err))
	}
	if err := checkP01(repositoryRoot); err != nil {
		reasons = append(reasons, "p01_"+reasonCode(err))
	}
	return Report{OK: len(reasons) == 0, Reasons: reasons}
}

var (
	errEvidenceMissing   = errors.New("evidence_missing")
	errEvidenceInvalid   = errors.New("evidence_invalid")
	errEvidenceFailed    = errors.New("evidence_failed")
	errSourceDrift       = errors.New("source_drift")
	errBoundaryUnchecked = errors.New("boundary_unchecked")
)

func reasonCode(err error) string {
	switch {
	case errors.Is(err, errEvidenceMissing):
		return "evidence_missing"
	case errors.Is(err, errEvidenceFailed):
		return "gate_failed"
	case errors.Is(err, errSourceDrift):
		return "source_drift"
	case errors.Is(err, errBoundaryUnchecked):
		return "boundary_unchecked"
	default:
		return "evidence_invalid"
	}
}

func checkP00(root string) error {
	run, sum, err := loadLatest(root, p00Evidence)
	if err != nil {
		return err
	}
	if !sum.OK || !sum.SourceHashesStable || !sum.ExpectationsHashStable || !sum.EvidenceCompleteness.Complete {
		return errEvidenceFailed
	}
	if err := verifyManifest(run); err != nil {
		return err
	}
	return verifySources(root, sum.SourceHashesEnd, p00RequiredSources)
}

func checkP01(root string) error {
	run, sum, err := loadLatest(root, p01Evidence)
	if err != nil {
		return err
	}
	if !sum.OK || sum.Status != "completed" || !sum.SourceHashesStable {
		return errEvidenceFailed
	}
	if err := verifyManifest(run); err != nil {
		return err
	}
	for _, id := range []string{"p00-gate", "inventory", "renderer-boundary"} {
		if !accepted(sum.CommandResults, id) {
			return errBoundaryUnchecked
		}
	}
	return verifySources(root, sum.SourceHashesEnd, p01RequiredSources)
}

func loadLatest(root, relativeEvidence string) (string, summary, error) {
	evidenceRoot := filepath.Join(root, filepath.FromSlash(relativeEvidence))
	entries, err := os.ReadDir(evidenceRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return "", summary{}, errEvidenceMissing
		}
		return "", summary{}, errEvidenceInvalid
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	if len(names) == 0 {
		return "", summary{}, errEvidenceMissing
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	run := filepath.Join(evidenceRoot, names[0])
	content, err := os.ReadFile(filepath.Join(run, "summary.json"))
	if err != nil {
		return "", summary{}, errEvidenceInvalid
	}
	var sum summary
	if err := json.Unmarshal(content, &sum); err != nil || sum.SchemaVersion != 1 {
		return "", summary{}, errEvidenceInvalid
	}
	return run, sum, nil
}

func verifyManifest(run string) error {
	content, err := os.ReadFile(filepath.Join(run, "evidence-manifest.json"))
	if err != nil {
		return errEvidenceInvalid
	}
	var value manifest
	if err := json.Unmarshal(content, &value); err != nil || value.SchemaVersion != 1 || !value.ImmutableRunDirectory {
		return errEvidenceInvalid
	}
	for _, artifact := range value.Artifacts {
		if artifact.Path == "summary.json" {
			actual, err := fileSHA256(filepath.Join(run, "summary.json"))
			if err != nil || actual != artifact.SHA256 {
				return errEvidenceInvalid
			}
			return nil
		}
	}
	return errEvidenceInvalid
}

func verifySources(root string, records []fileRecord, required []string) error {
	byPath := make(map[string]fileRecord, len(records))
	for _, record := range records {
		byPath[filepath.ToSlash(record.Path)] = record
	}
	for _, name := range required {
		record, ok := byPath[name]
		if !ok || record.Missing || record.SHA256 == "" {
			return errEvidenceInvalid
		}
		actual, err := fileSHA256(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil || actual != record.SHA256 {
			return errSourceDrift
		}
	}
	return nil
}

func accepted(results []commandResult, id string) bool {
	for _, result := range results {
		if result.ID == id {
			return result.Evaluation.Accepted
		}
	}
	return false
}

func fileSHA256(name string) (string, error) {
	content, err := os.ReadFile(name)
	if err != nil {
		return "", fmt.Errorf("hash source: %w", err)
	}
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:]), nil
}
