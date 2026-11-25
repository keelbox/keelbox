package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/prompt"
	"github.com/keelbox/keelbox/internal/provider"
	"github.com/keelbox/keelbox/internal/provider/aws"
)

// A cloud is a provider that creates and deletes the box's server through
// its API (docs/contributing/add-a-provider.md). Everything between create
// and destroy is the same for each: pin the injected host key, wait for
// SSH, rotate the host keys, then the ssh provider's apply.

// cloudServer is a box's server as its cloud reports it.
type cloudServer struct {
	// ref names the server in messages: "server 42".
	ref string
	// about is ref with its address, for the question destroy asks.
	about string
	// ip is its public IPv4 address.
	ip string
	// stopped is set when the cloud reports the server shut down.
	stopped bool
	// raw is the provider's own record of it.
	raw any
}

// cloud is what the commands need from a provider that creates servers.
type cloud interface {
	// title names the provider in the doctor heading: "hetzner".
	title() string
	// bootstrapUser is the account a new server's image logs in as; the
	// first apply runs as it and then turns its login off. Empty when the
	// server boots with the admin account itself.
	bootstrapUser() string
	// create makes the server for the box, with hk as its first host key,
	// and returns it once it runs and has an address. It fails when the
	// server exists. A server returned with an error may exist.
	create(ctx context.Context, hk provider.HostKeyPair, logf func(string, ...any)) (*cloudServer, error)
	// find returns the box's server, only when keelbox created it.
	find(ctx context.Context) (*cloudServer, error)
	// destroy deletes the server and everything create made with it, and
	// says each deletion.
	destroy(ctx context.Context, srv *cloudServer, logf func(string, ...any)) error
	// sweep removes what is left of a box that has no server: what an
	// interrupted create made, what park kept for resume, and any snapshot
	// with the box's parked labels, such as one an interrupted park left
	// unfinished. destroy calls it once nothing parked is left to ask about.
	sweep(ctx context.Context, logf func(string, ...any))
	// firewall makes the provider's own firewall for the server exist and
	// admit SSH or not; publicSSH nil keeps what it admits now. It returns
	// each change made and a summary of the rules.
	firewall(ctx context.Context, srv *cloudServer, publicSSH *bool) (changes []string, summary string, err error)
	// doctor checks the provider's side from this machine and prints its
	// rows. publicSSH reads whether the box's public SSH is open.
	doctor(ctx context.Context, srv *cloudServer, publicSSH func() (bool, error), row func(level, check, detail string)) int

	// The rest is what keelbox resize, park and resume need
	// (docs/adr/0071-resize-park-resume.md). The commands in resize.go and
	// park.go own the order of the calls, the questions, the messages and
	// what this computer records; a provider owns only the calls to its
	// cloud. Each call that changes or deletes something first checks that
	// it carries keelbox's labels for this box, and none of them puts a
	// credential in an error.

	// sizeKey is the config key that holds the server's size, as a dotted
	// path: "target.hetzner.server_type". resize rewrites it.
	sizeKey() string
	// sizes lists the sizes srv can change to without losing what is on
	// its disk, cheapest first, with the cloud's prices. The size it has
	// now is in the list and marked. Where the cloud can change a server
	// back down, the list holds only sizes that leave that possible (on
	// Hetzner the disk never grows); a cloud that cannot go back says so
	// in note, the line printed above the list about the prices: where
	// they are from and how they are billed.
	sizes(ctx context.Context, srv *cloudServer) (sizes []cloudSize, note string, err error)
	// stop shuts srv down through its operating system, so services stop
	// in order and the disk is written out, and returns once the cloud
	// reports it stopped. A server already stopped is not an error.
	stop(ctx context.Context, srv *cloudServer, logf func(string, ...any)) error
	// start powers srv on and returns it once it runs and has an address,
	// which is the one it had wherever the cloud keeps it. A server
	// already running is not an error.
	start(ctx context.Context, srv *cloudServer) (*cloudServer, error)
	// resize changes srv, which is stopped, to one of the sizes listed,
	// keeping what is on its disk, and returns the server: the same one,
	// still stopped, or the one that replaced it on a cloud that resizes
	// by replacing, which may already run. After an error the box still
	// has a server of its old size, or resize returns errLeftParked.
	resize(ctx context.Context, srv *cloudServer, size string, logf func(string, ...any)) (*cloudServer, error)
	// parkCost says what parking the box changes on the cloud's bill, with
	// the cloud's prices where it gives them: what the server costs, which
	// parking saves, and what the snapshot costs, which stays billed. It is
	// asked before anything changes.
	parkCost(ctx context.Context, srv *cloudServer) (parkBill, error)
	// typical says how long the three commands take on this cloud.
	typical() cloudTimes
	// snapshot saves the disk of srv, which is stopped, as the box's parked
	// snapshot: labelled keelbox=true, with the box name and the parked
	// marker (provider.ParkedLabels), so any computer with the box's config
	// finds it. It returns only once the cloud reports the snapshot
	// complete and usable to create a server from; park deletes the server
	// on the strength of that. After an error no snapshot of this call is
	// left, and the server is as it was.
	snapshot(ctx context.Context, srv *cloudServer, logf func(string, ...any)) (*cloudParked, error)
	// release deletes srv, which park has a confirmed snapshot of, and
	// gives up its public address, and returns once the server is gone. It
	// keeps the snapshot and whatever restore attaches the new server to
	// (the firewall); it says each deletion.
	release(ctx context.Context, srv *cloudServer, logf func(string, ...any)) error
	// parked returns the box's parked snapshot as the cloud holds it: the
	// newest complete one with the box's parked labels, or nil when there
	// is none. It reads only; nothing on this computer is consulted.
	parked(ctx context.Context) (*cloudParked, error)
	// restore creates the box's server from p with the name, size, place,
	// labels and firewall the config gives it, and provider.ResumeUserData
	// as its user data, so the SSH host keys on the disk are kept. The
	// firewall is in force before the server first boots where the cloud
	// takes it in the create call or applies it by label (Hetzner, Google
	// Cloud); where it cannot (DigitalOcean, Lightsail), restore puts it on
	// as soon as the cloud accepts it and returns only once it is in
	// force. It adds no login the disk does not have. It returns the
	// server once it runs and has an address, and fails when the box has a
	// server. A server returned with an error may exist.
	restore(ctx context.Context, p *cloudParked, logf func(string, ...any)) (*cloudServer, error)
	// discard deletes the parked snapshot p, and says so. It refuses
	// anything without the box's parked labels.
	discard(ctx context.Context, p *cloudParked, logf func(string, ...any)) error
}

