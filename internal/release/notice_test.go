package release

import (
	"bytes"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/version"
)

func TestUserAgent(t *testing.T) {
	defer func(v string) { version.Version = v }(version.Version)
	for v, want := range map[string]string{
		"v0.2.0":              `^keelbox/0\.2\.0 \([a-z0-9]+/[a-z0-9]+\)$`,
		"dev":                 `^keelbox/dev \(`,
		"v0.2.0 x\n":          `^keelbox/0\.2\.0x \(`,
		"":                    `^keelbox/dev \(`,
		"v0.2.0-rc.3-4-gabcd": `^keelbox/0\.2\.0-rc\.3-4-gabcd \(`,
	} {
		version.Version = v
		if got := UserAgent(); !regexp.MustCompile(want).MatchString(got) {
			t.Errorf("%q: %q does not match %s", v, got, want)
		}
	}
}

func TestSecurityNotices(t *testing.T) {
	ok := SecurityNotice{Since: "v0.2.0", Fixed: "v0.2.3", Summary: "A summary.", URL: "https://keelbox.dev/security/2027-01"}
	if !ok.Valid() {
		t.Fatal("a good notice is refused")
	}
	for _, u := range []string{"https://github.com/keelbox/keelbox/security/advisories/GHSA-abcd-1234", "https://keelbox.dev/security/2027-01.html"} {
		if n := (SecurityNotice{Fixed: "v0.2.3", Summary: "s", URL: u}); !n.Valid() {
			t.Errorf("refused %s", u)
		}
	}
	for name, n := range map[string]SecurityNotice{
		"fixed not a release": {Fixed: "0.2.3", Summary: "s", URL: ok.URL},
		"since after fixed":   {Since: "v0.3.0", Fixed: "v0.2.3", Summary: "s", URL: ok.URL},
		"no summary":          {Fixed: "v0.2.3", URL: ok.URL},
		"control characters":  {Fixed: "v0.2.3", Summary: "s\x1b]8;;https://x\x07", URL: ok.URL},
		"newline":             {Fixed: "v0.2.3", Summary: "one\ntwo", URL: ok.URL},
		"long summary":        {Fixed: "v0.2.3", Summary: strings.Repeat("a", 201), URL: ok.URL},
		"another host":        {Fixed: "v0.2.3", Summary: "s", URL: "https://keelbox.dev.example.com/x"},
		"plain http":          {Fixed: "v0.2.3", Summary: "s", URL: "http://keelbox.dev/x"},
		"space in url":        {Fixed: "v0.2.3", Summary: "s", URL: "https://keelbox.dev/x run this"},
		"dot dot":             {Fixed: "v0.2.3", Summary: "s", URL: "https://github.com/keelbox/../attacker/x"},
		"encoded dot dot":     {Fixed: "v0.2.3", Summary: "s", URL: "https://github.com/keelbox/%2e%2e/attacker"},
		"other github org":    {Fixed: "v0.2.3", Summary: "s", URL: "https://github.com/keelboxx/x"},
		"user part":           {Fixed: "v0.2.3", Summary: "s", URL: "https://keelbox.dev@evil.example.com/x"},
		"query":               {Fixed: "v0.2.3", Summary: "s", URL: "https://keelbox.dev/x?u=https://evil.example.com"},
		"backslash":           {Fixed: "v0.2.3", Summary: "s", URL: "https://keelbox.dev\\evil.example.com/x"},
		"bare host":           {Fixed: "v0.2.3", Summary: "s", URL: "https://keelbox.dev/"},
	} {
		if n.Valid() {
			t.Errorf("%s: accepted", name)
		}
	}
	for v, want := range map[string]bool{
		"v0.1.9": false, "v0.2.0-rc.1": false, "v0.2.0": true, "0.2.2": true, "v0.2.3-rc.1": true,
		"v0.2.3": false, "v0.3.0": false, "dev": false,
	} {
		if got := ok.Affects(v); got != want {
			t.Errorf("Affects(%s) = %v", v, got)
		}
	}
	all := SecurityNotice{Fixed: "v0.2.3", Summary: "s", URL: ok.URL}
	if !all.Affects("v0.1.0-rc.1") {
		t.Error("a notice without since misses an early release")
	}
	idx := Index{Security: []SecurityNotice{ok, {Fixed: "v1", Summary: "s", URL: ok.URL}}}
	if got := idx.Notices(); len(got) != 1 || got[0] != ok {
		t.Errorf("Notices: %+v", got)
	}
}

// TestPublishedNotices checks get/security.json, which
// scripts/channel-index.sh puts in the index: a JSON array of notices,
// every one of which clients would show.
func TestPublishedNotices(t *testing.T) {
	data, err := os.ReadFile("../../get/security.json")
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var notices []SecurityNotice
	if err := dec.Decode(&notices); err != nil {
		t.Fatalf("get/security.json: %v", err)
	}
	for _, n := range notices {
		if !n.Valid() {
			t.Errorf("get/security.json: clients would not show %+v: fixed and since are vX.Y.Z, the summary one line of at most 200 characters, the URL on https://keelbox.dev/ or https://github.com/keelbox/", n)
		}
	}
}
