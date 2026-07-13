package httpapi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestP13BCrossTransportFixtureIsAuthorizedAndSynthetic(t *testing.T) {
	root := filepath.Join("..", "..", "..", "fixtures", "migration", "p13", "mcp")
	marker, err := os.ReadFile(filepath.Join(root, ".autoplan-p13b-authorized-mcp-copy"))
	if err != nil || len(marker) == 0 {
		t.Fatal("P13B fixture marker is unavailable")
	}
	content, err := os.ReadFile(filepath.Join(root, "p13b-fixture-manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		SchemaVersion int    `json:"schema_version"`
		Kind          string `json:"kind"`
		Authorized    bool   `json:"authorized_copy"`
		RealUserData  bool   `json:"contains_real_userdata"`
	}
	if err := json.Unmarshal(content, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.SchemaVersion != 1 || manifest.Kind != "p13b-authorized-mcp-fixture" || !manifest.Authorized || manifest.RealUserData {
		t.Fatalf("invalid P13B fixture manifest: %#v", manifest)
	}
	cases, err := os.ReadFile(filepath.Join(root, "cross-transport-cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		SchemaVersion int  `json:"schema_version"`
		Synthetic     bool `json:"synthetic"`
		Cases         []struct {
			ID         string   `json:"id"`
			Transports []string `json:"transports"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(cases, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.SchemaVersion != 1 || !fixture.Synthetic || len(fixture.Cases) < 4 {
		t.Fatal("P13B cross-transport fixture is incomplete")
	}
	seen := make(map[string]struct{}, len(fixture.Cases))
	for _, item := range fixture.Cases {
		if item.ID == "" || len(item.Transports) != 2 || item.Transports[0] != "http" || item.Transports[1] != "stdio" {
			t.Fatalf("invalid fixture case: %#v", item)
		}
		if _, duplicate := seen[item.ID]; duplicate {
			t.Fatalf("duplicate fixture case %q", item.ID)
		}
		seen[item.ID] = struct{}{}
	}
}