// cloudSize is one size a server can change to, as keelbox resize lists it.
type cloudSize struct {
	// name is the value of the config's size key: "cpx32".
	name string
	// cpu and memory say what the size is: "2 vCPU (shared)", "4 GB".
	cpu, memory string
	// disk is the size of the box's disk at this size, in GB: the disk the
	// server has now where a resize leaves the disk alone, the size's own
	// where the disk grows with it. 0 when the cloud does not say.
	disk int
	// more is what else the size comes with: "4 TB transfer".
	more string
	// price is what the size costs a month in currency ("EUR"); 0 when the
	// cloud gives no price.
	price    float64
	currency string
	// small marks a size below what agents need (2 vCPU and 4 GB).
	small bool
	// current marks the size the server has now.
	current bool
}

// cloudParked is a parked box as its cloud holds it: the snapshot of its
// disk.
type cloudParked struct {
	// ref names the snapshot in messages: "snapshot 42".
	ref string
	// since is when the box was parked: when the snapshot was taken. A
	// snapshot without it is not taken as confirmed.
	since time.Time
	// detail is what the cloud says about it in a few words: its size, and
	// what it costs a month when the API gives a price.
	detail string
	// raw is the provider's own record of it.
	raw any
}

// errLeftParked is what resize returns on a cloud that resizes by replacing
// the server, when the old server is deleted and none could be created from
// its disk again: the box is parked, and parked holds the disk.
type errLeftParked struct {
	parked *cloudParked
	cause  error
}

func (e *errLeftParked) Error() string { return e.cause.Error() }

