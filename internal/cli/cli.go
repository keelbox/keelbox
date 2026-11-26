// Package cli implements the keelbox command line.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/manifest"
	"github.com/keelbox/keelbox/internal/modules"
	"github.com/keelbox/keelbox/internal/prompt"
	"github.com/keelbox/keelbox/internal/reports"
	"github.com/keelbox/keelbox/internal/ui"
	"github.com/keelbox/keelbox/internal/version"
	"github.com/keelbox/keelbox/internal/watch"
)

// options are the shared flags.
type options struct {
	local, verbose, yes, force, allowDowngrade, adopt, removeKeys, admin bool
	sshUser, binary, box, session, provider                              string
	configFile                                                           string // the config path given or found
	inUp                                                                 bool   // run by keelbox up, which prints its own next steps
	upLast                                                               bool   // up's closing apply, after the sign-ins: up says what is left
	askSudo                                                              bool   // the SSH account's sudo asks for a password: run under sudo on the terminal
	publicIP                                                             string // a cloud box's public address, from its provider: the box is told it (outsideAddrs)
	list, overwrite                                                      bool
	impact, all, json                                                    bool     // plan --impact, status --all; --json with either
	api                                                                  bool     // login --api: the sign-in for API billing
	skill                                                                string   // keelbox skills --skill: the skills to copy, by name
	remote                                                               []string // keelbox ssh BOX -- CMD: the command, one word per argument
	hasRemote                                                            bool     // a -- was given
	name, remove, target, snapshot, signIn                               string
	add                                                                  string // add: what to add to the box
	sub, channel                                                         string // keelbox notify test --channel NAME
	detail, area, send, show, del                                        string // keelbox report, keelbox reports
	why                                                                  string // keelbox install: the agent's stated reason
	status                                                               bool   // keelbox install --status
	request                                                              string // keelbox approve, keelbox deny: the request ID
	agentPermissions                                                     string // keelbox up: users.agent.permissions
	autoReboot                                                           string // keelbox up: host.auto_reboot
	// keelbox up.
	size, location, agents, host, hostKey, sshKey, tokenFile string
	profile                                                  string // aws: the AWS CLI profile
	doctlContext                                             string // digitalocean: the doctl context
	gcpProject                                               string // gcp: the Google Cloud project id
	hcloudContext                                            string // hetzner: read the token from this hcloud CLI context
	lanPorts, publicSSH, agentLimits                         string
	gitName, gitEmail                                        string // the agent's git identity
	remoteControl, tailscale, noHostKey, gh, skipSignIn      bool
	discardWork                                              bool            // keelbox destroy: delete work that exists only on the box
	set                                                      map[string]bool // flags given on the command line
	hooks                                                    *upHooks        // keelbox up's progress callbacks
	style                                                    ui.Style        // what stdout can show: colour, a live view
}

