package previews

import (
	"encoding/json"
	"net/netip"
	"strings"
	"testing"
)

func TestParseFileKeepsOnlyValidEntries(t *testing.T) {
	in := `{"version":1,"previews":[
		{"name":"web","port":3000,"https_port":8401},
		{"name":"Bad Name","port":3001,"https_port":8402},
		{"name":"evil\"}},{\"listen\":[\"0.0.0.0:443\"]","port":3002,"https_port":8403},
		{"name":"zero","port":0,"https_port":8404},
		{"name":"low","port":3003,"https_port":443},
		{"name":"dup","port":3004,"https_port":8401},
		{"name":"web","port":3005,"https_port":8405},
		{"name":"api","port":4000,"https_port":8499}
	]}`
	ps, problems := ParseFile([]byte(in), 8401, 8499)
	if len(ps) != 2 || ps[0].Name != "web" || ps[1].Name != "api" {
		t.Fatalf("kept %+v", ps)
	}
	if len(problems) != 6 {
		t.Errorf("want 6 problems, got %d: %v", len(problems), problems)
	}
	for _, bad := range []string{`{"version":2,"previews":[]}`, `not json`, strings.Repeat(" ", maxFile+1)} {
		if ps, problems := ParseFile([]byte(bad), 8401, 8499); ps != nil || len(problems) != 1 {
			t.Errorf("%.20q: kept %v, problems %v", bad, ps, problems)
		}
	}
}

func TestRenderCaddyListensOnTheTailnetOnly(t *testing.T) {
	tn := Tailnet{Connected: true, HTTPS: true, Name: "box.tail1.ts.net", IPs: []string{"100.64.1.2", "fd7a:115c:a1e0::5"}}
	b, err := RenderCaddy(tn, []Preview{{Name: "web", Port: 3000, HTTPSPort: 8401}})
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Admin struct{ Disabled bool }
		Apps  struct {
			HTTP struct {
				Servers map[string]struct {
					Listen         []string
					AutomaticHTTPS struct{ Disable bool } `json:"automatic_https"`
					Routes         []struct {
						Handle []struct {
							Upstreams     []struct{ Dial string }
							LoadBalancing struct {
								SelectionPolicy struct{ Policy string } `json:"selection_policy"`
								TryDuration     string                  `json:"try_duration"`
							} `json:"load_balancing"`
							HealthChecks struct {
								Passive struct {
									MaxFails int `json:"max_fails"`
								}
							} `json:"health_checks"`
							Headers struct {
								Request struct{ Set map[string][]string }
							}
						}
					}
				}
			}
		}
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	if !cfg.Admin.Disabled {
		t.Error("admin API on: any local process could change Caddy's config")
	}
	srv := cfg.Apps.HTTP.Servers["preview-web"]
	if strings.Join(srv.Listen, " ") != "100.64.1.2:8401 [fd7a:115c:a1e0::5]:8401" {
		t.Errorf("listen %v", srv.Listen)
	}
	if !srv.AutomaticHTTPS.Disable {
		t.Error("automatic HTTPS on: Caddy would open :80 and try ACME")
	}
	// localhost is 127.0.0.1 alone in Ubuntu's /etc/hosts; a server on
	// [::1] only is reached through the second upstream.
	h := srv.Routes[0].Handle[0]
	if len(h.Upstreams) != 2 || h.Upstreams[0].Dial != "127.0.0.1:3000" || h.Upstreams[1].Dial != "[::1]:3000" {
		t.Errorf("upstreams %+v", h.Upstreams)
	}
	if h.LoadBalancing.SelectionPolicy.Policy != "first" || h.LoadBalancing.TryDuration == "" || h.HealthChecks.Passive.MaxFails != 1 {
		t.Errorf("fallback to [::1]: %+v %+v", h.LoadBalancing, h.HealthChecks)
	}
	// Vite 6 and later refuse an unknown Host: the upstream gets its own,
	// the browser's goes in X-Forwarded-Host.
	set := srv.Routes[0].Handle[0].Headers.Request.Set
	if h := set["Host"]; len(h) != 1 || h[0] != "localhost:3000" {
		t.Errorf("Host to the upstream %v", h)
	}
	if h := set["X-Forwarded-Host"]; len(h) != 1 || h[0] != "{http.request.host}" {
		t.Errorf("X-Forwarded-Host %v", h)
	}
	if h := set["X-Forwarded-Proto"]; len(h) != 1 || h[0] != "https" {
		t.Errorf("X-Forwarded-Proto %v", h)
	}
	for _, ip := range []string{"203.0.113.5", "0.0.0.0", "::", "192.168.1.10"} {
		if _, err := RenderCaddy(Tailnet{IPs: []string{ip}}, []Preview{{Name: "web", Port: 3000, HTTPSPort: 8401}}); err == nil {
			t.Errorf("rendered a listener on %s", ip)
		}
	}
}