// openCloud is the box's cloud provider, with its credential; tests
// replace it. p asks for a missing credential when it can.
var openCloud = func(ctx context.Context, cfg *config.Config, o options, p *prompt.Prompter, out io.Writer) (cloud, error) {
	switch cfg.Target.Provider {
	case config.ProviderHetzner:
		cl, err := hetznerClient(ctx, cfg, o, p, out)
		if err != nil {
			return nil, err
		}
		return &hetznerCloud{cl: cl, cfg: cfg}, nil
	case config.ProviderAWS:
		return openAWS(ctx, cfg)
	case config.ProviderDigitalOcean:
		return openDigitalOcean(ctx, cfg)
	case config.ProviderGCP:
		return openGCP(ctx, cfg)
	}
	return nil, fmt.Errorf("provider %s creates no servers", cfg.Target.Provider)
}

// openCloudKnown is openCloud for a command that needs the box's server.
// Where the cloud's credential would have to be asked for, it first reads
// what this computer noted about the box: one that is parked, or whose
// server keelbox destroy deleted, has no server to reach, and the command
// says so instead of asking for a token. With the credential at hand
// nothing changes: the cloud is asked, and it is the authority.
func openCloudKnown(ctx context.Context, cfg *config.Config, o options, p *prompt.Prompter, ref string, out io.Writer) (cloud, error) {
	if cl, err := openCloud(ctx, cfg, o, noPrompt(), io.Discard); err == nil {
		return cl, nil
	}
	if err := noServerNoted(cfg, ref); err != nil {
		return nil, err
	}
	return openCloud(ctx, cfg, o, p, out)
}

// noServerNoted is the error for a box this computer has noted as having
// no server, parked or destroyed, or nil.
func noServerNoted(cfg *config.Config, ref string) error {
	if perr := parkedLocally(cfg, ref); perr != nil {
		return perr
	}
	if m, ok := loadDestroyedMark(cfg.Box); ok {
		return fmt.Errorf("%s has no server (keelbox destroy deleted it on %s); %s", cfg.Box, day(m.At), provider.NoServerFix(ref))
	}
	return nil
}

// cloudEntry is a cloud as keelbox up offers it: one row of the "Which
// cloud?" question, and the questions that follow it. A new provider is
// one more entry in cloudEntries.
type cloudEntry struct {
	// via is the provider's name: the --provider value and target.provider.
	via string
	// name is the row's label: "AWS (Lightsail)".
	name string
	// needs says in a few words what the cloud needs on this computer, and
	// reports whether that is in place. It runs before the question is
	// drawn, so it reads only what is on this computer: no network call.
	needs func(ctx context.Context, o options) (detail string, ready bool)
	// ask puts the cloud's own questions (credential, region, size) and
	// fills in the starter config.
	ask func(ctx context.Context, w *wizard, a *answers) error
}

// cloudEntries are the clouds in the order keelbox up lists them. Hetzner
// is first: it needs nothing else installed. The ones that go through the
// cloud's own CLI follow.
func cloudEntries() []cloudEntry {
	return []cloudEntry{
		{viaHetzner, "Hetzner", hetznerNeeds, func(ctx context.Context, w *wizard, a *answers) error { return w.askHetzner(ctx, a) }},
		{viaAWS, "AWS (Lightsail)", awsNeeds, func(ctx context.Context, w *wizard, a *answers) error { return w.askAWS(ctx, a) }},
		{viaDO, "DigitalOcean", doNeeds, func(ctx context.Context, w *wizard, a *answers) error { return w.askDigitalOcean(ctx, a) }},
		{viaGCP, "Google Cloud", gcpNeeds, func(ctx context.Context, w *wizard, a *answers) error { return w.askGCP(ctx, a) }},
	}
}

// cloudNamed is the entry of the cloud a provider name stands for.
func cloudNamed(provider string) (cloudEntry, bool) {
	for _, e := range cloudEntries() {
		if e.via == provider {
			return e, true
		}
	}
	return cloudEntry{}, false
}

// isCloud reports whether keelbox creates and deletes the provider's servers.
func isCloud(provider string) bool {
	_, ok := cloudNamed(provider)
	return ok
}

