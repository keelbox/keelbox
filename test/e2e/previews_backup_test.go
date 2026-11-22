//go:build e2e

package e2e

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// newKeysMarker starts the config keys an older keelbox does not know.
const newKeysMarker = "# keelbox e2e: previews and backups\n"

// tailnetKeyFile, when it exists, is a Tailscale auth key (ideally
// ephemeral and pre-approved) for a tailnet with HTTPS certificates on; the
// box then joins it and a preview is fetched over the tailnet from here.
var tailnetKeyFile = filepath.Join(home, ".config/keelbox/tailscale-e2e.key")

// MinIO, run on the box under root's Docker for the backup checks. MinIO
// stopped publishing community images and binaries (minio/minio is gone
// from Docker Hub, dl.min.io answers 410), so this is pgsty/minio, the
// maintained community fork of the same server.
const minioImage = "pgsty/minio:RELEASE.2026-08-04T00-00-00Z"

var backupKeyID, backupSecret, backupPassword string

func randomHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

func haveTailnetKey() bool {
	_, err := os.Stat(tailnetKeyFile)
	return err == nil
}

// featureConfig is the config's previews and backup blocks. The bucket is a
// MinIO the backup checks start on the box itself, on loopback.
func featureConfig(t *testing.T, box string) string {
	t.Helper()
	backupKeyID, backupSecret, backupPassword = "kbe2e"+randomHex(t, 6), randomHex(t, 20), randomHex(t, 16)
	creds := filepath.Join(workDir, "backup-s3.credentials")
	pass := filepath.Join(workDir, "backup.password")
	if err := os.WriteFile(creds, []byte(fmt.Sprintf("[default]\naws_access_key_id = %s\naws_secret_access_key = %s\n", backupKeyID, backupSecret)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pass, []byte(backupPassword+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := newKeysMarker + fmt.Sprintf(`previews:
  ports: 8401-8410
backup:
  repository: s3:http://127.0.0.1:9000/keelbox-e2e/%s
  credentials_file: %s
  password_file: %s
  keep: {daily: 3}
sessions:
  remote_control:
    enabled: true
`, box, creds, pass)
	if haveTailnetKey() {
		cfg += fmt.Sprintf("access:\n  tailscale:\n    auth_key_file: %s\n", tailnetKeyFile)
	}
	return cfg
}

func must(t *testing.T, host, user, remote string, want ...string) string {
	t.Helper()
	out, err := sshRun(t, host, user, nil, remote)
	if err != nil {
		t.Fatalf("%s as %s: %v\n%s", remote, user, err, out)
	}
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Fatalf("%s as %s: output lacks %q:\n%s", remote, user, w, out)
		}
	}
	return out
}

// fakeTailscale stands in for the tailscale CLI on the box: a tailnet with
// HTTPS certificates whose only address is 100.100.1.1 on tailscale0, and a
// self-signed certificate. Everything else goes to the real tailscale.
const fakeTailscale = `#!/bin/sh
# keelbox e2e stand-in for tailscale; removed at the end of the preview checks.
name=NAME
case "$1" in
status)
  printf '{"BackendState":"Running","CertDomains":["%s"],"Self":{"DNSName":"%s.","TailscaleIPs":["100.100.1.1"]}}\n' "$name" "$name" ;;
cert)
  if [ ! -f /root/kb-e2e-cert.pem ]; then
    openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -days 2 -subj "/CN=$name" \
      -addext "subjectAltName=DNS:$name" -keyout /root/kb-e2e-key.pem -out /root/kb-e2e-cert.pem 2>/dev/null || exit 1
  fi
  cp /root/kb-e2e-cert.pem "$3" && cp /root/kb-e2e-key.pem "$5" ;;
*) exec /usr/bin/tailscale "$@" ;;
esac
`

// previewTailnet puts the box on a tailnet for previews: the real one when
// tailscale-e2e.key exists, else a stand-in tailscale with 100.100.1.1 on
// tailscale0 and a self-signed certificate. It returns the box's MagicDNS
// name and, for the stand-in, the address to resolve it to.
func previewTailnet(t *testing.T, host, box string) (name, addr string, cleanup func()) {
	t.Helper()
	name, addr = box+".e2e-fake.ts.net", "100.100.1.1"
	if haveTailnetKey() {
		out := must(t, host, "admin", "tailscale status --json")
		m := regexp.MustCompile(`"DNSName":\s*"([^"]+)\.?"`).FindStringSubmatch(out)
		if m == nil {
			t.Fatalf("no DNSName in tailscale status:\n%s", out)
		}
		name, addr = strings.TrimSuffix(m[1], "."), ""
	} else {
		script := strings.ReplaceAll(fakeTailscale, "NAME", name)
		must(t, host, "admin", "sudo tee /usr/local/bin/tailscale >/dev/null <<'EOF'\n"+script+"EOF\nsudo chmod 755 /usr/local/bin/tailscale")
		must(t, host, "admin", "ip link show tailscale0 >/dev/null 2>&1 || { sudo ip link add tailscale0 type dummy && sudo ip link set tailscale0 up; }; sudo ip addr add 100.100.1.1/32 dev tailscale0")
		cleanup = func() {
			_, _ = sshRun(t, host, "admin", nil, "sudo rm -f /usr/local/bin/tailscale /root/kb-e2e-*.pem; sudo ip addr del 100.100.1.1/32 dev tailscale0; "+
				"ip -d link show tailscale0 | grep -q dummy && sudo ip link del tailscale0; sudo systemctl start keelbox-previews.service")
		}
		logf(t, "no %s: stand-in tailnet (100.100.1.1 on tailscale0, self-signed certificate); the tailnet fetch is not run", tailnetKeyFile)
	}
	if cleanup == nil {
		cleanup = func() {}
	}
	return name, addr, cleanup
}

// previewChecks: off the tailnet expose explains why it cannot work; on a
// (stand-in or real) tailnet, a dev server the agent exposes is served over
// HTTPS on the tailnet address only, Caddy is bound there and nowhere else,
// the firewall admits the preview ports only on tailscale0, and hostile
// agent input never reaches Caddy.
func previewChecks(t *testing.T, host, box, cfg string) {
	t.Helper()
	ok := func(format string, args ...any) { logf(t, "PASS "+format, args...) }

	if !haveTailnetKey() {
		out, err := sshRun(t, host, "agent", nil, "keelbox expose 3000")
		if err == nil || !strings.Contains(out, "not connected to a tailnet") {
			t.Errorf("expose off the tailnet: %v\n%s", err, out)
		} else {
			ok("expose off the tailnet refuses and says why: %s", firstLine(out))
		}
	}
	if out, _ := sshRun(t, host, "admin", nil, "sudo ss -Hltnp | grep '\"caddy\"' || echo none"); !strings.Contains(out, "none") {
		t.Errorf("caddy listens with nothing to serve:\n%s", out)
	}
	must(t, host, "agent", `mkdir -p ~/e2e-www && echo kb-preview-ok > ~/e2e-www/index.html && `+
		`(setsid python3 -m http.server 3000 --bind 127.0.0.1 --directory ~/e2e-www >/dev/null 2>&1 < /dev/null &) && sleep 1`)
	defer func() { _, _ = sshRun(t, host, "agent", nil, "pkill -f 'http.server 3000'; rm -rf ~/e2e-www") }()

	name, addr, cleanup := previewTailnet(t, host, box)
	defer cleanup()

	out := must(t, host, "agent", "keelbox expose 3000 --name web", "https://"+name+":8401/")
	ok("expose: %s", firstLine(out))
	fetch := "curl -fsS --max-time 10 --cacert /tmp/kb-e2e-ca.pem --resolve " + name + ":8401:" + addr + " https://" + name + ":8401/"
	if addr == "" {
		fetch = "curl -fsS --max-time 10 https://" + name + ":8401/"
	}
	must(t, host, "admin", "sudo cat /var/lib/keelbox/previews/tls/cert.pem > /tmp/kb-e2e-ca.pem && "+fetch, "kb-preview-ok")
	ok("preview served over HTTPS at %s", name)
	if haveTailnetKey() {
		c := &http.Client{Timeout: 20 * time.Second}
		resp, err := c.Get("https://" + name + ":8401/")
		if err != nil {
			t.Errorf("fetch over the tailnet from here: %v", err)
		} else {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if !strings.Contains(string(body), "kb-preview-ok") {
				t.Errorf("tailnet fetch: %s", body)
			} else {
				ok("preview fetched over the tailnet from the test host")
			}
		}
	}

	ss := must(t, host, "admin", `sudo ss -Hltnp | grep '"caddy"'`)
	for _, l := range strings.Split(strings.TrimSpace(ss), "\n") {
		f := strings.Fields(l)
		if len(f) < 4 || !(strings.HasPrefix(f[3], "100.") || strings.HasPrefix(f[3], "[fd7a:115c:a1e0:")) {
			t.Errorf("caddy listens off the tailnet: %s", l)
		}
	}
	ok("caddy bound to tailnet addresses only: %s", strings.Join(strings.Fields(ss)[3:4], ""))
	if c, err := net.DialTimeout("tcp", net.JoinHostPort(host, "8401"), 5*time.Second); err == nil {
		_ = c.Close()
		t.Error("preview port reachable on the box's LAN address")
	} else {
		ok("preview port not reachable on the box's LAN address (%v)", err)
	}
	must(t, host, "admin", "sudo ufw status verbose", "on tailscale0")
	must(t, host, "admin", "systemctl show -p IPAddressDeny --value keelbox-preview-caddy.service", "0.0.0.0/0")
	if out, err := kb(t, "doctor", cfg); err != nil || !strings.Contains(out, "Caddy listens on tailnet addresses only") || !strings.Contains(out, "admitted only on tailscale0") {
		t.Errorf("doctor with a preview: %v\n%s", err, out)
	} else {
		ok("doctor: previews binding and firewall")
	}
	must(t, host, "agent", "keelbox expose --list", "web", "listening")

	// Hostile input written straight to the file: refused, never in Caddy.
	must(t, host, "agent", `cp ~/.config/keelbox/previews.json ~/previews.bak && printf '%s' '{"version":1,"previews":[{"name":"x\",\"listen\":[\"0.0.0.0:22\"]","port":22,"https_port":8402},{"name":"ssh","port":22,"https_port":22}]}' > ~/.config/keelbox/previews.json && sleep 3`)
	caddyCfg, _ := sshRun(t, host, "admin", nil, "sudo cat /var/lib/keelbox/previews/caddy.json")
	status, _ := sshRun(t, host, "admin", nil, "cat /var/lib/keelbox/previews/status.json")
	if strings.Contains(caddyCfg, "0.0.0.0") || strings.Contains(caddyCfg, ":22\"") || !strings.Contains(status, "is outside previews.ports") || !strings.Contains(status, "name must be") {
		t.Errorf("hostile previews file:\n%s\n%s", caddyCfg, status)
	} else {
		ok("hostile previews file refused; nothing of it in the Caddy config")
	}
	// A link to a root-only file is read as the agent: refused.
	must(t, host, "agent", "ln -sf /etc/shadow ~/.config/keelbox/previews.json && sleep 3")
	if out, _ := sshRun(t, host, "agent", nil, "cat /var/lib/keelbox/previews/status.json"); !strings.Contains(out, "as agent") || strings.Contains(out, "root:") {
		t.Errorf("previews file linked to /etc/shadow:\n%s", out)
	} else {
		ok("previews file linked to /etc/shadow is read as the agent and refused")
	}
	must(t, host, "agent", "rm ~/.config/keelbox/previews.json && mv ~/previews.bak ~/.config/keelbox/previews.json && sleep 3")

	must(t, host, "agent", "keelbox expose --remove web", "Stopped publishing web")
	if out, _ := sshRun(t, host, "admin", nil, "sleep 1; sudo ss -Hltnp | grep '\"caddy\"' || echo none"); !strings.Contains(out, "none") {
		t.Errorf("caddy still listening after the last preview was removed:\n%s", out)
	} else {
		ok("removing the last preview stops Caddy listening")
	}
}

// backupChecks: back up the agent's home to a MinIO bucket on the box,
// list, restore into a new directory and compare, then delete a file and
// restore over the original. Secrets never show up in output, logs or the
// agent's reach.
func backupChecks(t *testing.T, host, cfg string) {
	t.Helper()
	ok := func(format string, args ...any) { logf(t, "PASS "+format, args...) }
	// Secrets go over stdin into root-only files: sudo logs command lines.
	sshInput(t, host, "admin", fmt.Sprintf("MINIO_ROOT_USER=%s\nMINIO_ROOT_PASSWORD=%s\n", backupKeyID, backupSecret), "sudo install -m 600 /dev/stdin /root/kb-e2e-minio.env")
	sshInput(t, host, "admin", backupSecret+"\n"+backupPassword+"\n", "sudo install -m 600 /dev/stdin /root/kb-e2e-patterns")
	must(t, host, "admin", "sudo docker run -d --name e2e-minio -p 127.0.0.1:9000:9000 --env-file /root/kb-e2e-minio.env "+minioImage+" server /data >/dev/null && "+
		"for i in $(seq 60); do curl -fsS http://127.0.0.1:9000/minio/health/live && break; sleep 2; done")
	defer func() {
		_, _ = sshRun(t, host, "admin", nil, "sudo docker rm -f e2e-minio; sudo rm -f /root/kb-e2e-minio.env /root/kb-e2e-patterns")
	}()

	must(t, host, "agent", "mkdir -p ~/projects/e2e-backup/node_modules/x ~/projects/e2e-backup/src && echo keep-me > ~/projects/e2e-backup/a.txt && "+
		"head -c 1048576 /dev/urandom > ~/projects/e2e-backup/src/blob && echo skip > ~/projects/e2e-backup/node_modules/x/big.js")
	var all strings.Builder
	run := func(args ...string) string {
		t.Helper()
		out, err := kb(t, args...)
		all.WriteString(out)
		if err != nil {
			t.Fatalf("keelbox %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return out
	}
	out := run("backup", cfg)
	if !strings.Contains(out, "backed up /home/agent as agent") {
		t.Fatalf("backup output:\n%s", out)
	}
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "backed up ") {
			ok("%s", l)
		}
	}
	if du, err := sshRun(t, host, "agent", nil, "cd && du -xsh --exclude=node_modules .[!.]* * 2>/dev/null | sort -h | tail -4 | tr '\\n\\t' '; '"); err == nil {
		logf(t, "largest in the agent's home: %s", du)
	}
	run("backup", cfg) // a second, incremental run
	out = run("backup", "--list", cfg)
	if strings.Count(out, "/home/agent") != 2 {
		t.Errorf("backup --list: want two snapshots of /home/agent:\n%s", out)
	} else {
		ok("backup --list shows both snapshots")
	}

	out = run("restore", cfg)
	m := regexp.MustCompile(`into (/home/agent/keelbox-restore/[0-9a-f]{8}), as agent`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("restore did not go to a new directory in the agent's home:\n%s", out)
	}
	dir := m[1]
	must(t, host, "agent", "diff -r ~/projects/e2e-backup "+dir+"/projects/e2e-backup -x node_modules && test ! -e "+dir+"/projects/e2e-backup/node_modules && "+
		"test \"$(stat -c %U "+dir+"/projects/e2e-backup/a.txt)\" = agent && echo same")
	ok("restore into %s matches the original, node_modules left out, files owned by the agent", dir)
	if out, err := kb(t, "restore", cfg); err == nil || !strings.Contains(out, "not empty") {
		t.Errorf("a second restore into the same, non-empty directory was not refused: %v\n%s", err, out)
	} else {
		ok("a restore into a non-empty directory is refused")
	}

	must(t, host, "agent", "rm ~/projects/e2e-backup/a.txt")
	out = run("restore", "--overwrite", cfg)
	if !strings.Contains(out, "over /home/agent") {
		t.Errorf("restore --overwrite output:\n%s", out)
	}
	must(t, host, "agent", "cat ~/projects/e2e-backup/a.txt", "keep-me")
	ok("restore --overwrite brought back a deleted file")

	must(t, host, "admin", "sudo stat -c '%a %U' /etc/keelbox/backup/s3.credentials /etc/keelbox/backup/restic.password", "600 root")
	if out, err := sshRun(t, host, "agent", nil, "cat /etc/keelbox/backup/s3.credentials"); err == nil {
		t.Errorf("the agent can read the bucket credentials:\n%s", out)
	} else {
		ok("the agent cannot read the bucket credentials")
	}
	leaks, _ := sshRun(t, host, "admin", nil, "sudo grep -rlF -f /root/kb-e2e-patterns /var/lib/keelbox/logbook.jsonl /etc/keelbox/keelbox.yaml /var/lib/keelbox/backup /var/lib/keelbox/previews; "+
		"sudo sh -c 'journalctl -q --no-pager | grep -cF -f /root/kb-e2e-patterns'")
	if strings.Contains(all.String(), backupSecret) || strings.Contains(all.String(), backupPassword) || strings.TrimSpace(leaks) != "0" {
		t.Errorf("a backup secret appeared in output, the logbook, the applied config or the journal: %q", leaks)
	} else {
		ok("backup secrets in no output, logbook, applied config or journal")
	}
	if out, err := kb(t, "doctor", cfg); err != nil || !strings.Contains(out, "last successful backup") {
		t.Errorf("doctor after a backup: %v\n%s", err, out)
	} else {
		ok("doctor reports the last successful backup")
	}
	must(t, host, "admin", "systemctl is-active keelbox-backup.timer", "active")
}

// sshInput runs remote with input on its stdin, so secrets stay out of
// command lines on the box.
func sshInput(t *testing.T, host, user, input, remote string) {
	t.Helper()
	cmd := exec.Command("ssh", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null",
		"-o", "ConnectTimeout=10", "-o", "LogLevel=ERROR", "-i", keyPath, "-o", "IdentitiesOnly=yes", user+"@"+host, "--", remote)
	cmd.Stdin = strings.NewReader(input)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s: %v\n%s", remote, err, out)
	}
}
