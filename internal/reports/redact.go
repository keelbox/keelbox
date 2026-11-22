package reports

import (
	"net/netip"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// The agent's text is untrusted: a repository's instructions can make an
// agent file a report that carries a secret, hoping the owner posts it.
// Before --send shows the issue, Redact replaces what should not reach a
// public issue; the owner then reads the whole text, may edit it, and says
// yes or no (ADR 0062).

// Redaction counts what one kind of pattern replaced.
type Redaction struct {
	Kind  string
	Count int
}

type rule struct {
	kind string
	re   *regexp.Regexp
	with string
}

var tokenRules = []rule{
	{"private key", regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?(?:-----END [A-Z0-9 ]*PRIVATE KEY-----|\z)`), "<private-key>"},
	{"GitHub token", regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,})`), "<github-token>"},
	{"API key", regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{16,}`), "<api-key>"},
	{"Slack token", regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9-]{10,}`), "<slack-token>"},
	{"AWS key", regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`), "<aws-key>"},
	{"AWS key", regexp.MustCompile(`(?i)(aws_secret_access_key\s*[=:]\s*)\S+`), "${1}<aws-secret>"},
	{"Tailscale key", regexp.MustCompile(`\btskey-[A-Za-z0-9-]{10,}`), "<tailscale-key>"},
	{"JWT", regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`), "<jwt>"},
	{"email address", regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`), "<email>"},
	{"tailnet name", regexp.MustCompile(`\b(?:[A-Za-z0-9-]+\.)+ts\.net\b`), "<tailnet-name>"},
}

var (
	ipv4Re    = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
	ipv6Re    = regexp.MustCompile(`\b[0-9A-Fa-f]{0,4}(?::[0-9A-Fa-f]{0,4}){2,7}\b|::[0-9A-Fa-f]{1,4}\b`)
	homeRe    = regexp.MustCompile(`(?:/home/[A-Za-z0-9._-]+|/root)\b`)
	projectRe = regexp.MustCompile(`~/projects/([A-Za-z0-9._-]+)((?:/[A-Za-z0-9._@+-]+)*)`)
	tailnet4  = netip.MustParsePrefix("100.64.0.0/10")
	tailnet6  = netip.MustParsePrefix("fd7a:115c:a1e0::/48")
)

// Projects are the project names under ~/projects the text names, after
// home paths are folded to ~, sorted.
func Projects(text string) []string {
	var out []string
	for _, m := range projectRe.FindAllStringSubmatch(homeRe.ReplaceAllString(text, "~"), -1) {
		if !slices.Contains(out, m[1]) {
			out = append(out, m[1])
		}
	}
	sort.Strings(out)
	return out
}

// Redact replaces tokens, private keys, email addresses, tailnet names and
// addresses, public IP addresses, the box name and home paths in text, and
// project names and the paths under them unless keep names them. It returns
// the text and what it replaced, by kind.
func Redact(text, box string, keep []string) (string, []Redaction) {
	counts := map[string]int{}
	var order []string
	count := func(kind string, n int) {
		if n == 0 {
			return
		}
		if counts[kind] == 0 {
			order = append(order, kind)
		}
		counts[kind] += n
	}
	for _, r := range tokenRules {
		count(r.kind, len(r.re.FindAllStringIndex(text, -1)))
		text = r.re.ReplaceAllString(text, r.with)
	}
	text = ipv4Re.ReplaceAllStringFunc(text, func(s string) string {
		a, err := netip.ParseAddr(s)
		switch {
		case err != nil:
			return s
		case tailnet4.Contains(a):
			count("tailnet address", 1)
			return "<tailnet-ip>"
		case isPublic(a):
			count("public IP address", 1)
			return "<public-ip>"
		}
		return s
	})
	text = ipv6Re.ReplaceAllStringFunc(text, func(s string) string {
		a, err := netip.ParseAddr(s)
		switch {
		case err != nil || !strings.Contains(s, ":"):
			return s
		case tailnet6.Contains(a):
			count("tailnet address", 1)
			return "<tailnet-ip>"
		case isPublic(a):
			count("public IP address", 1)
			return "<public-ip>"
		}
		return s
	})
	count("home path", len(homeRe.FindAllStringIndex(text, -1)))
	text = homeRe.ReplaceAllString(text, "~")
	text = projectRe.ReplaceAllStringFunc(text, func(s string) string {
		m := projectRe.FindStringSubmatch(s)
		if slices.Contains(keep, m[1]) {
			return s
		}
		count("project name or path", 1)
		if m[2] != "" {
			return "~/projects/<project>/<path>"
		}
		return "~/projects/<project>"
	})
	if box != "" {
		re := regexp.MustCompile(`\b` + regexp.QuoteMeta(box) + `\b`)
		count("box name", len(re.FindAllStringIndex(text, -1)))
		text = re.ReplaceAllString(text, "<box>")
	}
	var out []Redaction
	for _, k := range order {
		out = append(out, Redaction{Kind: k, Count: counts[k]})
	}
	return text, out
}

// isPublic is any address routed on the internet, documentation ranges
// included: telling them apart is not worth a leak.
func isPublic(a netip.Addr) bool {
	return a.IsGlobalUnicast() && !a.IsPrivate()
}