// bootstrapUser is the account a cloud's new server logs in as, or "" when
// it boots with the admin account itself.
func bootstrapUser(provider string) string {
	switch provider {
	case config.ProviderAWS:
		return aws.BootstrapUser
	case config.ProviderDigitalOcean, config.ProviderGCP:
		return ""
	}
	return "root"
}

func runCloud(ctx context.Context, cmd string, cfg *config.Config, o options, stdin io.Reader, stdout, stderr io.Writer) int {
	p := terminalPrompt(ctx, stdout, o.style)
	var cl cloud
	var err error
	switch cmd {
	case "create", "destroy", "resize", "park", "resume":
		// These are about the server itself: the cloud is asked.
		cl, err = openCloud(ctx, cfg, o, p, stdout)
	default:
		cl, err = openCloudKnown(ctx, cfg, o, p, boxRef(cfg, o.configFile), stdout)
	}
	if err != nil {
		fail(stderr, err)
		return 1
	}
	logf := func(f string, a ...any) { fmt.Fprintf(stdout, "keelbox: "+f+"\n", a...) }
	switch cmd {
	case "create":
		return cloudCreate(ctx, cl, cfg, o, logf, stdout, stderr)
	case "destroy":
		return cloudDestroy(ctx, cl, cfg, o, stdin, logf, stdout, stderr)
	case "resize":
		return cloudResize(ctx, cl, cfg, o, p, logf, stdout, stderr)
	case "park":
		return cloudPark(ctx, cl, cfg, o, p, logf, stdout, stderr)
	case "resume":
		return cloudResume(ctx, cl, cfg, o, logf, stdout, stderr)
	}
	public, srv, err := cloudPublic(ctx, cl, cfg, boxRef(cfg, o.configFile))
	if err != nil {
		fail(stderr, err)
		return 1
	}
	o.publicIP = srv.ip
	switch cmd {
	case "apply":
		return applyExisting(ctx, cl, cfg, srv, public, o, logf, stdout, stderr)
	case "doctor":
		a := reach(ctx, cfg, public, stderr)
		code := runSSH(ctx, cmd, cfg, a, o, stdout, stderr)
		if fcode := firewallDoctor(ctx, cl, cfg, srv, a, stdout); code == 0 {
			code = fcode
		}
		return code
	}
	return runSSH(ctx, cmd, cfg, reach(ctx, cfg, public, stderr), o, stdout, stderr)
}

// applyExisting is keelbox apply on a cloud box whose server exists;
// tests replace it.
var applyExisting = cloudApply

func cloudApply(ctx context.Context, cl cloud, cfg *config.Config, srv *cloudServer, public addr, o options, logf func(string, ...any), stdout, stderr io.Writer) int {
	// Before the apply only make sure the firewall exists and is
	// attached; whether it admits SSH follows the box afterwards.
	if changes, _, err := cl.firewall(ctx, srv, nil); err != nil {
		failf(stderr, "cloud firewall: %v", err)
		return 1
	} else {
		logChanges(logf, changes)
	}
	code, a := applyBox(ctx, cfg, public, o, stdout, stderr)
	if fcode := reconcileFirewall(context.WithoutCancel(ctx), cl, cfg, srv, a, logf, stderr); code == 0 {
		code = fcode
	}
	return code
}

func logChanges(logf func(string, ...any), changes []string) {
	for _, c := range changes {
		logf("%s", c)
	}
}

