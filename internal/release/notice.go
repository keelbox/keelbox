package release

import (
	"regexp"
	"runtime"
	"strings"
	"unicode"

	"github.com/keelbox/keelbox/internal/version"
)

// UserAgent is what keelbox sends to its release hosts: its version and
// platform, and nothing about the person or their boxes. The hosts' logs
// count installs by version and platform from it.
func UserAgent() string {
	v := strings.Map(func(r rune) rune {
		if r > unicode.MaxASCII || !unicode.IsPrint(r) || r == ' ' {
			return -1
		}
		return r
	}, strings.TrimPrefix(version.Version, "v"))
	if v == "" {
		v = "dev"
	}
	return "keelbox/" + v + " (" + runtime.GOOS + "/" + runtime.GOARCH + ")"
}

// SecurityNotice is a security problem the channel index tells clients
// about: the releases it affects and the one that fixes it. The index is
// not signed as the binaries are, so a client shows only a short line of
// plain text and a link to keelbox's own pages, never a command from the
// index (Valid); the command to update is keelbox's own.
type SecurityNotice struct {
	// Since is the first release affected; empty means every release
	// before Fixed.
	Since   string `json:"since,omitempty"`
	Fixed   string `json:"fixed"`   // vX.Y.Z, the release with the fix
	Summary string `json:"summary"` // one line of plain text
	URL     string `json:"url"`     // the advisory, on keelbox.dev or github.com/keelbox
}

// maxSummary bounds a notice's summary, so one fits in a line or two.
const maxSummary = 200

// noticeURL is the only shape of link a notice may carry: keelbox.dev, or
// keelbox's own organisation on GitHub, with a plain path. No "..", no
// percent-encoding, query, fragment or user part, so what is shown is
// where a browser goes.
var noticeURL = regexp.MustCompile(`^https://(keelbox\.dev|github\.com/keelbox)/[A-Za-z0-9_-]+(/[A-Za-z0-9_-]+(\.[A-Za-z0-9]+)?)*/?$`)

// Valid reports whether a client may show the notice: release versions, a
// short printable summary and a link to keelbox's own pages.
func (n SecurityNotice) Valid() bool {
	if !strings.HasPrefix(n.Fixed, "v") || !IsRelease(n.Fixed) {
		return false
	}
	if n.Since != "" && (!strings.HasPrefix(n.Since, "v") || !IsRelease(n.Since) || CompareVersions(n.Since, n.Fixed) >= 0) {
		return false
	}
	if n.Summary == "" || len(n.Summary) > maxSummary || strings.ContainsFunc(n.Summary, func(r rune) bool { return !unicode.IsPrint(r) }) {
		return false
	}
	return len(n.URL) <= 200 && noticeURL.MatchString(n.URL)
}

// Affects reports whether release v has the problem: from Since, up to
// but not including Fixed. A development build is never affected, since
// nothing says which release it is.
func (n SecurityNotice) Affects(v string) bool {
	if !IsRelease(v) {
		return false
	}
	v = "v" + strings.TrimPrefix(v, "v")
	return CompareVersions(v, n.Fixed) < 0 && (n.Since == "" || CompareVersions(v, n.Since) >= 0)
}

// Notices are the index's security notices a client may show.
func (idx *Index) Notices() []SecurityNotice {
	var out []SecurityNotice
	for _, n := range idx.Security {
		if n.Valid() {
			out = append(out, n)
		}
	}
	return out
}
