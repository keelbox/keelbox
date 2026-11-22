package installs

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/events"
	"github.com/keelbox/keelbox/internal/logbook"
	"github.com/keelbox/keelbox/internal/notify"
	"github.com/keelbox/keelbox/internal/system"
)

// box is a fake Ubuntu as far as a package request goes: apt-cache policy,
// a simulated and a real apt-get install, and dpkg-query.
type box struct {
	*system.Fake
	installed map[string]bool
	// unknown names have no package; virtual ones a package without a
	// candidate.
	unknown, virtual map[string]bool
	// pattern maps a name apt does not know to the packages it matches as
	// a pattern, as apt-cache policy and apt-get install read one.
	pattern map[string][]string
	// brings are the packages installing a name also installs; upgrades
	// the installed ones it upgrades; removes the ones it would remove.
	brings, upgrades, removes map[string][]string
	// late are removals only the real install would do: the box changed
	// after the simulated run.
	late map[string][]string
	// broken names fail in the real install.
	broken map[string]bool
	now    time.Time
}

const testAdminKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAdmin admin@laptop"

// newBox is a box with users.agent.permissions mode and one apply in its
// logbook.
func newBox(t *testing.T, mode string) (*engine.Env, *box) {
	t.Helper()
	b := &box{Fake: system.NewFake(), installed: map[string]bool{"ufw": true, "libssl3": true, "docker-ce": true},
		unknown: map[string]bool{}, virtual: map[string]bool{}, pattern: map[string][]string{},
		brings: map[string][]string{}, upgrades: map[string][]string{}, removes: map[string][]string{}, broken: map[string]bool{},
		now: time.Date(2026, 11, 15, 14, 0, 0, 0, time.UTC)}
	b.AddUser("agent", 1001)
	b.AddFile("/etc/shadow", "root:*:20000::::::\n", 0o640)
	b.OnFunc("apt-cache policy", b.policy)
	b.OnFunc("apt-get", b.aptGet)
	b.OnFunc("dpkg-query", b.dpkgQuery)
	y := fmt.Sprintf("version: 1\nbox: acme\nusers:\n  admin:\n    ssh_authorized_keys: [%q]\n  agent:\n    permissions: %s\n", testAdminKey, mode)
	cfg, err := config.Parse([]byte(y), "acme.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := logbook.Append(b, logbook.Record{Time: b.now.Add(-time.Hour), Box: "acme", Keelbox: "test", Release: "2026.11.1", Result: "ok"}); err != nil {
		t.Fatal(err)
	}
	return &engine.Env{X: b, Cfg: cfg}, b
}

func (b *box) opts() Options { return Options{Now: func() time.Time { return b.now }} }

func exit(c system.Cmd, code int, stdout, stderr string) (system.Result, error) {
	r := system.Result{ExitCode: code, Stdout: stdout, Stderr: stderr}
	if code != 0 {
		return r, &system.ExitError{Cmd: c, Result: r}
	}
	return r, nil
}

func (b *box) stanza(n string) string {
	inst, cand := "(none)", "1.0-1"
	if b.installed[n] {
		inst = "1.0-1"
	}
	if b.virtual[n] {
		cand = "(none)"
	}
	return fmt.Sprintf("%s:\n  Installed: %s\n  Candidate: %s\n  Version table:\n     1.0-1 500\n        500 http://archive.ubuntu.com/ubuntu resolute/main amd64 Packages\n", n, inst, cand)
}

func (b *box) policy(c system.Cmd) (system.Result, error) {
	var out strings.Builder
	for _, n := range c.Args[2:] { // policy -- names
		switch {
		case b.pattern[n] != nil:
			for _, m := range b.pattern[n] {
				out.WriteString(b.stanza(m))
			}
		case !b.unknown[n]:
			out.WriteString(b.stanza(n))
		}
	}
	return exit(c, 0, out.String(), "")
}

func (b *box) aptGet(c system.Cmd) (system.Result, error) {
	var words, names []string
	simulate, noRemove, dashes := false, false, false
	for i := 0; i < len(c.Args); i++ {
		switch a := c.Args[i]; {
		case dashes:
			names = append(names, a)
		case a == "-o":
			i++
		case a == "--":
			dashes = true
		case a == "-s":
			simulate = true
		case a == "--no-remove":
			noRemove = true
		case !strings.HasPrefix(a, "-"):
			words = append(words, a)
		}
	}
	if len(words) == 0 || words[0] != "install" {
		return exit(c, 0, "", "") // update
	}
	var inst, remv []string
	for _, n := range names {
		if b.unknown[n] {
			return exit(c, 100, "", "E: Unable to locate package "+n)
		}
		for _, u := range b.upgrades[n] {
			inst = append(inst, "Inst "+u+" [0.9-1] (1.0-1 Ubuntu:26.04/resolute-updates [amd64])")
		}
		for _, d := range b.brings[n] {
			inst = append(inst, "Inst "+d+" (1.0-1 Ubuntu:26.04/resolute [amd64])")
		}
		inst = append(inst, "Inst "+n+" (1.0-1 Ubuntu:26.04/resolute [amd64])")
		remv = append(remv, b.removes[n]...)
		if !simulate {
			remv = append(remv, b.late[n]...)
		}
	}
	if len(remv) > 0 && noRemove {
		return exit(c, 100, "The following packages will be REMOVED:\n  "+strings.Join(remv, " ")+"\n0 upgraded, 1 newly installed, 1 to remove and 0 not upgraded.\n",
			"E: Packages need to be removed but remove is disabled.")
	}
	if simulate {
		out := "NOTE: This is only a simulation!\nReading package lists...\n"
		for _, r := range remv {
			out += "Remv " + r + " [1.0-1]\n"
		}
		return exit(c, 0, out+strings.Join(inst, "\n")+"\n", "")
	}
	for _, n := range names {
		if b.broken[n] {
			return exit(c, 100, "Setting up "+n+" ...\n", "dpkg: error processing package "+n+" (--configure):\nE: Sub-process /usr/bin/dpkg returned an error code (1)")
		}
	}
	for _, r := range remv {
		delete(b.installed, r)
	}
	for _, n := range names {
		b.installed[n] = true
		for _, d := range b.brings[n] {
			b.installed[d] = true
		}
	}
	return exit(c, 0, "", "")
}

