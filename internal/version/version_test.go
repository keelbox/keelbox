package version

import "testing"

func TestString(t *testing.T) {
	defer func(v, c, d string) { Version, Commit, Date = v, c, d }(Version, Commit, Date)
	Version, Commit, Date = "dev", "", ""
	if got := String(); got != "dev" {
		t.Errorf("plain build: %q", got)
	}
	Version, Commit, Date = "v0.1.0", "0123456789abcdef0123", "2026-11-06T12:00:00Z"
	if got, want := String(), "v0.1.0 0123456789ab 2026-11-06"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