func cloudDestroy(ctx context.Context, cl cloud, cfg *config.Config, o options, stdin io.Reader, logf func(string, ...any), stdout, stderr io.Writer) int {
	// One reader for every question: each would otherwise buffer the
	// other's answer away.
	in := bufio.NewReader(stdin)
	ask := readerPrompt(ctx, in, stdin, stdout, o.style)
	srv, err := cl.find(ctx)
	if err != nil {
		p, perr := cl.parked(ctx)
		switch {
		case perr != nil:
			// Whether a snapshot is left is not known: nothing is swept.
			fail(stderr, err)
			failf(stderr, "could not look for a parked snapshot: %v", perr)
			return 1
		case p != nil:
			return destroyParked(ctx, cl, cfg, o, p, in, logf, stdout, stderr)
		}
		// No server: still remove what an interrupted create left.
		cl.sweep(ctx, logf)
		fail(stderr, err)
		return 1
	}
	if !o.discardWork {
		if code := destroyWorkCheck(ctx, cfg, srv.ip, o, ask, stdout, stderr); code != 0 {
			return code
		}
	}
	if !o.yes {
		typed, err := ask.AskTyped(fmt.Sprintf("Delete %s and everything on it? Type the box name to confirm", srv.about))
		if errors.Is(err, prompt.ErrInterrupted) {
			return exitInterrupted // Main says that nothing changed
		}
		if typed != cfg.Box {
			return declined(stdout, typed, cfg.Box)
		}
	}
	signedOut := destroySignOut(ctx, cfg, srv.ip, o, stdout)
	if err := cl.destroy(ctx, srv, logf); err != nil {
		fail(stderr, err)
		return 1
	}
	// A resume that did not finish leaves the parked snapshot beside the
	// server: it goes with the box.
	if p, err := cl.parked(ctx); err != nil {
		failf(stderr, "the server is deleted, but looking for a parked snapshot of %s failed: %v. Run keelbox destroy %s again to remove one\n", cfg.Box, err, boxRef(cfg, o.configFile))
		return 1
	} else if p != nil {
		if err := cl.discard(ctx, p, logf); err != nil {
			failf(stderr, "the server is deleted, but %s is not: %v. It is billed until it is deleted: keelbox destroy %s\n", p.ref, err, boxRef(cfg, o.configFile))
			return 1
		}
	}
	forgetBox(cfg, srv.ip)
	forgetUpState(cfg.Box)
	forgetParkMark(cfg.Box)
	stillValid(cfg, signedOut, stdout)
	noteDestroyed(cfg, o, stdout, stderr)
	return 0
}

// declined is the end of a question the user answered with no: the one
// sentence every command has for that, and the one exit code. typed is
// what was typed where a name was asked for, to say why it was not taken.
func declined(stdout io.Writer, typed, want string) int {
	if typed != "" && typed != want {
		fmt.Fprintf(stdout, "%s %q is not the box name, %s.\n", nothingChanged, typed, want)
	} else {
		fmt.Fprintln(stdout, nothingChanged)
	}
	return exitDeclined
}

// forgetBox drops what this laptop knows about a box that is gone: pinned
// host keys for its addresses and its tailnet record.
func forgetBox(cfg *config.Config, ip string) {
	kh := knownHosts(cfg)
	if ip != "" {
		provider.ForgetHost(kh, ip)
	}
	if name := cfg.Target.SSH.Host; name != "" {
		provider.ForgetHost(kh, name)
	}
	if t, ok := tailnetAddr(cfg); ok {
		provider.ForgetHost(kh, knownHostsName(t))
	}
	forgetRecord(cfg)
}

// cloudPublic is the address to reach an existing cloud box at outside the
// tailnet: its public IPv4, or target.ssh.host when set (its tailnet name,
// say), which inherits the host keys pinned for the IP.
//
// A box with no server and a parked snapshot is parked: the error says so
// and names keelbox resume, with ref as the box. Every command that
// connects to a cloud box comes through here, so each of them stops there.
func cloudPublic(ctx context.Context, cl cloud, cfg *config.Config, ref string) (addr, *cloudServer, error) {
	srv, err := cl.find(ctx)
	if err != nil {
		if p, perr := cl.parked(ctx); perr == nil && p != nil {
			return addr{}, nil, rememberParked(cfg, ref, p)
		}
		return addr{}, nil, err
	}
	// The cloud has a server: whatever this computer noted about the box
	// being parked or destroyed is out of date (it was resumed or created
	// again from another one).
	forgetParkMark(cfg.Box)
	forgetDestroyedMark(cfg.Box)
	ip := addr{srv.ip, cfg.Target.SSH.Port}
	name := cfg.Target.SSH.Host
	if name == "" {
		return ip, srv, nil
	}
	a := addr{name, cfg.Target.SSH.Port}
	if err := sshTarget(cfg, a, options{}, io.Discard, io.Discard).CopyPins(ip.host, ip.port); err != nil {
		return addr{}, nil, err
	}
	return a, srv, nil
}

