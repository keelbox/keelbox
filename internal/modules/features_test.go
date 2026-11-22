package modules

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/backup"
	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/previews"
	"github.com/keelbox/keelbox/internal/system"
)

const agentPreviews = "/home/agent/" + previews.AgentFile

func status(t *testing.T, s *sim) previews.Status {
	t.Helper()
	st, err := previews.ParseStatus([]byte(s.content(previews.StatusPath)))
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	return st
}

func writeAgentPreviews(s *sim, content string) {
	s.Files[agentPreviews] = &system.FakeFile{Data: []byte(content), Mode: 0o644, Owner: "agent", Group: "agent"}
}

// TestPreviewsServeOnlyOnTheTailnet: off the tailnet nothing listens and the
// status says why; on it, Caddy gets a config that listens on the tailnet
// addresses only, with the certificate readable by root and Caddy's user.
func TestPreviewsServeOnlyOnTheTailnet(t *testing.T) {
	s := newSim(t)
	e := simEnv(t, s, testConfig(t, ""))
	ctx := context.Background()
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatal(err)
	}
	writeAgentPreviews(s, `{"version":1,"previews":[{"name":"web","port":3000,"https_port":8401}]}`)
	st, err := previews.Sync(ctx, e)
	if err != nil {
		t.Fatal(err)
	}
	if st.Certificate {
		t.Error("certificate reported before the box has one")
	}
	if st.Error != previews.NotOnTailnet || s.units[previews.CaddyUnit] || s.has(previews.CaddyConfig) {
		t.Fatalf("off the tailnet: error %q, caddy running %v", st.Error, s.units[previews.CaddyUnit])
	}
	if !strings.Contains(PreviewsSummary(e), "not on the tailnet") {
		t.Errorf("status line: %s", PreviewsSummary(e))
	}

	s.tailscale = "Running"
	s.listening = "LISTEN 0 4096 127.0.0.1:3000 0.0.0.0:*\n"
	if _, err := previews.Sync(ctx, e); err != nil {
		t.Fatal(err)
	}
	st = status(t, s)
	if st.Error != "" || len(st.Previews) != 1 || st.Previews[0].URL != "https://testbox.tail1234.ts.net:8401/" || !st.Previews[0].Listening || !st.Certificate {
		t.Fatalf("on the tailnet: %+v", st)
	}
	cfg := s.content(previews.CaddyConfig)
	for _, want := range []string{`"100.101.102.103:8401"`, `"[fd7a:115c:a1e0::1]:8401"`, `"disabled": true`, `"localhost:3000"`} {
		if !strings.Contains(cfg, want) {
			t.Errorf("caddy config lacks %s:\n%s", want, cfg)
		}
	}
	if !s.units[previews.CaddyUnit] {
		t.Error("caddy not started")
	}
	key := s.Files[previews.KeyPath]
	if key == nil || key.Mode != 0o640 || key.Owner != "root" || key.Group != previews.ServiceUser {
		t.Errorf("key file: %+v", key)
	}
	if s.has(previews.KeyPath+".new") || s.has(previews.CertPath+".new") {
		t.Error("staged certificate left behind")
	}
	if f := s.Files[previews.CaddyConfig]; f.Mode != 0o640 || f.Group != previews.ServiceUser {
		t.Errorf("caddy config file: %+v", f)
	}

	// No previews left: Caddy stops, so nothing listens at all.
	writeAgentPreviews(s, `{"version":1,"previews":[]}`)
	if _, err := previews.Sync(ctx, e); err != nil {
		t.Fatal(err)
	}
	if s.units[previews.CaddyUnit] {
		t.Error("caddy still running with nothing to serve")
	}
	for _, it := range engine.Plan(ctx, e, All()) {
		if !it.OK {
			t.Errorf("sync left %s/%s pending: %s", it.Module, it.Step, it.Detail)
		}
	}
}