// flagSet defines every flag into o; commands document theirs in help.go.
func flagSet(o *options, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet("keelbox", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.BoolVar(&o.local, "local", false, "")
	fs.BoolVar(&o.verbose, "verbose", false, "")
	fs.BoolVar(&o.yes, "yes", false, "")
	fs.BoolVar(&o.force, "force", false, "")
	fs.BoolVar(&o.api, "api", false, "")
	fs.BoolVar(&o.allowDowngrade, "allow-downgrade", false, "")
	fs.BoolVar(&o.adopt, "adopt", false, "")
	fs.BoolVar(&o.removeKeys, "remove-keys", false, "")
	fs.BoolVar(&o.impact, "impact", false, "")
	fs.BoolVar(&o.json, "json", false, "")
	fs.StringVar(&o.sshUser, "ssh-user", "", "")
	fs.StringVar(&o.binary, "binary", "", "")
	fs.StringVar(&o.box, "box", "", "")
	fs.StringVar(&o.provider, "provider", "", "")
	fs.StringVar(&o.session, "session", "", "")
	fs.BoolVar(&o.admin, "admin", false, "")
	fs.StringVar(&o.name, "name", "", "")
	fs.BoolVar(&o.list, "list", false, "")
	fs.StringVar(&o.remove, "remove", "", "")
	fs.StringVar(&o.target, "target", "", "")
	fs.BoolVar(&o.overwrite, "overwrite", false, "")
	fs.BoolVar(&o.all, "all", false, "")
	fs.StringVar(&o.size, "size", "", "")
	fs.StringVar(&o.location, "location", "", "")
	fs.StringVar(&o.agents, "agents", "", "")
	fs.StringVar(&o.host, "host", "", "")
	fs.StringVar(&o.hostKey, "host-key", "", "")
	fs.StringVar(&o.sshKey, "ssh-key", "", "")
	fs.StringVar(&o.tokenFile, "token-file", "", "")
	fs.StringVar(&o.profile, "profile", "", "")
	fs.StringVar(&o.doctlContext, "doctl-context", "", "")
	fs.StringVar(&o.gcpProject, "gcp-project", "", "")
	fs.StringVar(&o.hcloudContext, "hcloud-context", "", "")
	fs.BoolVar(&o.remoteControl, "remote-control", false, "")
	fs.BoolVar(&o.tailscale, "tailscale", true, "")
	fs.BoolVar(&o.noHostKey, "no-host-key", false, "")
	fs.StringVar(&o.lanPorts, "lan-ports", "", "")
	fs.StringVar(&o.publicSSH, "public-ssh", "", "")
	fs.StringVar(&o.agentLimits, "limits", "", "")
	fs.BoolVar(&o.gh, "gh", true, "")
	fs.StringVar(&o.gitName, "git-name", "", "")
	fs.StringVar(&o.gitEmail, "git-email", "", "")
	fs.BoolVar(&o.skipSignIn, "skip-sign-in", false, "")
	fs.BoolVar(&o.discardWork, "discard-work", false, "")
	fs.StringVar(&o.channel, "channel", "", "")
	fs.StringVar(&o.detail, "detail", "", "")
	fs.StringVar(&o.area, "area", "", "")
	fs.StringVar(&o.send, "send", "", "")
	fs.StringVar(&o.show, "show", "", "")
	fs.StringVar(&o.del, "delete", "", "")
	fs.StringVar(&o.why, "why", "", "")
	fs.BoolVar(&o.status, "status", false, "")
	fs.StringVar(&o.agentPermissions, "agent-permissions", "", "")
	fs.StringVar(&o.autoReboot, "auto-reboot", "", "")
	fs.StringVar(&o.skill, "skill", "", "")
	return fs
}

func parse(cmd string, args []string, stderr io.Writer) (options, []string, error) {
	var o options
	// The flag package's own words ("flag provided but not defined: -x")
	// are not printed: flagError says the same in keelbox's.
	fs := flagSet(&o, io.Discard)
	fs.Usage = func() {}
	o.set = map[string]bool{}
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return o, nil, flagError(cmd, err)
		}
		fs.Visit(func(f *flag.Flag) { o.set[f.Name] = true })
		if fs.NArg() == 0 {
			if err := foreignFlag(cmd, o.set); err != nil {
				return o, nil, err
			}
			return o, pos, nil
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

// foreignFlag is the error for a flag another command has and cmd does
// not: it would be read and do nothing, which reads as accepted. --yes and
// --verbose are taken by every command, so a script may pass them to one
// that asks nothing or has no more to print.
func foreignFlag(cmd string, set map[string]bool) error {
	c, found := findCmd(cmd)
	if !found {
		return nil
	}
	own := map[string]bool{"yes": true, "verbose": true}
	for _, f := range flagNames(c) {
		own[strings.TrimPrefix(f, "--")] = true
	}
	var names []string
	for name := range set {
		if !own[name] {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)
	return fmt.Errorf("%s has no flag --%s; list its flags: keelbox help %s", cmd, names[0], cmd)
}

// flagError says what is wrong with a flag in keelbox's words, with the
// flag as it is typed (two dashes) and the command that lists the flags
// the command takes.
func flagError(cmd string, err error) error {
	msg := err.Error()
	help := "keelbox help " + cmd
	if name, ok := strings.CutPrefix(msg, "flag provided but not defined: -"); ok {
		name = strings.TrimLeft(name, "-")
		var known []string
		if c, found := findCmd(cmd); found {
			for _, f := range flagNames(c) {
				known = append(known, strings.TrimPrefix(f, "--"))
			}
		}
		if near := nearest(name, known); near != "" {
			return fmt.Errorf("%s has no flag --%s; did you mean --%s? List its flags: %s", cmd, name, near, help)
		}
		return fmt.Errorf("%s has no flag --%s; list its flags: %s", cmd, name, help)
	}
	if name, ok := strings.CutPrefix(msg, "flag needs an argument: -"); ok {
		return fmt.Errorf("--%s needs a value; see what it takes: %s", strings.TrimLeft(name, "-"), help)
	}
	if rest, ok := strings.CutPrefix(msg, "invalid boolean value "); ok {
		value, name, _ := strings.Cut(rest, " for -")
		name, _, _ = strings.Cut(strings.TrimLeft(name, "-"), ":")
		return fmt.Errorf("--%s is on or off and takes no value like %s; write --%s or --%s=false. List the flags: %s", name, value, name, name, help)
	}
	return fmt.Errorf("%s; list its flags: %s", msg, help)
}

// unknownCommand says so, with the nearest command, in a line or two.
func unknownCommand(cmd string, stderr io.Writer) int {
	text := fmt.Sprintf("unknown command %q.", cmd)
	if near := nearest(cmd, commandNames()); near != "" {
		text += " Did you mean keelbox " + near + "?"
	}
	failf(stderr, "%s Every command: keelbox help", text)
	return 2
}

// nearest is the command s abbreviates, or the closest within two edits.
func nearest(s string, names []string) string {
	for _, n := range names {
		if len(s) >= 2 && strings.HasPrefix(n, s) {
			return n
		}
	}
	best, bestD := "", 3
	for _, n := range names {
		if d := editDistance(s, n); d < bestD {
			best, bestD = n, d
		}
	}
	return best
}

func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

// styledCommands are the commands whose output is drawn in the brand's
// colours, and so ask the terminal for its background first.
var styledCommands = map[string]bool{
	"up": true, "ls": true, "boxes": true, "status": true, "doctor": true, "help": true, "-h": true, "--help": true,
	"plan": true, "apply": true, "create": true, "destroy": true, "rollback": true, "forget": true,
	"resize": true, "park": true, "resume": true, "add": true, "update": true,
}

// Main runs the CLI and returns the exit code.
func Main(args []string, stdout, stderr io.Writer) int {
	stoppedAtQuestion.Store(false)
	stopSaid.Store(false)
	check := startUpdateCheck(args, stderr)
	code := runCommand(args, stdout, stderr)
	cmd := ""
	if len(args) > 0 {
		cmd = args[0]
	}
	code = stoppedExit(cmd, code, stderr)
	refreshSSHConfig(cmd)
	check.finish(stderr)
	return code
}

// runCommand runs one command line.
func runCommand(args []string, stdout, stderr io.Writer) int {
	var style ui.Style
	if f, ok := stdout.(*os.File); ok {
		style = ui.For(f)
		if len(args) == 0 || styledCommands[args[0]] {
			// Brand colours for a light or a dark theme. Not before ssh,
			// attach or login, which hand the terminal to another program.
			style = style.WithBackground(os.Stdin, f)
		}
	}
	mainStyle.Store(&style)
	if len(args) == 0 {
		startScreen(stdout, style)
		if note := agentNote(); note != "" {
			fmt.Fprint(stdout, "\n"+note)
		}
		return 0
	}
	cmd := args[0]
	if cmd == "-v" || cmd == "--version" || cmd == "--v" || cmd == "-version" {
		cmd = "version"
	}
	if cmd == "-h" || cmd == "--help" || cmd == "help" {
		if len(args) > 1 {
			c, ok := findCmd(args[1])
			if !ok {
				return unknownCommand(args[1], stderr)
			}
			fmt.Fprint(stdout, commandHelpIn(style, c))
			return 0
		}
		fmt.Fprint(stdout, overviewIn(style))
		if note := agentNote(); note != "" {
			fmt.Fprint(stdout, "\n"+note)
		}
		return 0
	}
	if cmd == "__boxes" {
		return cmdBoxes(stdout)
	}
	if cmd == modules.HasCommand {
		return hasCommand(args[1:], stderr)
	}
	if cmd == modules.EventCommand {
		return cmdEvent(args[1:], os.Stdin, stderr)
	}
	if cmd == watch.BootCommand {
		return bootMark(args[1:], stdout, stderr)
	}
	if cmd == modules.WelcomeCommand {
		return cmdWelcome(args[1:], stdout, stderr)
	}
	c, known := findCmd(cmd)
	if !known && !internalCommands[cmd] {
		return unknownCommand(cmd, stderr)
	}
	// keelbox ssh BOX -- CMD: what follows -- is the box's, flags included.
	var remote []string
	hasRemote := false
	if cmd == "ssh" {
		for i, a := range args[1:] {
			if a == "--" {
				remote, hasRemote = args[i+2:], true
				args = args[:i+1]
				break
			}
		}
	}
	for _, a := range args[1:] {
		if a == "-h" || a == "--help" || a == "-help" {
			fmt.Fprint(stdout, commandHelpIn(style, c))
			return 0
		}
	}
	o, pos, err := parse(cmd, args[1:], stderr)
	if err != nil {
		fail(stderr, err)
		return 2
	}
	o.remote, o.hasRemote = remote, hasRemote
	o.style = style
	if code, done := asAgentOnBox(cmd, o, stdout, stderr); done {
		return code
	}
	asked := stdout // where "Which box?" is asked: the terminal itself
	if style.Live && (cmd == "doctor" || cmd == "status" && !o.all && !o.json) {
		// The box writes plain text; marks and colours are added here.
		r := ui.NewReport(stdout, style, cmd == "status")
		defer r.Close()
		stdout = r
	}
	ctx, stop := interruptible(stderr, cmd == "apply" || cmd == "update" || cmd == "up" || cmd == "create" || cmd == "rollback" || cmd == "resize" || cmd == "park" || cmd == "resume")
	defer stop()
	commandCtx.Store(&ctx)

	switch cmd {
	case "version":
		m, _ := manifest.ForChannel("stable")
		fmt.Fprintf(stdout, "keelbox %s (release %s, %s, %s/%s)\n", version.String(), m.Release, runtime.Version(), runtime.GOOS, runtime.GOARCH)
		return 0
	case "init":
		return cmdInit(o, pos, stdout, stderr)
	case "self-update":
		return cmdSelfUpdate(ctx, o, stdout, stderr)
	case "expose":
		return cmdExpose(ctx, o, pos, stdout, stderr)
	case "ls", "boxes":
		if o.json {
			return cmdListJSON(stdout)
		}
		return cmdList(style, stdout, stderr)
	case "agent-skill":
		sub := ""
		if len(pos) > 0 {
			sub = pos[0]
		}
		if len(pos) > 1 {
			failf(stderr, "agent-skill takes install or remove: keelbox agent-skill install")
			return 2
		}
		return cmdAgentSkill(sub, stdout, stderr)
	case "ssh-config":
		return cmdSSHConfig(o, terminalPrompt(ctx, stdout, o.style), stdout, stderr)
	case "up":
		return cmdUp(ctx, o, pos, terminalPrompt(ctx, stdout, o.style), realRunner{}, stdout, stderr)
	case "forget-token":
		return cmdForgetToken(ctx, pos, stdout, stderr)
	case "forget":
		return cmdForget(o, pos, terminalPrompt(ctx, stdout, o.style), stdout, stderr)
	case "completion":
		return cmdCompletion(pos, stdout, stderr)
	case "report":
		return cmdReport(o, pos, os.Stdin, stdout, stderr)
	case "install":
		return cmdInstall(ctx, o, pos, stdout, stderr)
	case "approve", "deny":
		if pos, o.request = requestArg(pos); len(pos) > 1 || cmd == "deny" && o.request == "" || o.request != "" && o.all {
			fmt.Fprintf(stderr, "keelbox: %s takes a box and a request ID, 8 hex digits as keelbox requests BOX lists them: %s\n", cmd,
				map[string]string{"approve": "keelbox approve acme 3f2a9c1e, or every request that waits: keelbox approve acme --all", "deny": "keelbox deny acme 3f2a9c1e"}[cmd])
			return 2
		}
	case "restore":
		pos, o.snapshot = snapshotArg(pos)
	case "login":
		pos, o.signIn = signInArg(pos)
	case "add":
		var err error
		if pos, o.add, err = addArg(pos); err != nil {
			fail(stderr, err)
			return 2
		}
	case "notify":
		if pos, o.sub = notifyArg(pos); o.sub == "" {
			failf(stderr, "notify takes test: keelbox notify test [box] [--channel NAME]")
			return 2
		}
	case "status":
		if o.all {
			return cmdStatusAll(ctx, o, pos, stdout, stderr)
		}
	case "plan", "apply", "doctor", "create", "destroy", "resize", "park", "resume", "rollback", "attach", "sessions", "skills", "ssh", "backup", "previews-sync", "reports", "requests":
	}

	path, err := resolveConfig(cmd, pos, boxPrompt(ctx, asked, style))
	if errors.Is(err, prompt.ErrInterrupted) {
		return exitInterrupted // Ctrl-C at "Which box?": nothing has started, and Main says so
	}
	if err != nil {
		fail(stderr, err)
		return 2
	}
	o.configFile = path
	cfg, err := config.Load(path)
	if errors.Is(err, os.ErrPermission) && os.Geteuid() != 0 {
		// The box's own config is root's: run the command as root.
		return asRoot(cmd, path, o, stdout, stderr)
	}
	if err != nil {
		fail(stderr, err)
		return 2
	}
	if boxRef(cfg, path) == cfg.Box && isRegistered(cfg.Box) {
		noteBoxUsed(cfg.Box) // offered first the next time a command names no box
	}
	provider := cfg.Target.Provider
	if o.local {
		provider = config.ProviderLocal
		cfg.Target.Provider = config.ProviderLocal
	}
	switch cmd {
	case "login":
		return cmdLogin(ctx, cfg, o, o.signIn, stdout, stderr)
	case "add":
		return cmdAdd(ctx, cfg, o, path, func(next *config.Config) int {
			next.Target.Provider = cfg.Target.Provider // --local, as this run has it
			code, _ := runProvider(ctx, "apply", next, o, path, provider, stdout, stderr)
			return code
		}, stdout, stderr)
	case "update":
		return cmdUpdate(ctx, cfg, o, path, argsAfterCommand(os.Args[1:], "update"), func() int {
			code, _ := runProvider(ctx, "apply", cfg, o, path, provider, stdout, stderr)
			return code
		}, stdout, stderr)
	case "attach":
		return cmdAttach(ctx, cfg, o, stdout, stderr)
	case "sessions":
		return cmdSessions(ctx, cfg, o, stdout, stderr)
	case "skills":
		return cmdSkills(ctx, cfg, o, terminalPrompt(ctx, stdout, o.style), stdout, stderr)
	case "ssh":
		return cmdShell(ctx, cfg, o, stdout, stderr)
	case "reboot":
		return cmdReboot(ctx, cfg, o, path, provider, terminalPrompt(ctx, stdout, o.style), stdout, stderr)
	}
	switch {
	case (cmd == "resize" || cmd == "park" || cmd == "resume") && !isCloud(provider):
		return needsCloudBox(cmd, cfg, stderr)
	case cmd == "create" && !isCloud(provider):
		failf(stderr, "create makes a new server and needs provider hetzner, aws, digitalocean or gcp; %s is provider %s, so converge it: keelbox apply %s; or, for a new server on hetzner, aws, digitalocean or gcp: keelbox up NAME --provider hetzner\n", cfg.Box, provider, cfg.Box)
		return 2
	case cmd == "destroy" && !isCloud(provider):
		failf(stderr, "destroy deletes a Hetzner, AWS, DigitalOcean or Google Cloud server keelbox created; %s is a machine you have (provider %s), so keelbox leaves it as it is. "+
			"Stop managing it from here: keelbox forget %s. Undo keelbox's changes on it: https://keelbox.dev/docs/guides/removing-a-box\n", cfg.Box, provider, cfg.Box)
		return 2
	}
	if cmd == "reports" && o.send != "" {
		fetchReport = func(ctx context.Context, n string) (reports.Report, error) {
			return reportFromBox(ctx, cfg, o, path, provider, n)
		}
		return sendReport(ctx, cfg, o.send, terminalPrompt(ctx, stdout, o.style), stdout, stderr)
	}
	// What the box says of the agent's package requests is kept for
	// keelbox ls, which reaches no box.
	var said *strings.Builder
	if notesRequests(cmd, cfg, o, path, provider) {
		said = &strings.Builder{}
		stdout = io.MultiWriter(stdout, said)
	}
	code, done := runProvider(ctx, cmd, cfg, o, path, provider, stdout, stderr)
	if said != nil {
		noteRequests(cfg.Box, cmd, code, said.String())
	}
	if done {
		return code
	}
	if code != 0 && isCloud(provider) && parkedLocally(cfg, "") != nil {
		// The box is parked and the command said so: that line is all
		// there is to say about a box without a server.
		return code
	}
	// The laptop adds the sign-in rows to a doctor it ran over SSH, with
	// commands that name the box; the box's own run for it leaves them out.
	if cmd == "doctor" && !forLaptop(o) {
		doctorSignIns(ctx, cfg, o, code != 0, stdout, stderr)
	}
	if (cmd == "status" || cmd == "doctor") && !o.json {
		upNotFinished(cfg, style, asked)
	}
	if cmd == "status" && !o.json {
		boxSecurityNote(cfg, style, asked)
	}
	return code
}

// runProvider runs cmd where the box is: here as root, or over SSH. done
// is true when it handed the command to sudo here, which says all.
func runProvider(ctx context.Context, cmd string, cfg *config.Config, o options, path, provider string, stdout, stderr io.Writer) (code int, done bool) {
	switch provider {
	case config.ProviderLocal:
		if os.Geteuid() != 0 {
			return asRoot(cmd, path, o, stdout, stderr), true
		}
		code = runLocal(ctx, cmd, cfg, o, stdout, stderr)
	case config.ProviderSSH:
		public := addr{cfg.Target.SSH.Host, cfg.Target.SSH.Port}
		if cmd == "apply" {
			if code, _ = applyBox(ctx, cfg, public, o, stdout, stderr); code == 0 && o.sshUser != "" && !o.inUp {
				printNextSteps(stdout, cfg, boxRef(cfg, o.configFile))
			}
		} else {
			code = runSSH(ctx, cmd, cfg, reach(ctx, cfg, public, stderr), o, stdout, stderr)
		}
	default:
		if isCloud(provider) {
			code = runCloud(ctx, cmd, cfg, o, os.Stdin, stdout, stderr)
		}
	}
	return code, false
}

// reportFromBox reads report n from the box's inbox, as JSON.
func reportFromBox(ctx context.Context, cfg *config.Config, o options, path, provider, n string) (reports.Report, error) {
	q := o
	q.send, q.show, q.json = "", n, true
	q.set = map[string]bool{"show": true, "json": true}
	for k := range o.set {
		if k != "send" {
			q.set[k] = true
		}
	}
	var out, errOut bytes.Buffer
	if code, _ := runProvider(ctx, "reports", cfg, q, path, provider, &out, &errOut); code != 0 {
		msg := strings.TrimSpace(errOut.String())
		if msg == "" {
			msg = fmt.Sprintf("keelbox reports --show %s exited %d", n, code)
		}
		return reports.Report{}, errors.New(strings.TrimPrefix(msg, "keelbox: "))
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	var r reports.Report
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &r); err != nil {
		return r, fmt.Errorf("the box's report %s does not read: %w", n, err)
	}
	return r, nil
}

// signInArg takes login's sign-in name out of the positional arguments:
// keelbox login acme codex, or keelbox login codex with one box.
func signInArg(pos []string) ([]string, string) {
	if len(pos) == 0 {
		return pos, ""
	}
	last := pos[len(pos)-1]
	n, ok := signInName(last)
	if !ok || (len(pos) == 1 && (isRegistered(last) || fileExists(last))) {
		return pos, ""
	}
	return pos[:len(pos)-1], n
}

var snapshotRe = regexp.MustCompile(`^([0-9a-f]{8,64}|latest)$`)

// snapshotArg takes restore's snapshot ID out of the positional arguments:
// a word that looks like one and is not a file.
func snapshotArg(pos []string) ([]string, string) {
	for i, p := range pos {
		if _, err := os.Stat(p); err != nil && snapshotRe.MatchString(p) && !isRegistered(p) {
			return append(append([]string(nil), pos[:i]...), pos[i+1:]...), p
		}
	}
	return pos, ""
}

func cmdInit(o options, pos []string, stdout, stderr io.Writer) int {
	path := "keelbox.yaml"
	if len(pos) == 1 {
		path = pos[0]
	} else if len(pos) > 1 {
		failf(stderr, "init takes at most one path: keelbox init acme.yaml --box acme")
		return 2
	}
	provider := o.provider
	switch provider {
	case "":
		provider = config.ProviderLocal
	case config.ProviderLocal, config.ProviderSSH, config.ProviderHetzner, config.ProviderAWS, config.ProviderDigitalOcean, config.ProviderGCP:
	default:
		failf(stderr, "init --provider %q: use local, ssh, hetzner, aws, digitalocean or gcp, for example: keelbox init --provider hetzner\n", provider)
		return 2
	}
	if _, err := os.Stat(path); err == nil && !o.force {
		failf(stderr, "%s exists; overwrite it: keelbox init %s --force\n", path, path)
		return 1
	}
	out, err := config.StarterFor(o.box, provider).Render()
	if err != nil {
		fail(stderr, err)
		return 2
	}
	if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
		fail(stderr, err)
		return 1
	}
	next := map[string]string{
		config.ProviderLocal:        "Edit it, then: keelbox plan " + path + " (it runs itself with sudo)",
		config.ProviderSSH:          "Set target.ssh.host, then: keelbox plan " + path + " --ssh-user root",
		config.ProviderHetzner:      "Set target.hetzner.ssh_keys to a key in your Hetzner project, then: keelbox create " + path,
		config.ProviderAWS:          "Set target.aws.region and bundle, sign in to the AWS CLI (aws login), then: keelbox create " + path,
		config.ProviderDigitalOcean: "Set target.digitalocean.region and size, sign in to doctl (doctl auth login), then: keelbox create " + path,
		config.ProviderGCP:          "Set target.gcp.zone and machine_type, sign in to gcloud (gcloud auth login), then: keelbox create " + path,
	}[provider]
	fmt.Fprintf(stdout, "Wrote %s. %s\n", path, next)
	return 0
}