func (b *box) dpkgQuery(c system.Cmd) (system.Result, error) {
	var out strings.Builder
	for _, n := range c.Args[2:] {
		if b.installed[n] {
			fmt.Fprintf(&out, "%s ii \n", n)
		}
	}
	return exit(c, 0, out.String(), "")
}

const requestFile = "/home/agent/" + AgentFile

// ask writes a request to the agent's file, as the agent, and returns its ID.
func (b *box) ask(t *testing.T, why string, pkgs ...string) string {
	t.Helper()
	id := NewID()
	b.writeRequest(t, Request{V: 1, ID: id, Time: b.now, Packages: pkgs, Why: why})
	return id
}

func (b *box) writeRequest(t *testing.T, r Request) {
	t.Helper()
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.UserFS("agent").WriteFile(requestFile, data, 0o600, "agent", "agent"); err != nil {
		t.Fatal(err)
	}
}

// installRan reports whether a real apt-get install ran, and with what.
func (b *box) installRan() (string, bool) {
	for _, c := range b.Calls {
		s := c.String()
		if c.Name == "apt-get" && strings.Contains(s, " install ") && !strings.Contains(s, " -s ") {
			return s, true
		}
	}
	return "", false
}

func isWaiting(e *engine.Env, now time.Time) bool {
	w, _ := Waiting(e, now)
	return w
}