// cloudCreate creates the server, waits for SSH and cloud-init, then runs
// the first apply as the image's own account (root on Hetzner, ubuntu on
// AWS). That account's SSH login is turned off by that apply; later runs
// use the admin user. A DigitalOcean or Google Cloud server boots with the
// admin account, and every apply runs as it.
func cloudCreate(ctx context.Context, cl cloud, cfg *config.Config, o options, logf func(string, ...any), stdout, stderr io.Writer) int {
	start := time.Now()
	// From the order to the rotated host keys, Ctrl-C waits: stopped in
	// between, the server would run unrecorded or keep the host key its
	// user data carried. Pressed twice, keelbox stops at once.
	outer, ctx := ctx, context.WithoutCancel(ctx)
	// A parked box has no server, and creating one would leave the box
	// twice: a new empty server and the snapshot that holds its disk.
	if p, err := cl.parked(ctx); err != nil {
		fail(stderr, err)
		return 1
	} else if p != nil {
		fail(stderr, rememberParked(cfg, boxRef(cfg, o.configFile), p))
		return 1
	}
	priv, pub, err := provider.GenerateHostKey(ctx, cfg.Box)
	if err != nil {
		fail(stderr, err)
		return 1
	}
	srv, err := cl.create(ctx, provider.HostKeyPair{Private: priv, Public: pub}, logf)
	var refused *cloudRefusal
	if errors.As(err, &refused) {
		if !o.hooks.refused(refused) {
			fail(stderr, fmt.Errorf("%w. %s", err, createRefusedFix(cfg, o, refused)))
		}
		return 1
	}
	if err != nil {
		fail(stderr, err)
		if srv != nil {
			notef(stderr, "%s may exist; continue with keelbox up %s, or remove it: keelbox destroy %s", srv.ref, cfg.Box, cfg.Box)
		}
		return 1
	}
	// The box has a server again.
	forgetDestroyedMark(cfg.Box)
	ip := addr{srv.ip, cfg.Target.SSH.Port}
	o.hooks.server(ip.host)
	o.publicIP = srv.ip
	bootstrap := o
	if bootstrap.sshUser == "" {
		bootstrap.sshUser = cl.bootstrapUser()
	}
	// A new server: whatever this laptop knew about an earlier box of the
	// same name, or an earlier server at the same address, is stale.
	forgetRecord(cfg)
	s := sshTarget(cfg, ip, bootstrap, stdout, stderr)
	// Pinned before the first packet: addresses are reused between servers,
	// and the injected key is the only one this server should present.
	if err := s.Pin(provider.ParseHostKeys(pub)); err != nil {
		fail(stderr, err)
		return 1
	}
	logf("%s running at %s; waiting for SSH (host key %s)", srv.ref, ip.host, provider.ParseHostKeys(pub)[0].Fingerprint())
	if err := s.WaitReachable(ctx, 5*time.Minute); err != nil {
		fail(stderr, err)
		return 1
	}
	// cloud-init exits 2 for "done with recoverable errors"; only the wait matters.
	_, _ = s.Output(ctx, "cloud-init status --wait >/dev/null 2>&1 || true")
	if err := s.RotateHostKeys(ctx); err != nil {
		fail(stderr, err)
		return 1
	}
	logf("host keys rotated and pinned")
	ctx = outer
	if ctx.Err() != nil {
		next := "keelbox apply " + cfg.Box
		if o.inUp {
			next = "keelbox up " + cfg.Box
		}
		failf(stderr, "stopped before the first apply; %s runs at %s with its host key pinned. Continue: %s\n", srv.ref, ip.host, next)
		return exitInterrupted
	}
	code, a := applyBox(ctx, cfg, ip, bootstrap, stdout, stderr)
	if code == exitDetached {
		// The first apply carries on on the server; the follower said how
		// to pick it up, and that apply brings the cloud firewall in step.
		return code
	}
	if fcode := reconcileFirewall(context.WithoutCancel(ctx), cl, cfg, srv, a, logf, stderr); code == 0 {
		code = fcode
	}
	if code != 0 {
		failf(stderr, "the server exists but apply failed; fix the cause above and run keelbox up %s (or keelbox apply %s), or remove it: keelbox destroy %s\n", cfg.Box, cfg.Box, cfg.Box)
		return code
	}
	logf("%s", createdNote(cfg, o, time.Since(start)))
	if !o.inUp {
		printNextSteps(stdout, cfg, boxRef(cfg, o.configFile))
	}
	return 0
}

