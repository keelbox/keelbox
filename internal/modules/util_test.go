package modules

import (
	"strings"
	"testing"
)

// A download is tried again after any failure, a connection reset in the
// middle of it included, which curl's --retry alone does not cover.
func TestDownloadsRetryAfterAReset(t *testing.T) {
	args := strings.Join(curl("https://example.com/x", "/tmp/x").Args, " ")
	if !strings.Contains(args, "--retry 3 --retry-all-errors") || !strings.Contains(args, "--proto =https") {
		t.Errorf("curl %s", args)
	}
}