// TestPreviewsRenderAgainAfterUpgrade: a status written by a keelbox whose
// Caddy config was older (one that sent the tailnet Host upstream) makes
// the next apply sync, so an upgrade fixes running previews at once and not
// at the next timer run.
func TestPreviewsRenderAgainAfterUpgrade(t *testing.T) {
	s := newSim(t)
	s.tailscale = "Running"
	e := simEnv(t, s, testConfig(t, ""))
	ctx := context.Background()
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatal(err)
	}
	writeAgentPreviews(s, `{"version":1,"previews":[{"name":"web","port":5173,"https_port":8401}]}`)
	if _, err := previews.Sync(ctx, e); err != nil {
		t.Fatal(err)
	}
	// What an older keelbox left: no render version, a config without the Host.
	var old map[string]any
	if err := json.Unmarshal([]byte(s.content(previews.StatusPath)), &old); err != nil {
		t.Fatal(err)
	}
	delete(old, "render")
	b, _ := json.Marshal(old)
	s.Files[previews.StatusPath].Data = b
	s.Files[previews.CaddyConfig].Data = []byte("{}\n")

	var pending []string
	for _, it := range engine.Plan(ctx, e, All()) {
		if !it.OK {
			pending = append(pending, it.Step)
		}
	}
	if len(pending) != 1 || pending[0] != "previews-sync" {
		t.Fatalf("after the upgrade, pending: %v", pending)
	}
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatal(err)
	}
	if st := status(t, s); st.Render != previews.Render {
		t.Errorf("render after apply: %d", st.Render)
	}
	if !strings.Contains(s.content(previews.CaddyConfig), `"localhost:5173"`) {
		t.Errorf("caddy config not rewritten:\n%s", s.content(previews.CaddyConfig))
	}
	for _, it := range engine.Plan(ctx, e, All()) {
		if !it.OK {
			t.Errorf("second plan: %s/%s pending: %s", it.Module, it.Step, it.Detail)
		}
	}
}

// TestPreviewsReadTheAgentFileAsTheAgent: a link from the agent's previews
// file to a root-only file gets the service a permission error, not the
// file, and nothing of it reaches the status or Caddy.
func TestPreviewsReadTheAgentFileAsTheAgent(t *testing.T) {
	s := newSim(t)
	s.tailscale = "Running"
	e := simEnv(t, s, testConfig(t, ""))
	ctx := context.Background()
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatal(err)
	}
	s.Files[agentPreviews] = &system.FakeFile{Target: "/etc/shadow", Owner: "agent", Group: "agent"}
	st, err := previews.Sync(ctx, e)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Problems) == 0 || !strings.Contains(st.Problems[0], "as agent") {
		t.Errorf("want a read error as the agent, got %v", st.Problems)
	}
	if strings.Contains(s.content(previews.StatusPath), "root:") || strings.Contains(s.content(previews.CaddyConfig), "root:") {
		t.Error("shadow content reached a file the agent can read")
	}
}

// TestPreviewsRefuseInvalidAgentInput: entries that try to inject Caddy
// config, privileged ports or ports outside the range are dropped and
// reported; doctor warns about them.
func TestPreviewsRefuseInvalidAgentInput(t *testing.T) {
	s := newSim(t)
	s.tailscale = "Running"
	e := simEnv(t, s, testConfig(t, "previews:\n  ports: 9000-9001\n"))
	ctx := context.Background()
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatal(err)
	}
	writeAgentPreviews(s, `{"version":1,"previews":[
		{"name":"x\",\"listen\":[\"0.0.0.0:443\"]","port":3000,"https_port":9000},
		{"name":"web","port":3000,"https_port":443},
		{"name":"ok","port":3000,"https_port":9001}]}`)
	st, err := previews.Sync(ctx, e)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Previews) != 1 || st.Previews[0].Name != "ok" || len(st.Problems) != 2 {
		t.Fatalf("published %+v, problems %v", st.Previews, st.Problems)
	}
	cfg := s.content(previews.CaddyConfig)
	if strings.Contains(cfg, "0.0.0.0") || strings.Contains(cfg, ":443") {
		t.Errorf("agent input reached the caddy config:\n%s", cfg)
	}
	warned := false
	for _, f := range engine.Doctor(ctx, e, All()) {
		warned = warned || (f.Module == "previews" && f.Check == "agent-file" && f.Level == engine.LevelWarn)
	}
	if !warned {
		t.Error("doctor did not report the refused entries")
	}
}

