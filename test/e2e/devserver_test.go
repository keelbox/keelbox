//go:build e2e

package e2e

import (
	"strings"
	"testing"
	"time"
)

// TestPreviewDevServer: a stock Vite 6 dev server, with no allowedHosts,
// bound to [::1] only (where localhost resolves first on many images), exposed
// before it starts: expose waits for the first certificate and says so,
// --list sees the server once it runs, and the preview passes Vite's host
// check because Caddy sends the upstream's own Host.
func TestPreviewDevServer(t *testing.T) {
	ensureKey(t)
	build(t)
	_, host := agentsBox(t)
	box := strings.TrimSpace(must(t, host, "admin", "hostname"))
	name, addr, cleanup := previewTailnet(t, host, box)
	defer cleanup()

	must(t, host, "agent", `mkdir -p ~/projects/vite6 && cd ~/projects/vite6 && npm init -y >/dev/null && `+
		`npm install --no-audit --no-fund vite@6 >/dev/null 2>&1 && echo '<!doctype html><p>kb-vite-ok</p>' > index.html && npx vite --version`, "vite/6.")

	// Exposed before the server runs, on a box with no certificate yet.
	out := must(t, host, "agent", "keelbox expose 5173 --name web", "https://"+name+":8401/", "Nothing listens on port 5173 yet")
	if !strings.Contains(out, "first time only") {
		t.Errorf("first expose does not say it waits for the certificate:\n%s", out)
	}
	logf(t, "PASS expose before the server: %s", firstLine(out))
	must(t, host, "agent", "keelbox expose --list", "nothing on the port")

	must(t, host, "agent", `cd ~/projects/vite6 && (setsid npx vite --host ::1 --port 5173 --strictPort > /tmp/vite.log 2>&1 < /dev/null &) && `+
		`for i in $(seq 60); do ss -Hltn | grep -q ':5173 ' && break; sleep 1; done; ss -Hltn | grep ':5173 '`)
	defer func() { _, _ = sshRun(t, host, "agent", nil, "pkill -f 'vite --host ::1 --port 5173'") }()
	bound := must(t, host, "agent", "ss -Hltn | grep ':5173 '", "[::1]:5173")
	if strings.Contains(bound, "127.0.0.1:5173") {
		t.Fatalf("vite listens on IPv4 too, so this does not test an IPv6-only server:\n%s", bound)
	}
	logf(t, "vite listens on [::1]:5173 only")

	// Live, not the service's run from before the server started.
	must(t, host, "agent", "keelbox expose --list", "web", "listening")
	logf(t, "PASS expose --list sees the server started after expose")

	// Vite refuses the tailnet name itself, so the test means something.
	if out, _ := sshRun(t, host, "agent", nil, "curl -s -o /dev/null -w '%{http_code}' -H 'Host: "+name+":8401' 'http://[::1]:5173/'"); !strings.Contains(out, "403") {
		t.Fatalf("vite 6 accepted an unknown Host (%s): the host check this test relies on is gone", out)
	}
	fetch := "curl -sS --max-time 10 -w ' %{http_code}' --cacert /tmp/kb-e2e-ca.pem --resolve " + name + ":8401:" + addr + " https://" + name + ":8401/"
	if addr == "" {
		fetch = "curl -sS --max-time 10 -w ' %{http_code}' https://" + name + ":8401/"
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		out, err := sshRun(t, host, "admin", nil, "sudo cat /var/lib/keelbox/previews/tls/cert.pem > /tmp/kb-e2e-ca.pem && "+fetch)
		if err == nil && strings.Contains(out, "kb-vite-ok") && strings.HasSuffix(strings.TrimSpace(out), "200") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("preview of vite 6: %v\n%s", err, out)
		}
		time.Sleep(2 * time.Second)
	}
	logf(t, "PASS vite 6 with no allowedHosts served through the preview at %s", name)

	must(t, host, "agent", "keelbox expose --remove web", "Stopped publishing web")
}
