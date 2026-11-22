// Package previews publishes the agent's loopback dev servers at HTTPS URLs
// on the tailnet (docs/adr/0019-previews.md).
//
// The agent describes what it wants in a file in its own home, written by
// `keelbox expose` without sudo. A root service (keelbox-previews.service,
// started by a path unit when that file changes and by a timer) reads the
// file as the agent, validates every entry, and renders a Caddy JSON config
// in which the agent's input is reduced to a name and two port numbers.
// Caddy runs as its own system user with its admin API off, listens only on
// the box's tailnet addresses, and serves the box's Tailscale certificate.
package previews

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Paths and units on the box.
const (
	// AgentFile is the agent's previews file, relative to its home.
	AgentFile   = ".config/keelbox/previews.json"
	Dir         = "/var/lib/keelbox/previews"
	StatusPath  = Dir + "/status.json"
	CaddyConfig = Dir + "/caddy.json"
	TLSDir      = Dir + "/tls"
	CertPath    = TLSDir + "/cert.pem"
	KeyPath     = TLSDir + "/key.pem"
	// StateDir is Caddy's own storage, owned by ServiceUser.
	StateDir    = "/var/lib/keelbox-preview"
	ServiceUser = "keelbox-preview"
	CaddyBin    = "/usr/local/bin/caddy"
	CaddyUnit   = "keelbox-preview-caddy.service"
	SyncUnit    = "keelbox-previews.service"
	PathUnit    = "keelbox-previews.path"
	TimerUnit   = "keelbox-previews.timer"
	// Max is the most previews one agent can publish.
	Max = 100
	// maxFile caps the agent's file; anything larger is refused unread.
	maxFile = 64 << 10
)

// Preview is one published dev server: the agent's loopback port, served
// at https://<box's tailnet name>:<HTTPSPort>/.
type Preview struct {
	Name      string `json:"name"`
	Port      int    `json:"port"`
	HTTPSPort int    `json:"https_port"`
}

// File is the agent's previews file.
type File struct {
	Version  int       `json:"version"`
	Previews []Preview `json:"previews"`
}

var nameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`)

// ValidName reports whether a preview name is allowed.
func ValidName(s string) bool { return nameRe.MatchString(s) }

// ParseFile decodes the agent's file and keeps the entries that pass every
// rule; each one that does not is described in problems. Nothing in the file
// reaches the Caddy config except a validated name and two port numbers.
//
// skip are ports in lo-hi previews do not use (Config.PreviewSkipped).
func ParseFile(data []byte, lo, hi int, skip ...int) ([]Preview, []string) {
	if len(data) == 0 {
		return nil, nil
	}
	if len(data) > maxFile {
		return nil, []string{fmt.Sprintf("previews file is larger than %d bytes; ignored", maxFile)}
	}
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, []string{"previews file is not valid JSON: " + err.Error()}
	}
	if f.Version != 1 {
		return nil, []string{fmt.Sprintf("previews file version %d is not 1; ignored", f.Version)}
	}
	var out []Preview
	var problems []string
	names, https := map[string]bool{}, map[int]bool{}
	for i, p := range f.Previews {
		bad := func(format string, args ...any) {
			problems = append(problems, fmt.Sprintf("preview %d (%q): ", i+1, p.Name)+fmt.Sprintf(format, args...))
		}
		switch {
		case len(out) >= Max:
			bad("more than %d previews", Max)
		case !ValidName(p.Name):
			bad("name must be lowercase letters, digits and hyphens, at most 32 characters")
		case names[p.Name]:
			bad("name listed twice")
		case p.Port < 1 || p.Port > 65535:
			bad("port %d is out of range", p.Port)
		case p.HTTPSPort < lo || p.HTTPSPort > hi:
			bad("https port %d is outside previews.ports %d-%d", p.HTTPSPort, lo, hi)
		case slices.Contains(skip, p.HTTPSPort):
			bad("https port %d is kept open to the LAN by host.firewall.allow, so previews skip it; keelbox expose %d --name %s picks another", p.HTTPSPort, p.Port, p.Name)
		case https[p.HTTPSPort]:
			bad("https port %d is used twice", p.HTTPSPort)
		default:
			names[p.Name], https[p.HTTPSPort] = true, true
			out = append(out, Preview{Name: p.Name, Port: p.Port, HTTPSPort: p.HTTPSPort})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].HTTPSPort < out[j].HTTPSPort })
	return out, problems
}

// Marshal renders the file as the agent's CLI writes it.
func (f File) Marshal() []byte {
	f.Version = 1
	if f.Previews == nil {
		f.Previews = []Preview{}
	}
	b, _ := json.MarshalIndent(f, "", "  ")
	return append(b, '\n')
}

// Hash identifies the content of the agent's file, so `keelbox expose` can
// tell when the root service has processed its change.
func Hash(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	s := sha256.Sum256(data)
	return hex.EncodeToString(s[:])
}

// Tailnet is what the box's tailscaled reports.
type Tailnet struct {
	Connected bool     `json:"connected"`
	Name      string   `json:"name,omitempty"` // MagicDNS name, no trailing dot
	IPs       []string `json:"ips,omitempty"`
	// HTTPS is set when the tailnet issues certificates for Name (HTTPS
	// certificates are on in the Tailscale admin console).
	HTTPS bool `json:"https"`
}

// ParseTailscaleStatus reads `tailscale status --json`.
func ParseTailscaleStatus(out string) Tailnet {
	var st struct {
		BackendState string
		CertDomains  []string
		Self         struct {
			DNSName      string
			TailscaleIPs []string
		}
	}
	if json.Unmarshal([]byte(out), &st) != nil || st.BackendState != "Running" {
		return Tailnet{}
	}
	t := Tailnet{Connected: true, Name: strings.TrimSuffix(st.Self.DNSName, ".")}
	for _, ip := range st.Self.TailscaleIPs {
		if a, err := netip.ParseAddr(ip); err == nil && InTailnet(a) {
			t.IPs = append(t.IPs, a.String())
		}
	}
	for _, d := range st.CertDomains {
		if d == t.Name && t.Name != "" {
			t.HTTPS = true
		}
	}
	return t
}

var (
	cgnat     = netip.MustParsePrefix("100.64.0.0/10")
	tailnetV6 = netip.MustParsePrefix("fd7a:115c:a1e0::/48")
)

// InTailnet reports whether an address is in Tailscale's ranges.
func InTailnet(a netip.Addr) bool {
	a = a.Unmap()
	return cgnat.Contains(a) || tailnetV6.Contains(a)
}

// URL is a preview's address.
func URL(t Tailnet, p Preview) string {
	return fmt.Sprintf("https://%s:%d/", t.Name, p.HTTPSPort)
}

// Published is one preview as the service last served it.
type Published struct {
	Preview
	URL string `json:"url,omitempty"`
	// Listening reports whether something listened on the loopback port
	// when the service last ran.
	Listening bool `json:"listening"`
}

// Status is what the root service last did, readable by every account so
// the agent's `keelbox expose` can report it.
type Status struct {
	Agent string `json:"agent"`
	Ports [2]int `json:"ports"`
	// Skip are the ports in Ports that previews do not use: kept open to
	// the LAN by host.firewall.allow.
	Skip     []int       `json:"skip,omitempty"`
	Tailnet  Tailnet     `json:"tailnet"`
	Previews []Published `json:"previews"`
	// Problems are entries of the agent's file that were refused.
	Problems []string `json:"problems,omitempty"`
	// Certificate is set once the box has its Tailscale certificate: the
	// first one can take Tailscale a minute to issue.
	Certificate bool `json:"certificate,omitempty"`
	// Error says why nothing is served, when nothing is.
	Error string `json:"error,omitempty"`
	// Render is the RenderCaddy version that wrote Caddy's config; apply
	// syncs again when it is older than this keelbox's.
	Render  int       `json:"render,omitempty"`
	Source  string    `json:"source"` // Hash of the agent's file as processed
	Updated time.Time `json:"updated"`
}

// ParseStatus decodes the status file.
func ParseStatus(data []byte) (Status, error) {
	var s Status
	err := json.Unmarshal(data, &s)
	return s, err
}

// NotOnTailnet explains why previews need the tailnet.
const NotOnTailnet = "this box is not connected to a tailnet. Previews are served only on the tailnet, " +
	"at the box's MagicDNS name with its Tailscale HTTPS certificate, and never on a public address. " +
	"The admin joins the box with keelbox login (or sudo tailscale up)"

// NoHTTPS explains how to turn certificates on.
const NoHTTPS = "the tailnet does not issue HTTPS certificates for this box. Turn on MagicDNS and HTTPS Certificates " +
	"in the Tailscale admin console (DNS page); previews start within ten minutes, or at the next keelbox expose"

// Render numbers RenderCaddy's output. Raise it whenever the config it
// writes for the same previews changes, so the apply that installs the new
// keelbox also rewrites what Caddy serves (0, the config without a render
// number, sent the tailnet Host upstream and dialled localhost).
const Render = 1

// RenderCaddy builds Caddy's JSON config: one server per preview, listening
// on the tailnet addresses only, TLS from the Tailscale certificate, a
// reverse proxy to the loopback port with the upstream's own Host, and the
// admin API off so no local
// process can change it.
func RenderCaddy(t Tailnet, ps []Preview) ([]byte, error) {
	if len(t.IPs) == 0 {
		return nil, fmt.Errorf("no tailnet address to listen on")
	}
	type obj = map[string]any
	servers := obj{}
	for _, p := range ps {
		var listen []string
		for _, ip := range t.IPs {
			a, err := netip.ParseAddr(ip)
			if err != nil || !InTailnet(a) {
				return nil, fmt.Errorf("refusing to listen on %q: not a tailnet address", ip)
			}
			listen = append(listen, netip.AddrPortFrom(a, uint16(p.HTTPSPort)).String()) //nolint:gosec // G115: ParseFile keeps ports in 1-65535
		}
		port := strconv.Itoa(p.Port)
		upstream := "localhost:" + port
		servers["preview-"+p.Name] = obj{
			"listen":                  listen,
			"automatic_https":         obj{"disable": true},
			"tls_connection_policies": []any{obj{"certificate_selection": obj{"any_tag": []string{"keelbox-tailnet"}}}},
			"routes": []any{obj{"handle": []any{obj{
				"handler": "reverse_proxy",
				// Ubuntu's /etc/hosts maps localhost to 127.0.0.1 only, and
				// some dev servers listen on [::1] only: dial 127.0.0.1, and
				// on a refused connection [::1]. A refusal marks that
				// address down for a second, so a server on [::1] costs
				// one refused dial a second, on loopback.
				"upstreams": []any{obj{"dial": "127.0.0.1:" + port}, obj{"dial": "[::1]:" + port}},
				"load_balancing": obj{
					"selection_policy": obj{"policy": "first"},
					"try_duration":     "2s",
					"try_interval":     "100ms",
				},
				"health_checks": obj{"passive": obj{"fail_duration": "1s", "max_fails": 1}},
				// Dev servers refuse a Host they do not know (Vite 6 and
				// later answer 403 to the tailnet name): the upstream sees
				// its own address, and the browser's host and scheme in
				// X-Forwarded-Host and X-Forwarded-Proto.
				"headers": obj{"request": obj{"set": obj{
					"Host":              []string{upstream},
					"X-Forwarded-Host":  []string{"{http.request.host}"},
					"X-Forwarded-Proto": []string{"https"},
				}}},
			}}}},
		}
	}
	cfg := obj{
		"admin":   obj{"disabled": true, "config": obj{"persist": false}},
		"storage": obj{"module": "file_system", "root": StateDir},
		"logging": obj{"logs": obj{"default": obj{"level": "WARN"}}},
		"apps": obj{
			"tls": obj{"certificates": obj{"load_files": []any{obj{
				"certificate": CertPath, "key": KeyPath, "tags": []string{"keelbox-tailnet"},
			}}}},
			"http": obj{"servers": servers},
		},
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	return append(b, '\n'), err
}

// Listeners parses `ss -Hltn[p]` into local addresses, each with the rest of
// its line (the process, with -p).
func Listeners(ss string) map[netip.AddrPort]string {
	out := map[netip.AddrPort]string{}
	for _, l := range strings.Split(ss, "\n") {
		f := strings.Fields(l)
		if len(f) < 4 {
			continue
		}
		local := f[3]
		local = strings.Replace(local, "*:", "0.0.0.0:", 1)
		if i := strings.LastIndex(local, "%"); i >= 0 { // fe80::1%eth0:22
			if j := strings.LastIndex(local, "]"); j > i {
				local = local[:i] + local[j:]
			}
		}
		ap, err := netip.ParseAddrPort(local)
		if err != nil {
			continue
		}
		out[ap] = strings.Join(f[4:], " ")
	}
	return out
}

// LoopbackListening reports whether port answers on loopback (or on every
// address, which includes loopback).
func LoopbackListening(ls map[netip.AddrPort]string, port int) bool {
	for ap := range ls {
		if int(ap.Port()) == port && (ap.Addr().IsLoopback() || ap.Addr().IsUnspecified()) {
			return true
		}
	}
	return false
}

// Assign adds or updates a preview in f: an existing name keeps its HTTPS
// port unless previews now skip it, a new one gets the lowest free port in
// lo-hi that is not in skip.
func Assign(f File, name string, port, lo, hi int, skip ...int) (File, Preview, error) {
	if !ValidName(name) {
		return f, Preview{}, fmt.Errorf("name %q: use lowercase letters, digits and hyphens, at most 32 characters", name)
	}
	if port < 1 || port > 65535 {
		return f, Preview{}, fmt.Errorf("port %d is out of range", port)
	}
	used := map[int]bool{}
	for _, p := range skip {
		used[p] = true
	}
	moved := -1
	for i, p := range f.Previews {
		if p.Name == name {
			if !slices.Contains(skip, p.HTTPSPort) {
				f.Previews[i].Port = port
				return f, f.Previews[i], nil
			}
			moved = i // its HTTPS port is skipped now: give it another
			continue
		}
		used[p.HTTPSPort] = true
	}
	if moved >= 0 {
		f.Previews = append(f.Previews[:moved:moved], f.Previews[moved+1:]...)
	}
	if len(f.Previews) >= Max {
		return f, Preview{}, fmt.Errorf("%d previews already; remove one first", Max)
	}
	for hp := lo; hp <= hi; hp++ {
		if !used[hp] {
			p := Preview{Name: name, Port: port, HTTPSPort: hp}
			f.Previews = append(f.Previews, p)
			return f, p, nil
		}
	}
	return f, Preview{}, fmt.Errorf("every port in previews.ports %d-%d is in use; remove a preview first", lo, hi)
}

// Remove drops a preview by name.
func Remove(f File, name string) (File, bool) {
	for i, p := range f.Previews {
		if p.Name == name {
			f.Previews = append(f.Previews[:i:i], f.Previews[i+1:]...)
			return f, true
		}
	}
	return f, false
}
