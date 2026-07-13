package process

import (
	"strings"
	"testing"
)

func TestP12OutputRedactsSplitSecretsAndWorkspacePaths(t *testing.T) {
	const secret = "p12-fixture-secret-value"
	const workspace = "C:\\p12-fixture\\workspace"
	logPath := workspace + "\\logs\\fixture.log"
	redactor := NewRedactor(map[string]string{"FIXTURE_TOKEN": secret}, logPath)
	collector := newRedactedOutputCollector(OutputStdout, 512, 10, 256, 8, newOutputBudget(1024, 20), redactor, &outputSequencer{})
	_, _ = collector.Write([]byte("report " + secret[:10]))
	_, _ = collector.Write([]byte(secret[10:] + " " + logPath + "\n"))
	output := collector.finalize(redactor)
	if output.RedactionFailed || strings.Contains(output.Tail, secret) || strings.Contains(output.Tail, workspace) || strings.Contains(output.Tail, "fixture.log") {
		t.Fatalf("unsafe process tail: %#v", output)
	}
	if !strings.Contains(output.Tail, "<redacted") || !strings.Contains(output.Tail, "<path>") {
		t.Fatalf("expected stable redaction markers: %#v", output)
	}
}

func TestP12OutputDiscardsInvalidUTF8InsteadOfPersistingIt(t *testing.T) {
	redactor := NewRedactor(nil)
	collector := newRedactedOutputCollector(OutputStderr, 64, 4, 64, 16, newOutputBudget(64, 4), redactor, &outputSequencer{})
	_, _ = collector.Write([]byte{0xff, 0xfe})
	output := collector.finalize(redactor)
	if !output.RedactionFailed || !output.Truncated || output.Tail != "" {
		t.Fatalf("invalid UTF-8 was retained: %#v", output)
	}
}
