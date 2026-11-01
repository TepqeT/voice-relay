package version

import (
	"regexp"
	"testing"
)

// The fork version appears in logs, diagnostics and reports: keep it "v<N>".
func TestForkFormat(t *testing.T) {
	if !regexp.MustCompile(`^v[1-9][0-9]*$`).MatchString(Fork) {
		t.Fatalf("Fork = %q, want v<number>", Fork)
	}
}