// TestPreviewsDoctorFailsOnAPublicListener: Caddy listening anywhere but a
// tailnet address, or a firewall rule admitting the preview ports off the
// tailnet, fails doctor.
func TestPreviewsDoctorFailsOnAPublicListener(t *testing.T) {
	s := newSim(t)
	e := simEnv(t, s, testConfig(t, ""))
	ctx := context.Background()
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatal(err)
	}
	find := func(check string) engine.Finding {
		for _, f := range engine.Doctor(ctx, e, All()) {
			if f.Module == "previews" && f.Check == check {
				return f
			}
		}
		t.Fatalf("no previews/%s finding", check)
		return engine.Finding{}
	}
	s.listening = "LISTEN 0 4096 100.101.102.103:8401 0.0.0.0:* users:((\"caddy\",pid=9,fd=7))\n"
	if f := find("binding"); f.Level != engine.LevelOK {
		t.Errorf("tailnet listener: %+v", f)
	}
	s.listening += "LISTEN 0 4096 0.0.0.0:8402 0.0.0.0:* users:((\"caddy\",pid=9,fd=8))\n"
	if f := find("binding"); f.Level != engine.LevelFail {
		t.Errorf("public listener passed: %+v", f)
	}
	if f := find("firewall"); f.Level != engine.LevelOK {
		t.Errorf("firewall: %+v", f)
	}
	for _, to := range []string{"8401/tcp", "8000:9000/tcp", "Anywhere", "8443"} {
		if !portRuleOverlaps(to, 8401, 8499, nil) {
			t.Errorf("%s not seen as admitting preview ports", to)
		}
	}
	for _, to := range []string{"22/tcp", "8443/tcp", "8443", "8443:8444/tcp", "8450/udp"} {
		if portRuleOverlaps(to, 8401, 8499, []int{8443, 8444}) {
			t.Errorf("%s seen as admitting preview ports", to)
		}
	}
}

// A port host.firewall.allow keeps open to the LAN inside previews.ports
// (UniFi's 8443 in the default 8401-8499) is left out of the range, not a
// doctor failure: previews never use it, and doctor says so.
func TestPreviewsSkipPortsKeptOpenToTheLAN(t *testing.T) {
	s := newSim(t)
	e := simEnv(t, s, testConfig(t, "host:\n  firewall:\n    allow:\n      - {service: unifi}\n      - {port: 8450-8451}\n      - {port: 8460, proto: udp}\n"))
	if got := fmt.Sprint(e.Cfg.PreviewSkipped()); got != "[8443 8450 8451]" {
		t.Fatalf("skipped %s", got)
	}
	ctx := context.Background()
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatal(err)
	}
	for _, f := range engine.Doctor(ctx, e, All()) {
		if f.Module == "previews" && f.Check == "firewall" && (f.Level != engine.LevelOK || !strings.Contains(f.Detail, "previews skip 8443, 8450, 8451, kept open to the LAN by host.firewall.allow (unifi, 8450-8451)")) {
			t.Errorf("firewall: %+v", f)
		}
	}
	st, _ := previews.ReadStatus(e)
	if fmt.Sprint(st.Skip) != "[8443 8450 8451]" {
		t.Errorf("status skip %v", st.Skip)
	}
	for _, it := range engine.Plan(ctx, e, All()) {
		if !it.OK {
			t.Errorf("not converged: %+v", it)
		}
	}
}