func TestParseTailscaleStatus(t *testing.T) {
	on := ParseTailscaleStatus(`{"BackendState":"Running","CertDomains":["box.tail1.ts.net"],"Self":{"DNSName":"box.tail1.ts.net.","TailscaleIPs":["100.64.1.2","fd7a:115c:a1e0::5","192.0.2.1"]}}`)
	if !on.Connected || !on.HTTPS || on.Name != "box.tail1.ts.net" || len(on.IPs) != 2 {
		t.Errorf("running: %+v", on)
	}
	if noCert := ParseTailscaleStatus(`{"BackendState":"Running","Self":{"DNSName":"box.tail1.ts.net.","TailscaleIPs":["100.64.1.2"]}}`); noCert.HTTPS || !noCert.Connected {
		t.Errorf("no cert domains: %+v", noCert)
	}
	if off := ParseTailscaleStatus(`{"BackendState":"NeedsLogin"}`); off.Connected {
		t.Errorf("logged out: %+v", off)
	}
}

func TestListeners(t *testing.T) {
	ss := "LISTEN 0 4096 127.0.0.1:3000 0.0.0.0:*\n" +
		"LISTEN 0 4096 *:5173 *:*\n" +
		"LISTEN 0 4096 [::1]:4000 [::]:*\n" +
		"LISTEN 0 4096 100.64.1.2:8401 0.0.0.0:* users:((\"caddy\",pid=9,fd=7))\n" +
		"LISTEN 0 4096 [fe80::1%eth0]:22 [::]:*\n"
	ls := Listeners(ss)
	for _, port := range []int{3000, 5173, 4000} {
		if !LoopbackListening(ls, port) {
			t.Errorf("port %d not seen on loopback", port)
		}
	}
	if LoopbackListening(ls, 8401) {
		t.Error("a tailnet-only listener counted as loopback")
	}
	if p := ls[netip.MustParseAddrPort("100.64.1.2:8401")]; !strings.Contains(p, `"caddy"`) {
		t.Errorf("process column lost: %q", p)
	}
}

func TestAssignAndRemove(t *testing.T) {
	f := File{Version: 1}
	f, a, err := Assign(f, "web", 3000, 8401, 8403)
	if err != nil || a.HTTPSPort != 8401 {
		t.Fatalf("%v %+v", err, a)
	}
	f, b, _ := Assign(f, "api", 4000, 8401, 8403)
	f, a2, _ := Assign(f, "web", 3001, 8401, 8403)
	if b.HTTPSPort != 8402 || a2.HTTPSPort != 8401 || a2.Port != 3001 || len(f.Previews) != 2 {
		t.Errorf("reassign: %+v %+v %+v", b, a2, f)
	}
	f, _ = Remove(f, "web")
	f, c, _ := Assign(f, "docs", 5000, 8401, 8403)
	if c.HTTPSPort != 8401 {
		t.Errorf("freed port not reused: %+v", c)
	}
	f, _, _ = Assign(f, "x", 1, 8401, 8403)
	if _, _, err := Assign(f, "y", 2, 8401, 8403); err == nil {
		t.Error("assigned past the range")
	}
	if _, _, err := Assign(f, "Bad!", 2, 8401, 8403); err == nil {
		t.Error("bad name accepted")
	}
	// What the CLI writes, the service accepts.
	if ps, problems := ParseFile(f.Marshal(), 8401, 8403); len(ps) != 3 || len(problems) != 0 {
		t.Errorf("round trip: %v %v", ps, problems)
	}
}

// Ports host.firewall.allow keeps open to the LAN are skipped: a new
// preview never gets one, a preview already on one moves when it is
// exposed again, and the service refuses an entry on one.
func TestAssignSkipsPortsOpenToTheLAN(t *testing.T) {
	f := File{Version: 1, Previews: []Preview{{Name: "web", Port: 3000, HTTPSPort: 8443}}}
	if ps, problems := ParseFile(f.Marshal(), 8441, 8445, 8443); len(ps) != 0 || len(problems) != 1 || !strings.Contains(problems[0], "kept open to the LAN by host.firewall.allow") {
		t.Errorf("entry on a skipped port: %v %v", ps, problems)
	}
	f, p, err := Assign(f, "api", 4000, 8442, 8445, 8442, 8443)
	if err != nil || p.HTTPSPort != 8444 {
		t.Fatalf("new preview: %v %+v", err, p)
	}
	f, p, err = Assign(f, "web", 3000, 8442, 8445, 8442, 8443)
	if err != nil || p.HTTPSPort != 8445 || len(f.Previews) != 2 {
		t.Fatalf("moved preview: %v %+v %+v", err, p, f)
	}
	if _, _, err := Assign(f, "docs", 5000, 8442, 8445, 8442, 8443); err == nil {
		t.Error("assigned a skipped port")
	}
}
