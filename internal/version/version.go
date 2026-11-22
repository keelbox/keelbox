// Package version carries build metadata, set at link time:
//
//	go build -ldflags "-X github.com/keelbox/keelbox/internal/version.Version=v0.1.0 ..."
//
// scripts/build.sh and .goreleaser.yaml set all three.
package version

// Build metadata. Commit and Date are empty in a plain go build.
var (
	Version = "dev"
	Commit  = ""
	Date    = "" // commit time, RFC 3339, so rebuilds of a commit are identical
)

// String is the one-line build description: version, short commit and date.
func String() string {
	s := Version
	if Commit != "" {
		c := Commit
		if len(c) > 12 {
			c = c[:12]
		}
		s += " " + c
	}
	if len(Date) >= 10 {
		s += " " + Date[:10]
	}
	return s
}