func TestPreviewsNeedTailscale(t *testing.T) {
	s := newSim(t)
	e := simEnv(t, s, testConfig(t, "access:\n  tailscale:\n    enabled: false\n"))
	if (Previews{}).Enabled(e.Cfg) {
		t.Error("previews enabled without tailscale")
	}
}

const backupYAML = `backup:
  repository: s3:https://s3.example.com/bucket/testbox
  credentials_file: /run/keelbox/backup.credentials
  password_file: /run/keelbox/backup.password
`

const (
	credsSecret = "aws_secret_access_key = SECRETKEYVALUE"
	passSecret  = "REPOSITORYPASSWORD"
)

func backupSim(t *testing.T, extra string) (*sim, *engine.Env) {
	t.Helper()
	s := newSim(t)
	s.AddFile("/run/keelbox/backup.credentials", "[default]\naws_access_key_id = AKIA\n"+credsSecret+"\n", 0o600)
	s.AddFile("/run/keelbox/backup.password", passSecret+"\n", 0o600)
	e := simEnv(t, s, testConfig(t, backupYAML+extra))
	if _, err := engine.Apply(context.Background(), e, All()); err != nil {
		t.Fatalf("apply: %v", err)
	}
	return s, e
}

func TestBackupConvergesAndKeepsSecretsRootOnly(t *testing.T) {
	s, e := backupSim(t, "")
	ctx := context.Background()
	for _, it := range engine.Plan(ctx, e, All()) {
		if !it.OK {
			t.Errorf("after apply, %s/%s pending: %s", it.Module, it.Step, it.Detail)
		}
	}
	for _, p := range []string{backup.CredentialsPath, backup.PasswordPath} {
		f := s.Files[p]
		if f == nil || f.Mode != 0o600 || f.Owner != "root" {
			t.Errorf("%s: %+v", p, f)
		}
	}
	if !strings.Contains(s.content(unitDir+backup.Timer), "OnCalendar=daily") || !s.units[backup.Timer] {
		t.Error("timer not installed and running")
	}
	// The staged copies go away; the installed ones stand.
	delete(s.Files, "/run/keelbox/backup.credentials")
	delete(s.Files, "/run/keelbox/backup.password")
	res, err := engine.Apply(ctx, e, All())
	if err != nil || len(res.Changes) != 0 {
		t.Errorf("apply without the staged secrets: %v %v", err, res.Changes)
	}
	for _, c := range s.Calls {
		line := c.String() + " " + strings.Join(c.Env, " ")
		if strings.Contains(line, "SECRETKEYVALUE") || strings.Contains(line, passSecret) {
			t.Errorf("secret in a command: %s", line)
		}
	}
	for _, it := range engine.Plan(ctx, e, All()) {
		if strings.Contains(it.Detail, "SECRET") || strings.Contains(it.Detail, passSecret) {
			t.Errorf("secret in plan output: %s", it.Detail)
		}
	}
}

func TestBackupSecretOutsideRootOnlyDirIsRefused(t *testing.T) {
	s := newSim(t)
	s.AddUser("admin", 1000)
	s.Files["/home/admin/s3"] = &system.FakeFile{Data: []byte("x"), Mode: 0o600, Owner: "admin", Group: "admin"}
	s.AddFile("/root/pw", "x", 0o644)
	e := simEnv(t, s, testConfig(t, "backup:\n  repository: s3:https://s3.example.com/b\n  credentials_file: /home/admin/s3\n  password_file: /root/pw\n"))
	ctx := context.Background()
	if _, err := secretFileStep("c", "backup.credentials_file", "/home/admin/s3", backup.CredentialsPath).Check(ctx, e); err == nil || !strings.Contains(err.Error(), backup.SecretDir) {
		t.Errorf("credentials in the admin's home accepted: %v", err)
	}
	if _, err := secretFileStep("p", "backup.password_file", "/root/pw", backup.PasswordPath).Check(ctx, e); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Errorf("world-readable password accepted: %v", err)
	}
}