// createRefusedFix is how keelbox create goes on after a refusal: for a
// box keelbox up configured, the up commands that change where it is
// created; else the config keys, and another box for another cloud.
func createRefusedFix(cfg *config.Config, o options, r *cloudRefusal) string {
	ref := boxRef(cfg, o.configFile)
	if ref == cfg.Box && notCreated(cfg) {
		return r.upFix(cfg.Box, cfg.Target.Provider, "")
	}
	path := o.configFile
	if path == "" {
		path = boxConfigPath(cfg.Box)
	}
	var names []string
	for _, e := range otherClouds(cfg.Target.Provider)[1:] {
		names = append(names, e.via)
	}
	return r.configFix(path, "keelbox create "+ref, fmt.Sprintf("A box on another cloud: keelbox up %s-2 --provider %s (or %s)",
		cfg.Box, otherClouds(cfg.Target.Provider)[0].via, strings.Join(names, ", ")))
}

// createdNote is the line that ends a create, with what its time covers:
// the server, from the order to the end of the first apply. In keelbox up
// the sign-ins are still to come, and only the total on its last screen
// stands for the whole set-up.
func createdNote(cfg *config.Config, o options, took time.Duration) string {
	note := "server created and set up in " + took.Round(time.Second).String()
	if o.inUp && !o.skipSignIn && len(signIns(cfg)) > 0 {
		note += "; the sign-ins come next"
	}
	return note
}

// reconcileFirewall sets the cloud firewall's SSH rule from the box's own
// state: SSH is admitted unless the box has closed public SSH, which it
// does only after a tailnet proof. When the state cannot be read, the rule
// is left as it is.
func reconcileFirewall(ctx context.Context, cl cloud, cfg *config.Config, srv *cloudServer, a addr, logf func(string, ...any), stderr io.Writer) int {
	var want *bool
	if st, err := boxPublicSSH(ctx, cfg, a); err != nil {
		notef(stderr, "cloud firewall: leaving the SSH rule as it is: %v", err)
	} else {
		open := st.State != "closed"
		want = &open
	}
	changes, summary, err := cl.firewall(ctx, srv, want)
	if err != nil {
		failf(stderr, "cloud firewall: %v", err)
		return 1
	}
	if len(changes) > 0 {
		logf("%s", firewallChanged(want, summary))
	}
	return 0
}

// firewallChanged is the one line an apply prints when it changed the
// cloud's firewall: what that means for reaching the box, then the
// provider's own wording of the rules in brackets.
func firewallChanged(publicSSH *bool, summary string) string {
	what := "updated"
	switch {
	case publicSSH != nil && !*publicSSH:
		what = "no longer admits SSH from the internet, as the box itself does not"
	case publicSSH != nil:
		what = "admits SSH from the internet"
	}
	return fmt.Sprintf("the cloud firewall %s (%s)", what, summary)
}

// firewallDoctor checks the cloud firewall from the laptop: it exists, is
// attached, and admits SSH exactly while the box's public SSH is open.
func firewallDoctor(ctx context.Context, cl cloud, cfg *config.Config, srv *cloudServer, a addr, out io.Writer) int {
	row := func(level, check, detail string) { fmt.Fprintf(out, "  %-4s  %-24s %s\n", level, check, detail) }
	fmt.Fprintf(out, "\n%s (checked from this machine)\n", cl.title())
	return cl.doctor(ctx, srv, func() (bool, error) {
		st, err := boxPublicSSH(ctx, cfg, a)
		return st.State != "closed", err
	}, row)
}