func run(t *testing.T, e *engine.Env, b *box) Outcome {
	t.Helper()
	out, err := Run(context.Background(), e, b.opts())
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// rootID is root's ID for the request the agent filed under ref.
func rootID(t *testing.T, b *box, ref string) string {
	t.Helper()
	return entry(t, b, ref).ID
}

// entry is root's record of the request the agent filed under id.
func entry(t *testing.T, b *box, id string) Entry {
	t.Helper()
	st, err := ReadState(b)
	if err != nil {
		t.Fatal(err)
	}
	en := st.FindRef(id)
	if en == nil {
		t.Fatalf("no entry for request %s in %+v", id, st)
	}
	return *en
}

func installRecords(t *testing.T, b *box) []logbook.Record {
	t.Helper()
	recs, err := logbook.Read(b)
	if err != nil {
		t.Fatal(err)
	}
	var out []logbook.Record
	for _, r := range recs {
		if r.Kind == logbook.KindInstall {
			out = append(out, r)
		}
	}
	return out
}

func TestCheckNames(t *testing.T) {
	many := make([]string, MaxNames+1)
	for i := range many {
		many[i] = fmt.Sprintf("pkg%d", i)
	}
	for name, tc := range map[string]struct {
		in   []string
		code string // "" when the names pass
	}{
		"plain names":             {[]string{"jq", "libpq-dev", "g++", "libstdc++6", "python3.12", "0ad"}, ""},
		"a repeat is one":         {[]string{"jq", "jq"}, ""},
		"twenty names":            {many[:MaxNames], ""},
		"none":                    {nil, "no package named"},
		"21 names":                {many, "too many names"},
		"an option":               {[]string{"-y"}, "an option, not a package name"},
		"a long option":           {[]string{"--allow-unauthenticated"}, "an option, not a package name"},
		"an apt option":           {[]string{"-oDPkg::Pre-Invoke::=id"}, "an option, not a package name"},
		"an option after a name":  {[]string{"jq", "--reinstall"}, "an option, not a package name"},
		"a version":               {[]string{"jq=1.7"}, "not a package name"},
		"a release":               {[]string{"jq/noble-backports"}, "not a package name"},
		"an architecture":         {[]string{"jq:arm64"}, "not a package name"},
		"a local deb":             {[]string{"./evil.deb"}, "not a package name"},
		"an absolute path":        {[]string{"/tmp/evil.deb"}, "not a package name"},
		"a home path":             {[]string{"~/evil.deb"}, "not a package name"},
		"a URL":                   {[]string{"https://example.com/evil.deb"}, "not a package name"},
		"a semicolon":             {[]string{"jq;id"}, "not a package name"},
		"a command substitution":  {[]string{"$(id)"}, "not a package name"},
		"a backtick":              {[]string{"`id`"}, "not a package name"},
		"a pipe":                  {[]string{"jq|sh"}, "not a package name"},
		"a space":                 {[]string{"jq curl"}, "not a package name"},
		"a newline":               {[]string{"jq\nsudo"}, "not a package name"},
		"a tab":                   {[]string{"jq\tsudo"}, "not a package name"},
		"a NUL":                   {[]string{"jq\x00"}, "not a package name"},
		"an escape sequence":      {[]string{"\x1b[2Jjq"}, "not a package name"},
		"upper case":              {[]string{"JQ"}, "not a package name"},
		"a Cyrillic look-alike":   {[]string{"sud\u043e"}, "not a package name"},
		"a full-width look-alike": {[]string{"\uff4a\uff51"}, "not a package name"},
		"a zero-width joiner":     {[]string{"j\u200dq"}, "not a package name"},
		"a glob":                  {[]string{"lib*"}, "not a package name"},
		"a pattern":               {[]string{"?name(jq)"}, "not a package name"},
		"a tilde pattern":         {[]string{"~njq"}, "not a package name"},
		"a task":                  {[]string{"ubuntu-desktop^"}, "not a package name"},
		"one character":           {[]string{"j"}, "not a package name"},
		"empty":                   {[]string{""}, "not a package name"},
		"a leading dot":           {[]string{".jq"}, "not a package name"},
		"a leading plus":          {[]string{"+jq"}, "not a package name"},
		"too long":                {[]string{strings.Repeat("a", maxNameLen+1)}, "not a package name"},
		"sudo":                    {[]string{"sudo"}, "on the deny-list (privilege tools)"},
		"a deny-listed prefix":    {[]string{"jq", "docker-ce-cli"}, "on the deny-list (Docker engine and containerd)"},
		"a deny-listed suffix":    {[]string{"zfs-dkms"}, "on the deny-list (kernel and boot)"},
		"the ssh server":          {[]string{"openssh-server"}, "on the deny-list (remote login)"},
	} {
		got, refusal := CheckNames(tc.in)
		switch {
		case tc.code == "" && refusal != nil:
			t.Errorf("%s: refused: %s", name, refusal.Msg)
		case tc.code == "":
			if name == "a repeat is one" && len(got) != 1 {
				t.Errorf("%s: %q", name, got)
			}
		case refusal == nil:
			t.Errorf("%s: %q passed, want it refused as %q", name, tc.in, tc.code)
		case refusal.Code != tc.code:
			t.Errorf("%s: refused as %q (%s), want %q", name, refusal.Code, refusal.Msg, tc.code)
		default:
			// What the agent wrote is shown escaped, in ASCII, cut short.
			for _, r := range refusal.Msg {
				if r < 0x20 || r > 0x7e {
					t.Errorf("%s: the refusal shows a character that is not printable ASCII: %q", name, refusal.Msg)
					break
				}
			}
			if len(refusal.Msg) > 600 {
				t.Errorf("%s: the refusal is %d bytes long", name, len(refusal.Msg))
			}
		}
	}
}

// Every family the deny-list must cover has its packages refused, and
// ordinary development packages with similar names are not.
func TestDenyList(t *testing.T) {
	for _, n := range []string{
		"openssh-server", "ssh", "dropbear-bin", "ufw", "nftables", "iptables-persistent", "firewalld", "sudo", "sudo-rs", "opendoas", "pkexec",
		"docker.io", "docker-ce", "docker-ce-rootless-extras", "containerd.io", "containerd", "runc", "tailscale", "apparmor", "apparmor-profiles",
		"unattended-upgrades", "auto-apt-proxy", "systemd", "systemd-resolved", "systemd-oomd", "systemd-boot-efi", "dbus", "libpam-ssh", "libnss-extrausers",
		"cloud-init", "ec2-instance-connect", "google-guest-agent", "network-manager", "netplan.io", "linux-image-generic", "grub-pc", "dkms",
		"v4l2loopback-dkms", "incron", "cronie", "tinyproxy", "squid", "squid-openssl", "privoxy", "apt-cacher-ng", "lsh-server", "ttyd", "shellinabox",
		"openssh-server-gssapi", "arno-iptables-firewall", "pleaser",
	} {
		if _, denied := Denied(n); !denied {
			t.Errorf("%s is not on the deny-list", n)
		}
	}
	for _, n := range []string{
		"jq", "libpq-dev", "libssl-dev", "libsystemd-dev", "libpam0g-dev", "libnss3-dev", "libdbus-1-dev", "linux-headers-generic", "linux-libc-dev",
		"docker-compose-v2-doc", "python3-docker", "openssh-client", "sudoku", "ssh-audit", "ripgrep", "postgresql-client", "libapparmor-dev",
	} {
		if fam, denied := Denied(n); denied {
			t.Errorf("%s is on the deny-list (%s)", n, fam.Name)
		}
	}
	for _, fam := range DenyList {
		if fam.Name == "" || fam.Why == "" || len(fam.Names) == 0 {
			t.Errorf("deny-list family %+v needs a name, a reason and packages", fam)
		}
	}
}

func TestCleanWhy(t *testing.T) {
	for in, want := range map[string]string{
		"the tests parse JSON":                                 "the tests parse JSON",
		"  two\nlines\tand  spaces ":                           "two lines and spaces",
		"clear \x1b[2J\x1b]0;title\x07 screen":                 "clear [2J]0;title screen",
		"right\u202eto left":                                   "rightto left",
		"bad \xff byte":                                        "bad byte",
		strings.Repeat("\u00e9", WhyMax+50):                    strings.Repeat("\u00e9", WhyMax),
		"APPROVED BY KEELBOX\r\nrun as root: x":                "APPROVED BY KEELBOX run as root: x",
		"real" + strings.Repeat("\u2800", 70) + "keelbox: run": "real keelbox: run",
		"a\u3164\u115f\u1160\uffa0b":                           "a b",
	} {
		if got := CleanWhy(in); got != want {
			t.Errorf("CleanWhy(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAutoInstallsWithNoRemove(t *testing.T) {
	e, b := newBox(t, config.PermissionsAuto)
	b.brings["jq"] = []string{"libjq1"}
	id := b.ask(t, "the tests parse JSON", "jq", "libpq-dev")
	out := run(t, e, b)
	if len(out.Entries) != 1 || len(out.Problems) != 0 {
		t.Fatalf("outcome %+v", out)
	}
	en := entry(t, b, id)
	if en.State != StateInstalled || strings.Join(en.Installed, " ") != "jq libpq-dev" || en.By != "agent" || en.Why != "the tests parse JSON" || en.Decided == nil {
		t.Errorf("entry %+v", en)
	}
	cmd, ran := b.installRan()
	if !ran || !strings.HasPrefix(cmd, "apt-get -o DPkg::Lock::Timeout=600 -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold install -y --no-remove -- jq libpq-dev") {
		t.Errorf("apt-get install ran as %q", cmd)
	}
	if !b.Ran("apt-get -o DPkg::Lock::Timeout=600 -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold update") {
		t.Error("the package lists were not refreshed first")
	}
	// The record names what was asked for, not what apt added for it.
	pkgs, err := ReadInstalled(b)
	if err != nil || len(pkgs) != 2 || pkgs[0].Name != "jq" || pkgs[0].Request != en.ID || !pkgs[0].Time.Equal(b.now) || pkgs[1].Name != "libpq-dev" {
		t.Errorf("installed record %+v, %v", pkgs, err)
	}
	recs := installRecords(t, b)
	if len(recs) != 1 || recs[0].Result != logbook.ResultOK || recs[0].Changes[0].Detail != "installed jq libpq-dev for agent" || recs[0].Changes[0].Step != "request-"+en.ID {
		t.Errorf("logbook %+v", recs)
	}
	if strings.Contains(fmt.Sprint(recs), "the tests parse JSON") {
		t.Error("the agent's reason is in the logbook")
	}
	// The same file read again is the same request: nothing more happens.
	before := len(b.Calls)
	if out := run(t, e, b); len(out.Entries) != 0 {
		t.Errorf("a request already seen was taken again: %+v", out)
	}
	for _, c := range b.Calls[before:] {
		if c.Name == "apt-get" || c.Name == "apt-cache" {
			t.Errorf("a request already seen ran %s", c)
		}
	}
	// A second request within the hour does not refresh the lists again.
	b.ask(t, "", "ripgrep")
	before = len(b.Calls)
	run(t, e, b)
	for _, c := range b.Calls[before:] {
		if c.Name == "apt-get" && strings.HasSuffix(c.String(), " update") {
			t.Error("the package lists were refreshed twice within the hour")
		}
	}
}

func TestAlreadyInstalledAsksNothingOfApt(t *testing.T) {
	for _, mode := range config.PermissionModes {
		e, b := newBox(t, mode)
		b.installed["jq"] = true
		id := b.ask(t, "", "jq")
		notified := 0
		o := b.opts()
		o.Notify = func(Entry) { notified++ }
		if _, err := Run(context.Background(), e, o); err != nil {
			t.Fatal(err)
		}
		en := entry(t, b, id)
		if en.State != StateInstalled || len(en.Installed) != 0 || notified != 0 {
			t.Errorf("%s: entry %+v, %d notified", mode, en, notified)
		}
		if cmd, ran := b.installRan(); ran {
			t.Errorf("%s: apt-get ran for a package that is installed: %s", mode, cmd)
		}
		if pkgs, _ := ReadInstalled(b); len(pkgs) != 0 {
			t.Errorf("%s: a package that was there already is recorded as the agent's: %+v", mode, pkgs)
		}
	}
}

// Requests that reach root and are refused by what the box says: nothing
// is installed, the reason is recorded for the agent, and the logbook gets
// fixed words.
func TestRunRefuses(t *testing.T) {
	for name, tc := range map[string]struct {
		pkgs  []string
		setup func(b *box)
		code  string
		says  string
	}{
		"no such package":         {[]string{"jqq"}, func(b *box) { b.unknown["jqq"] = true }, "no install candidate", "apt has no package named jqq"},
		"no candidate":            {[]string{"awk"}, func(b *box) { b.virtual["awk"] = true }, "no install candidate", "awk has no install candidate"},
		"a pattern apt expands":   {[]string{"lib."}, func(b *box) { b.pattern["lib."] = []string{"liba", "libb"} }, "no install candidate", "apt has no package named lib."},
		"a removal suffix":        {[]string{"ufw-"}, func(b *box) { b.unknown["ufw-"] = true }, "no install candidate", "apt has no package named ufw-"},
		"would remove":            {[]string{"iptables-nft-compat"}, func(b *box) { b.removes["iptables-nft-compat"] = []string{"ufw"} }, "would remove a package", "would remove ufw"},
		"brings a denied package": {[]string{"docker-compose-v2"}, func(b *box) { b.brings["docker-compose-v2"] = []string{"docker.io", "containerd"} }, "on the deny-list (Docker engine and containerd)", "would also install or upgrade docker.io"},
		"upgrades a denied one":   {[]string{"some-tool"}, func(b *box) { b.upgrades["some-tool"] = []string{"docker-ce"} }, "on the deny-list (Docker engine and containerd)", "would also install or upgrade docker-ce"},
		"a deny-listed name":      {[]string{"jq", "sudo"}, nil, "on the deny-list (privilege tools)", "sudo is on keelbox's deny-list"},
		"a version":               {[]string{"jq=1.7"}, nil, "not a package name", `"jq=1.7" is not a package name`},
		"an option":               {[]string{"--reinstall", "jq"}, nil, "an option, not a package name", `"--reinstall" is an option`},
		"a desktop":               {[]string{"everything"}, func(b *box) { b.brings["everything"] = manyNames(transactionMax) }, "too many packages", "over 300"},
	} {
		for _, mode := range config.PermissionModes {
			e, b := newBox(t, mode)
			if tc.setup != nil {
				tc.setup(b)
			}
			id := b.ask(t, "please", tc.pkgs...)
			run(t, e, b)
			en := entry(t, b, id)
			if en.State != StateRefused || !strings.Contains(en.Detail, tc.says) || en.Decided == nil {
				t.Errorf("%s (%s): entry %+v, want refused saying %q", name, mode, en, tc.says)
			}
			if cmd, ran := b.installRan(); ran {
				t.Errorf("%s (%s): apt-get install ran: %s", name, mode, cmd)
			}
			recs := installRecords(t, b)
			if len(recs) != 1 || !strings.HasPrefix(recs[0].Changes[0].Detail, "refused agent's request: "+tc.code) || recs[0].Result != logbook.ResultOK {
				t.Errorf("%s (%s): logbook %+v, want refused: %s", name, mode, recs, tc.code)
			}
			if pkgs, _ := ReadInstalled(b); len(pkgs) != 0 {
				t.Errorf("%s (%s): installed record %+v", name, mode, pkgs)
			}
		}
	}
}

func manyNames(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("lib%d", i)
	}
	return out
}

// An upgrade of a dependency that is not deny-listed goes through: a -dev
// package often needs the newer version of the library it belongs to.
func TestDependencyUpgradeIsAllowed(t *testing.T) {
	e, b := newBox(t, config.PermissionsAuto)
	b.upgrades["libssl-dev"] = []string{"libssl3"}
	id := b.ask(t, "", "libssl-dev")
	run(t, e, b)
	if en := entry(t, b, id); en.State != StateInstalled || strings.Join(en.Installed, " ") != "libssl-dev" {
		t.Errorf("entry %+v", en)
	}
}

// A request file root cannot take as data is left alone: a link is not
// followed, a huge file is not read, and nothing of what they hold or
// point at gets into root's records.
func TestRequestFileGuards(t *testing.T) {
	valid := func(b *box) []byte {
		data, _ := json.Marshal(Request{V: 1, ID: "0a1b2c3d", Time: b.now, Packages: []string{"jq"}})
		return data
	}
	for name, plant := range map[string]func(b *box){
		"a link to a root file": func(b *box) {
			b.Files[requestFile] = &system.FakeFile{Target: "/etc/shadow", Owner: "agent", Group: "agent"}
		},
		"a link to a request of the agent's own": func(b *box) {
			b.Files["/home/agent/real.json"] = &system.FakeFile{Data: valid(b), Mode: 0o600, Owner: "agent", Group: "agent"}
			b.Files[requestFile] = &system.FakeFile{Target: "/home/agent/real.json", Owner: "agent", Group: "agent"}
		},
		"a huge file": func(b *box) {
			huge := append(valid(b), []byte(strings.Repeat(" ", requestMax))...)
			b.Files[requestFile] = &system.FakeFile{Data: huge, Mode: 0o600, Owner: "agent", Group: "agent"}
		},
		"a FIFO": func(b *box) {
			b.Files[requestFile] = &system.FakeFile{Special: true, Mode: 0o600, Owner: "agent", Group: "agent"}
		},
		"a directory": func(b *box) {
			b.Files[requestFile] = &system.FakeFile{Dir: true, Mode: 0o700, Owner: "agent", Group: "agent"}
		},
		"a second document after the request": func(b *box) {
			b.Files[requestFile] = &system.FakeFile{Data: append(valid(b), []byte(` {"packages":["sudo"]}`)...), Mode: 0o600, Owner: "agent", Group: "agent"}
		},
		"not JSON": func(b *box) {
			b.Files[requestFile] = &system.FakeFile{Data: []byte("jq\n"), Mode: 0o600, Owner: "agent", Group: "agent"}
		},
		"an unknown field": func(b *box) {
			b.Files[requestFile] = &system.FakeFile{Data: []byte(`{"v":1,"id":"0a1b2c3d","time":"2026-11-15T14:00:00Z","packages":["jq"],"state":"installed"}`), Mode: 0o600, Owner: "agent", Group: "agent"}
		},
		"another version": func(b *box) {
			b.Files[requestFile] = &system.FakeFile{Data: []byte(`{"v":2,"id":"0a1b2c3d","time":"2026-11-15T14:00:00Z","packages":["jq"]}`), Mode: 0o600, Owner: "agent", Group: "agent"}
		},
		"an ID that is a path": func(b *box) {
			b.Files[requestFile] = &system.FakeFile{Data: []byte(`{"v":1,"id":"../../etc","time":"2026-11-15T14:00:00Z","packages":["jq"]}`), Mode: 0o600, Owner: "agent", Group: "agent"}
		},
		"an old request": func(b *box) {
			data, _ := json.Marshal(Request{V: 1, ID: "0a1b2c3d", Time: b.now.Add(-time.Hour), Packages: []string{"jq"}})
			b.Files[requestFile] = &system.FakeFile{Data: data, Mode: 0o600, Owner: "agent", Group: "agent"}
		},
		"a request from the future": func(b *box) {
			data, _ := json.Marshal(Request{V: 1, ID: "0a1b2c3d", Time: b.now.Add(time.Hour), Packages: []string{"jq"}})
			b.Files[requestFile] = &system.FakeFile{Data: data, Mode: 0o600, Owner: "agent", Group: "agent"}
		},
	} {
		e, b := newBox(t, config.PermissionsAuto)
		plant(b)
		// Not work for the service, and the journal is told why.
		if w, problem := Waiting(e, b.now); w || problem == "" || strings.Contains(problem, "root:*") {
			t.Errorf("%s: waiting %v, problem %q", name, w, problem)
		}
		out := run(t, e, b)
		if len(out.Entries) != 0 || len(out.Problems) != 1 {
			t.Errorf("%s: outcome %+v, want no entry and one problem", name, out)
		}
		if strings.Contains(fmt.Sprint(out.Problems), "root:*") {
			t.Errorf("%s: the problem carries the linked file: %q", name, out.Problems)
		}
		for _, c := range b.Calls {
			if c.Name == "apt-get" || c.Name == "apt-cache" {
				t.Errorf("%s: ran %s", name, c)
			}
		}
		if st, _ := ReadState(b); len(st.Requests) != 0 {
			t.Errorf("%s: recorded %+v", name, st.Requests)
		}
		if len(installRecords(t, b)) != 0 {
			t.Errorf("%s: wrote the logbook", name)
		}
	}
	// No file at all is no request and no problem.
	e, b := newBox(t, config.PermissionsAuto)
	if out := run(t, e, b); len(out.Entries) != 0 || len(out.Problems) != 0 || isWaiting(e, b.now) {
		t.Errorf("no file: %+v", out)
	}
}

func TestManualWaitsForApproval(t *testing.T) {
	e, b := newBox(t, config.PermissionsManual)
	id := b.ask(t, "the tests \x1b[2Jparse JSON", "jq", "libpq-dev")
	var notified []Entry
	o := b.opts()
	o.Notify = func(en Entry) { notified = append(notified, en) }
	if _, err := Run(context.Background(), e, o); err != nil {
		t.Fatal(err)
	}
	en := entry(t, b, id)
	if en.State != StatePending || en.Why != "the tests [2Jparse JSON" || en.Decided != nil {
		t.Errorf("entry %+v", en)
	}
	if cmd, ran := b.installRan(); ran {
		t.Errorf("manual installed without approval: %s", cmd)
	}
	if len(notified) != 1 || notified[0].Ref != id {
		t.Errorf("notified %+v", notified)
	}
	if recs := installRecords(t, b); len(recs) != 1 || recs[0].Changes[0].Detail != "agent asked for jq libpq-dev; waiting for approval" {
		t.Errorf("logbook %+v", recs)
	}
	// The event names the packages and the command, never the reason.
	ev := PendingEvent("acme", en, b.now)
	if err := ev.Validate(); err != nil {
		t.Fatal(err)
	}
	if ev.Level != events.Warn || ev.Fields["packages"] != "jq, libpq-dev" || ev.Fields["fix"] != "keelbox approve acme "+en.ID || strings.Contains(fmt.Sprint(ev), "parse JSON") {
		t.Errorf("event %+v", ev)
	}
	if m := notify.Render(ev); m.Title != "acme · keelbox" || m.Text != "the agent asks to install jq, libpq-dev. Approve: keelbox approve acme "+en.ID {
		t.Errorf("message %q: %q", m.Title, m.Text)
	}

	// Approving another ID does nothing.
	if _, err := Approve(e, "ffffffff", false, b.opts()); err != ErrNoRequest {
		t.Errorf("approve of an unknown ID: %v", err)
	}
	// A run of the service with nothing approved leaves it waiting.
	run(t, e, b)
	if en := entry(t, b, id); en.State != StatePending {
		t.Errorf("after another run: %+v", en)
	}
	b.now = b.now.Add(10 * time.Minute)
	got, err := Approve(e, rootID(t, b, id), false, b.opts())
	if err != nil || len(got) != 1 || got[0].State != StateApproved || !got[0].Approved {
		t.Fatalf("approve: %+v, %v", got, err)
	}
	// Approving installs nothing by itself: the service does, at its next
	// run, and approving again before that names the same request.
	if cmd, ran := b.installRan(); ran {
		t.Errorf("approve ran apt-get itself: %s", cmd)
	}
	if !isWaiting(e, b.now) {
		t.Error("an approved request is not work for the service")
	}
	if again, err := Approve(e, rootID(t, b, id), false, b.opts()); err != nil || len(again) != 1 {
		t.Errorf("approve again before the install: %+v, %v", again, err)
	}
	out := run(t, e, b)
	if len(out.Entries) != 1 || out.Entries[0].State != StateInstalled || !out.Entries[0].Approved {
		t.Fatalf("the run after approve: %+v", out)
	}
	if cmd, ran := b.installRan(); !ran || !strings.Contains(cmd, " install -y --no-remove -- jq libpq-dev") {
		t.Errorf("the approved install ran %q", cmd)
	}
	recs := installRecords(t, b)
	if last := recs[len(recs)-1].Changes[0].Detail; last != "installed jq libpq-dev for agent, approved by the owner" {
		t.Errorf("logbook after approve: %q", last)
	}
	if pkgs, _ := ReadInstalled(b); len(pkgs) != 2 {
		t.Errorf("installed record %+v", pkgs)
	}
	// Installed: it no longer waits.
	if _, err := Approve(e, rootID(t, b, id), false, b.opts()); err != ErrNoRequest {
		t.Errorf("approve after the install: %v", err)
	}
	if isWaiting(e, b.now) {
		t.Error("the service still has work")
	}
}

// An approved request gets the same checks as one the service takes
// straight from the agent: what changed on the box since it was recorded
// counts.
func TestApprovedRequestIsCheckedAgain(t *testing.T) {
	e, b := newBox(t, config.PermissionsManual)
	id := b.ask(t, "", "some-tool")
	run(t, e, b)
	if en := entry(t, b, id); en.State != StatePending {
		t.Fatalf("entry %+v", en)
	}
	b.removes["some-tool"] = []string{"ufw"}
	if _, err := Approve(e, rootID(t, b, id), false, b.opts()); err != nil {
		t.Fatal(err)
	}
	run(t, e, b)
	if en := entry(t, b, id); en.State != StateRefused || !strings.Contains(en.Detail, "would remove ufw") || !en.Approved {
		t.Fatalf("after approve: %+v", en)
	}
	if cmd, ran := b.installRan(); ran {
		t.Errorf("approve installed what would remove ufw: %s", cmd)
	}
	if !b.installed["ufw"] {
		t.Error("ufw was removed")
	}
}

func TestDenyAndApproveAll(t *testing.T) {
	e, b := newBox(t, config.PermissionsManual)
	var ids []string
	for _, p := range []string{"jq", "ripgrep", "tree"} {
		ids = append(ids, b.ask(t, "", p))
		run(t, e, b)
		b.now = b.now.Add(time.Second)
	}
	en, err := Deny(e, rootID(t, b, ids[1]), b.opts())
	if err != nil || en.State != StateDenied {
		t.Fatalf("deny: %+v, %v", en, err)
	}
	if _, err := Deny(e, rootID(t, b, ids[1]), b.opts()); err != ErrNoRequest {
		t.Errorf("a second deny: %v", err)
	}
	got, err := Approve(e, "", true, b.opts())
	if err != nil || len(got) != 2 {
		t.Fatalf("approve --all: %+v, %v", got, err)
	}
	// One run of the service installs every approved request.
	if out := run(t, e, b); len(out.Entries) != 2 {
		t.Fatalf("the run after approve --all: %+v", out)
	}
	if !b.installed["jq"] || !b.installed["tree"] || b.installed["ripgrep"] {
		t.Errorf("installed %v", b.installed)
	}
	st, _ := ReadState(b)
	if len(st.Pending()) != 0 {
		t.Errorf("still pending: %+v", st.Pending())
	}
	recs := installRecords(t, b)
	found := false
	for _, r := range recs {
		found = found || r.Changes[0].Detail == "the owner denied agent's request for ripgrep"
	}
	if !found {
		t.Errorf("no denied record in %+v", recs)
	}
	// Nothing waiting: approve --all is not an error.
	if got, err := Approve(e, "", true, b.opts()); err != nil || len(got) != 0 {
		t.Errorf("approve --all with nothing waiting: %+v, %v", got, err)
	}
}

// A request approved by mistake can be denied until the service begins it.
func TestDenyAfterApprove(t *testing.T) {
	e, b := newBox(t, config.PermissionsManual)
	id := b.ask(t, "", "sl")
	run(t, e, b)
	if _, err := Approve(e, rootID(t, b, id), false, b.opts()); err != nil {
		t.Fatal(err)
	}
	if en, err := Deny(e, rootID(t, b, id), b.opts()); err != nil || en.State != StateDenied {
		t.Fatalf("deny after approve: %+v, %v", en, err)
	}
	run(t, e, b)
	if cmd, ran := b.installRan(); ran || b.installed["sl"] {
		t.Errorf("a denied request was installed: %s", cmd)
	}
	if isWaiting(e, b.now) {
		t.Error("the service still has work")
	}
}

// A request left as installing by a run that was cut off is closed as
// failed by the next run, with what to do, and is not installed twice.
func TestCutOffInstallIsClosed(t *testing.T) {
	e, b := newBox(t, config.PermissionsAuto)
	id := b.ask(t, "", "jq")
	run(t, e, b)
	st, _ := ReadState(b)
	en := *st.FindRef(id)
	en.State, en.Decided = StateInstalling, nil
	st.set(en)
	if err := st.write(b); err != nil {
		t.Fatal(err)
	}
	if !isWaiting(e, b.now) {
		t.Error("a cut-off install is not work for the service")
	}
	before := len(b.Calls)
	run(t, e, b)
	got := entry(t, b, id)
	if got.State != StateFailed || !strings.Contains(got.Detail, "sudo dpkg --configure -a") || !strings.Contains(got.Detail, "keelbox ssh acme --admin") {
		t.Errorf("entry %+v", got)
	}
	for _, c := range b.Calls[before:] {
		if c.Name == "apt-get" {
			t.Errorf("the next run ran %s", c)
		}
	}
}

// Every change to root's record is made under the installs lock, and
// apt-get runs without it.
func TestLockIsNotHeldDuringTheInstall(t *testing.T) {
	e, b := newBox(t, config.PermissionsAuto)
	held, taken := 0, 0
	o := b.opts()
	o.Lock = func() (func(), error) {
		held++
		taken++
		return func() { held-- }, nil
	}
	b.OnFunc("apt-get", func(c system.Cmd) (system.Result, error) {
		s := c.String()
		if strings.Contains(s, " install ") && !strings.Contains(s, " -s ") && held != 0 {
			t.Errorf("the installs lock is held while apt-get installs")
		}
		return b.aptGet(c)
	})
	b.ask(t, "", "jq")
	if _, err := Run(context.Background(), e, o); err != nil {
		t.Fatal(err)
	}
	if !b.installed["jq"] || held != 0 || taken < 2 {
		t.Errorf("installed %v, lock held %d, taken %d", b.installed["jq"], held, taken)
	}
}

// A request that would remove a package fails at the install too, should
// the simulated run not have shown it: apt-get runs with --no-remove.
func TestNoRemoveHoldsAtTheInstall(t *testing.T) {
	e, b := newBox(t, config.PermissionsAuto)
	b.late = map[string][]string{"some-tool": {"ufw"}}
	id := b.ask(t, "", "some-tool")
	run(t, e, b)
	en := entry(t, b, id)
	if en.State != StateFailed || !strings.Contains(en.Detail, "Packages need to be removed but remove is disabled") {
		t.Errorf("entry %+v", en)
	}
	if !b.installed["ufw"] || b.installed["some-tool"] {
		t.Errorf("installed after a refused removal: %v", b.installed)
	}
}

func TestInstallFailureIsRecorded(t *testing.T) {
	e, b := newBox(t, config.PermissionsAuto)
	b.broken["brokenthing"] = true
	id := b.ask(t, "", "brokenthing")
	run(t, e, b)
	en := entry(t, b, id)
	if en.State != StateFailed || !strings.Contains(en.Detail, "Sub-process /usr/bin/dpkg returned an error code (1)") || len(en.Installed) != 0 {
		t.Errorf("entry %+v", en)
	}
	recs := installRecords(t, b)
	if len(recs) != 1 || recs[0].Result != logbook.ResultFailed || recs[0].Error != "apt-get install failed" || strings.Contains(fmt.Sprint(recs[0]), "dpkg") {
		t.Errorf("logbook %+v: want a failed record without apt's output", recs)
	}
	if pkgs, _ := ReadInstalled(b); len(pkgs) != 0 {
		t.Errorf("a failed install is recorded as installed: %+v", pkgs)
	}
}

func TestBudgets(t *testing.T) {
	e, b := newBox(t, config.PermissionsManual)
	for i := 0; i < MaxPending; i++ {
		b.ask(t, "", fmt.Sprintf("pkg%d", i))
		run(t, e, b)
		b.now = b.now.Add(time.Second)
	}
	id := b.ask(t, "", "one-more")
	run(t, e, b)
	if en := entry(t, b, id); en.State != StateRefused || !strings.Contains(en.Detail, "already wait for the owner") {
		t.Errorf("request %d: %+v", MaxPending+1, en)
	}
	for i := MaxPending + 1; i < HourMax; i++ {
		b.ask(t, "", "jq")
		run(t, e, b)
	}
	id = b.ask(t, "", "jq")
	run(t, e, b)
	if en := entry(t, b, id); en.State != StateRefused || !strings.Contains(en.Detail, "requests in the last hour already") {
		t.Errorf("request %d in the hour: %+v", HourMax+1, en)
	}
	// A file rewritten without end gets an answer each time, and one line
	// in the logbook, not one a request.
	for i := 0; i < 2*historyMax; i++ {
		id = b.ask(t, "", "jq")
		run(t, e, b)
		b.now = b.now.Add(2 * time.Second)
	}
	if en := entry(t, b, id); en.State != StateRefused {
		t.Errorf("a request past the budget has no answer: %+v", en)
	}
	over := 0
	for _, r := range installRecords(t, b) {
		if strings.Contains(r.Changes[0].Detail, "too many requests") && !strings.Contains(r.Changes[0].Detail, "waiting") {
			over++
		}
	}
	if over != 1 {
		t.Errorf("%d logbook records for requests past the hour's budget, want 1", over)
	}
	// An hour on, requests are taken again, and the ones waiting are kept
	// however many were decided since.
	b.now = b.now.Add(2 * time.Hour)
	for i := 0; i < historyMax+10 && i < HourMax-1; i++ {
		b.ask(t, "", "sudo")
		run(t, e, b)
	}
	st, _ := ReadState(b)
	if len(st.Pending()) != MaxPending {
		t.Errorf("%d waiting after the history was cut, want %d", len(st.Pending()), MaxPending)
	}
	done := 0
	for _, r := range st.Requests {
		if r.Done() {
			done++
		}
	}
	if done > historyMax {
		t.Errorf("%d decided requests kept, over %d", done, historyMax)
	}
}

// The event for the longest request there can be still passes the event
// checks: a notification names a few packages and counts the rest.
func TestPendingEventFitsTheLongestRequest(t *testing.T) {
	var names []string
	for i := 0; i < MaxNames; i++ {
		names = append(names, strings.Repeat("a", maxNameLen-2)+fmt.Sprintf("%02d", i))
	}
	if _, refusal := CheckNames(names); refusal != nil {
		t.Fatal(refusal.Msg)
	}
	ev := PendingEvent("acme", Entry{ID: "0a1b2c3d", Packages: names}, time.Now())
	if err := ev.Validate(); err != nil {
		t.Errorf("event for %d names of %d characters: %v", MaxNames, maxNameLen, err)
	}
	if !strings.HasSuffix(ev.Fields["packages"], " and 14 more") {
		t.Errorf("packages field: %q", ev.Fields["packages"])
	}
}

func TestPace(t *testing.T) {
	_, b := newBox(t, config.PermissionsAuto)
	if w := Pace(b, b.now); w != 0 {
		t.Errorf("first run waits %s", w)
	}
	if w := Pace(b, b.now.Add(500*time.Millisecond)); w != RunPace-500*time.Millisecond {
		t.Errorf("a run half a second later waits %s", w)
	}
	if w := Pace(b, b.now.Add(time.Minute)); w != 0 {
		t.Errorf("a run a minute later waits %s", w)
	}
}

func TestUnlistedPackagesAndTheirYAML(t *testing.T) {
	e, b := newBox(t, config.PermissionsAuto)
	e.Cfg.Host.Packages = []string{"htop", "libpq-dev"}
	for _, p := range []string{"jq", "libpq-dev", "tree"} {
		b.ask(t, "", p)
		run(t, e, b)
		b.now = b.now.Add(time.Second)
	}
	// A package the admin removed since is no longer listed.
	delete(b.installed, "tree")
	still := StillInstalled(context.Background(), e)
	if strings.Join(still, " ") != "jq libpq-dev" {
		t.Errorf("still installed: %q", still)
	}
	un := Unlisted(still, []string{"curl"}, e.Cfg)
	if strings.Join(un, " ") != "jq" {
		t.Errorf("unlisted: %q", un)
	}
	want := "host:\n  packages:\n    - htop\n    - libpq-dev\n    - jq\n"
	got := PackagesYAML(e.Cfg, un)
	if got != want {
		t.Errorf("YAML:\n%s\nwant:\n%s", got, want)
	}
	// The YAML is the config's own: it parses with the box's other keys.
	y := fmt.Sprintf("version: 1\nbox: acme\nusers:\n  admin:\n    ssh_authorized_keys: [%q]\n%s", testAdminKey, got)
	c, err := config.Parse([]byte(y), "acme.yaml")
	if err != nil || len(Unlisted(still, nil, c)) != 0 {
		t.Errorf("the printed YAML does not list them: %v, %v", c, err)
	}
	if Told(b, un) {
		t.Error("told before apply said it")
	}
	if !Told(b, un) {
		t.Error("not remembered as told")
	}
	if Told(b, []string{"jq", "tree"}) {
		t.Error("another set counts as told")
	}
}

func TestParsePolicy(t *testing.T) {
	out := `jq:
  Installed: 1.8.1-4ubuntu2
  Candidate: 1.8.1-4ubuntu2
  Version table:
 *** 1.8.1-4ubuntu2 500
        500 http://gb.archive.ubuntu.com/ubuntu resolute-updates/main amd64 Packages
        100 /var/lib/dpkg/status
awk:
  Installed: (none)
  Candidate: (none)
  Version table:
sl:
  Installed: (none)
  Candidate: 5.02-1build1
  Version table:
     5.02-1build1 500
        500 http://gb.archive.ubuntu.com/ubuntu resolute/universe amd64 Packages
`
	pol := parsePolicy(out)
	if len(pol) != 3 || pol["jq"] != (policy{"1.8.1-4ubuntu2", "1.8.1-4ubuntu2"}) || pol["awk"] != (policy{"(none)", "(none)"}) || pol["sl"] != (policy{"(none)", "5.02-1build1"}) {
		t.Errorf("policy %+v", pol)
	}
	if len(parsePolicy("")) != 0 {
		t.Error("no output parsed as a package")
	}
}

func TestParseSimulation(t *testing.T) {
	out := `NOTE: This is only a simulation!
      apt-get needs root privileges for real execution.
Reading package lists...
The following NEW packages will be installed:
  cowsay sl
0 upgraded, 2 newly installed, 0 to remove and 58 not upgraded.
Inst libssl3t64 [3.0.13-0ubuntu3.1] (3.0.13-0ubuntu3.4 Ubuntu:24.04/noble-updates [amd64])
Inst cowsay (3.03+dfsg2-8build1 Ubuntu:26.04/resolute [all])
Inst sl:amd64 (5.02-1build1 Ubuntu:26.04/resolute [amd64])
Conf cowsay (3.03+dfsg2-8build1 Ubuntu:26.04/resolute [all])
Remv ufw [0.36.2-9build1]
Purg old-thing [1.0]
`
	tr := parseSimulation(out)
	if strings.Join(tr.install, " ") != "libssl3t64 cowsay sl" || strings.Join(tr.remove, " ") != "old-thing ufw" {
		t.Errorf("transaction %+v", tr)
	}
	stopped := `The following packages will be REMOVED:
  containerd.io docker-ce
  docker-ce-cli*
The following NEW packages will be installed:
  docker.io runc
0 upgraded, 7 newly installed, 3 to remove and 55 not upgraded.
`
	if tr := parseSimulation(stopped); strings.Join(tr.remove, " ") != "containerd.io docker-ce docker-ce-cli" || len(tr.install) != 0 {
		t.Errorf("transaction apt stopped at: %+v", tr)
	}
}

func TestModeFile(t *testing.T) {
	e, _ := newBox(t, config.PermissionsManual)
	m, err := ParseMode(ModeFile(e.Cfg))
	if err != nil || m != (Mode{Box: "acme", Agent: "agent", Mode: "manual"}) {
		t.Errorf("mode %+v, %v", m, err)
	}
	for _, bad := range []string{"", "{}", `{"agent":"agent","mode":"root"}`, `{"mode":"auto"}`} {
		if _, err := ParseMode([]byte(bad)); err == nil {
			t.Errorf("ParseMode(%q) passed", bad)
		}
	}
}
