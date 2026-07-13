package maintenance

import "context"

// SmokeResult contains no marker payloads or record identifiers.  A smoke
// implementation must create its marker in a rollback transaction and verify
// it through the Go application boundary, authenticated REST, and a snapshot.
type SmokeResult struct {
	ApplicationReadWrite bool `json:"application_read_write"`
	RESTReadWrite        bool `json:"rest_read_write"`
	SnapshotVisible      bool `json:"snapshot_visible"`
	MarkerRolledBack     bool `json:"marker_rolled_back"`
	NoDuplicateEvents    bool `json:"no_duplicate_events"`
}

func (result SmokeResult) Passed() bool {
	return result.ApplicationReadWrite && result.RESTReadWrite && result.SnapshotVisible &&
		result.MarkerRolledBack && result.NoDuplicateEvents
}

type Smoker interface {
	Smoke(context.Context) (SmokeResult, error)
}
