package executors

import (
	"encoding/json"
	"testing"
)

func TestDependencyLabelsPreserveConfiguredOrder(t *testing.T) {
	labels, err := dependencyLabels(json.RawMessage(`["compile","lint","compile"]`))
	if err != nil {
		t.Fatalf("dependency labels: %v", err)
	}
	if len(labels) != 3 || labels[0] != "compile" || labels[1] != "lint" || labels[2] != "compile" {
		t.Fatalf("labels=%v", labels)
	}
}

func TestParseArgumentsKeepsEachPersistedArgumentSeparate(t *testing.T) {
	args, err := parseArguments(json.RawMessage(`["one value", {"value":"two value","quoting":"strong"}, true]`))
	if err != nil {
		t.Fatalf("arguments: %v", err)
	}
	if len(args) != 3 || args[0] != "one value" || args[1] != "two value" || args[2] != "true" {
		t.Fatalf("args=%q", args)
	}
}