// resticSim answers restic like a repository with the given snapshots.
type resticSim struct {
	snapshots []backup.Snapshot
	calls     []system.Cmd
	exists    bool
}

func (r *resticSim) run(c system.Cmd) (system.Result, error) {
	r.calls = append(r.calls, c)
	if c.Name == "/bin/bash" {
		return res("")
	}
	switch c.Args[0] {
	case "cat":
		if !r.exists {
			rr := system.Result{ExitCode: 10}
			return rr, &system.ExitError{Cmd: c, Result: rr}
		}
	case "init":
		if r.exists {
			rr := system.Result{ExitCode: 1, Stderr: "Fatal: create repository at s3:x failed: config file already exists\n"}
			return rr, &system.ExitError{Cmd: c, Result: rr}
		}
		r.exists = true
	case "backup":
		id := strings.Repeat("ab", 32)
		return res(`{"message_type":"status"}` + "\n" + `{"message_type":"summary","snapshot_id":"` + id + `","total_bytes_processed":2048,"data_added":1024}` + "\n")
	case "snapshots":
		b, _ := json.Marshal(r.snapshots)
		return res(string(b))
	}
	return res("")
}

func TestBackupReadsEachPathAsItsOwner(t *testing.T) {
	s, e := backupSim(t, "  paths: [/home/agent, /etc/keelbox]\n")
	r := &resticSim{}
	s.restic = r.run
	var out strings.Builder
	if err := backup.Run(context.Background(), e, &out); err != nil {
		t.Fatal(err)
	}
	var backups []string
	for _, c := range r.calls {
		env := strings.Join(c.Env, " ")
		for _, want := range []string{"RESTIC_PASSWORD_FILE=" + backup.PasswordPath, "AWS_SHARED_CREDENTIALS_FILE=" + backup.CredentialsPath, "AWS_SECRET_ACCESS_KEY="} {
			if !strings.Contains(env, want) {
				t.Errorf("%s: env lacks %s", c, want)
			}
		}
		if c.Args[0] == "backup" {
			backups = append(backups, c.String())
		}
	}
	if !strings.Contains(r.calls[0].String(), "init") {
		t.Errorf("repository not initialised: %v", r.calls)
	}
	if len(backups) != 2 {
		t.Fatalf("backups: %v", backups)
	}
	if !strings.Contains(backups[0], "-- /usr/sbin/runuser -u agent -- env -i") || !strings.Contains(backups[0], "--exclude=node_modules") {
		t.Errorf("agent's home not read as the agent: %s", backups[0])
	}
	if strings.Contains(backups[1], "runuser") || !strings.Contains(backups[1], "keelbox-path=/etc/keelbox") {
		t.Errorf("root path: %s", backups[1])
	}
	last := r.calls[len(r.calls)-1].String()
	for _, want := range []string{"forget", "--prune", "--keep-daily 7", "--keep-weekly 4", "--keep-monthly 6", "--group-by host,tags"} {
		if !strings.Contains(last, want) {
			t.Errorf("retention lacks %s: %s", want, last)
		}
	}
	st, _ := backup.ReadStatus(e)
	if st.LastSuccess.IsZero() || st.Error != "" || s.Files[backup.StatusPath].Mode != 0o600 {
		t.Errorf("status: %+v", st)
	}
	if !strings.Contains(out.String(), "backed up /home/agent as agent") {
		t.Errorf("output: %s", out.String())
	}

	// Later runs skip init; a status from before (or for another
	// repository) gets an init that finds the repository and goes on.
	r.calls = nil
	if err := backup.Run(context.Background(), e, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(r.calls[0].String(), "init") {
		t.Errorf("second run tried init again: %v", r.calls[0])
	}
	delete(s.Files, backup.StatusPath)
	r.calls = nil
	if err := backup.Run(context.Background(), e, &out); err != nil {
		t.Fatalf("init on an existing repository: %v", err)
	}
}

func TestRestoreGoesToANewDirectoryUnlessOverwrite(t *testing.T) {
	s, e := backupSim(t, "")
	snap := backup.Snapshot{ID: strings.Repeat("cd", 32), Time: time.Now(), Tags: []string{backup.Tag, backup.PathTag + "/home/agent"}}
	r := &resticSim{exists: true, snapshots: []backup.Snapshot{snap}}
	s.restic = r.run
	ctx := context.Background()
	var out strings.Builder
	if err := backup.Restore(ctx, e, backup.RestoreOptions{}, &out); err != nil {
		t.Fatal(err)
	}
	want := "/home/agent/keelbox-restore/cdcdcdcd"
	if f := s.Files[want]; f == nil || !f.Dir || f.Owner != "agent" {
		t.Fatalf("default target %s not made by the agent: %+v", want, f)
	}
	extract := r.calls[len(r.calls)-1]
	line := extract.String()
	if !strings.Contains(line, "/usr/sbin/runuser -u agent -- env -i") || !strings.Contains(line, "--directory="+want) || !strings.Contains(line, "/home-agent.tar") {
		t.Errorf("restore not extracted as the agent into %s: %s", want, line)
	}

	// A target with files in it is refused without --overwrite.
	s.Files["/home/agent/x"] = &system.FakeFile{Dir: true, Mode: 0o755, Owner: "agent", Group: "agent"}
	s.Files["/home/agent/x/old"] = &system.FakeFile{Data: []byte("old"), Mode: 0o644, Owner: "agent", Group: "agent"}
	if err := backup.Restore(ctx, e, backup.RestoreOptions{Target: "/home/agent/x"}, &out); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Errorf("non-empty target: %v", err)
	}
	if err := backup.Restore(ctx, e, backup.RestoreOptions{Overwrite: true}, &out); err != nil {
		t.Fatal(err)
	}
	if line := r.calls[len(r.calls)-1].String(); !strings.HasSuffix(line, "--directory=/home/agent") {
		t.Errorf("overwrite did not restore over the original path: %s", line)
	}
	if err := backup.Restore(ctx, e, backup.RestoreOptions{Overwrite: true, Target: "/tmp/x"}, &out); err == nil {
		t.Error("--overwrite with --target accepted")
	}
	if err := backup.Restore(ctx, e, backup.RestoreOptions{Snapshot: "ffff0000"}, &out); err == nil {
		t.Error("unknown snapshot accepted")
	}
}

func TestBackupDoctorReportsAge(t *testing.T) {
	s, e := backupSim(t, "")
	ctx := context.Background()
	level := func() engine.Level {
		for _, f := range engine.Doctor(ctx, e, All()) {
			if f.Module == "backup" && f.Check == "last-backup" {
				return f.Level
			}
		}
		t.Fatal("no backup/last-backup finding")
		return 0
	}
	if l := level(); l != engine.LevelWarn {
		t.Errorf("never backed up: %v", l)
	}
	write := func(st backup.Status) {
		b, _ := json.Marshal(st)
		s.Files[backup.StatusPath] = &system.FakeFile{Data: b, Mode: 0o600, Owner: "root", Group: "root"}
	}
	write(backup.Status{LastAttempt: time.Now(), LastSuccess: time.Now().Add(-time.Hour)})
	if l := level(); l != engine.LevelOK {
		t.Errorf("fresh backup: %v", l)
	}
	write(backup.Status{LastAttempt: time.Now(), LastSuccess: time.Now().Add(-72 * time.Hour), Error: "boom"})
	if l := level(); l != engine.LevelFail {
		t.Errorf("stale backup: %v", l)
	}
}
