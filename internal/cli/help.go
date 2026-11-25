package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/keelbox/keelbox/internal/reports"
	"github.com/keelbox/keelbox/internal/ui"
	"github.com/keelbox/keelbox/internal/version"
)

// Help comes in three sizes: keelbox alone prints a start screen, keelbox
// help an overview of every command, and keelbox COMMAND --help (or keelbox
// help COMMAND) one command's examples and flags. docs/reference/cli.md
// carries the overview and every command's help; TestCLIReferenceMatchesHelp
// keeps them equal.

const docsURL = "https://keelbox.dev/docs"

type flagDoc struct {
	flag, help string // flag with its argument, as typed: "--ssh-user NAME"
}

type example struct {
	cmd, what string
}

type cmdDoc struct {
	name, args, summary string
	group               string
	about               []string // a few lines after the summary
	examples            []example
	flags               []flagDoc
}

// Flags several commands share.
var (
	fLocal   = flagDoc{"--local", "run on this machine (with sudo), whatever target.provider says"}
	fSSHUser = flagDoc{"--ssh-user NAME", "connect as NAME instead of target.ssh.user"}
	fBinary  = flagDoc{"--binary PATH", "keelbox binary to upload (default: this one, keelbox-linux-<arch> beside it, or the same release, downloaded and verified)"}
	fVerbose = flagDoc{"--verbose", "print every step and every command run on the box, instead of the progress view"}
	remote   = []flagDoc{fLocal, fSSHUser, fBinary}
)

var commands = []cmdDoc{
	{
		name: "up", args: "[box]", group: "Set up",
		summary: "set up a box step by step; run it again to continue",
		about: []string{
			"Asks where the box runs, sets it up, offers each vendor's sign-in on your terminal",
			"and prints how to reach it. Answers are kept until the box's config is written, so",
			"a check that fails asks nothing twice. Without a terminal, pass --provider and --yes; a",
			"server keelbox has not connected to before also needs --host-key, and the agent's commits",
			"need --git-name and --git-email.",
		},
		examples: []example{
			{"keelbox up", "ask, then set it up; on this machine, asks for sudo"},
			{"keelbox up devbox", "continue devbox where it stopped"},
			{"keelbox up nas --provider ssh --host 192.168.1.20 --ssh-user ubuntu --host-key " + hostKeyPlaceholder + " --yes", ""},
		},
		flags: []flagDoc{
			{"--provider WHERE", "ssh (an existing server), local (this machine), hetzner, aws, digitalocean or gcp (a new server) or cloud-init (one you create from the printed cloud-config)"},
			{"--name NAME", "the box name (or give it as the argument)"},
			{"--yes", "go ahead without asking"},
			{"--host ADDRESS", "ssh, cloud-init: the server's address"},
			{"--ssh-user NAME", "ssh: the account your key logs in as now (default root)"},
			{"--host-key FPR", "ssh: the server's SHA256 host key fingerprint, pinned at first connection"},
			{"--no-host-key", "cloud-init: leave the single-use host key out of the cloud-config"},
			{"--size TYPE", "hetzner: the server type; aws: the Lightsail bundle; digitalocean: the Droplet size (default: the cheapest with 2 vCPU and 4 GB); gcp: the machine type (default e2-medium)"},
			{"--location NAME", "hetzner: the location (default fsn1); aws: the region (default: the AWS CLI's own); digitalocean: the region; gcp: the zone (default: gcloud's own)"},
			{"--profile NAME", "aws: the AWS CLI profile whose sign-in keelbox uses (default: the CLI's own default)"},
			{"--doctl-context NAME", "digitalocean: the doctl context whose sign-in keelbox uses (default: doctl's current one)"},
			{"--gcp-project ID", "gcp: the Google Cloud project the server is created in (default: gcloud's own)"},
			{"--token-file PATH", "hetzner: a mode 600 file with the API token, written to the config instead of storing the token"},
			{"--hcloud-context NAME", "hetzner: read the API token from this context of the hcloud CLI each time; keelbox keeps no copy"},
			{"--ssh-key PATH", "your SSH key (default ~/.ssh/id_ed25519, id_ecdsa or id_rsa)"},
			{"--agents LIST", "claude-code, codex, both comma-separated (default) or none"},
			{"--agent-permissions MODE", "what the agent may change: auto (default; it installs Ubuntu packages with keelbox install), manual (it asks first) or full (sudo)"},
			{"--auto-reboot WHEN", "when the box restarts for an update: nightly (default; 01:00 to 06:00 in the box's timezone once idle), weekly (the same on Sunday night) or never (you run keelbox reboot)"},
			{"--remote-control", "run Claude Code Remote Control for the Claude app"},
			{"--gh=false", "leave out the GitHub CLI and its sign-in"},
			{"--git-name NAME", "the name on the agent's git commits; with --git-email (asked on a terminal, offering this computer's)"},
			{"--git-email ADDRESS", "the email on the agent's git commits"},
			{"--tailscale=false", "no Tailscale: the box is reached over public SSH only"},
			{"--public-ssh MODE", "when SSH closes: auto (outside the tailnet, the LAN included), lan (the LAN keeps it; the default on a LAN), open or closed"},
			{"--lan-ports LIST", "ssh, local: services found listening to keep open to the LAN: all, none, or known services and ports such as unifi,8081"},
			{"--limits PCT", "ssh, local: cap the agent's CPU and memory at PCT, such as 75%, or none (default 75% where other services run)"},
			{"--skip-sign-in", "finish without the sign-ins; keelbox login BOX does them later"},
			{"--adopt", "ssh, local: accept what keelbox lists as cut off or replaced on a machine in use"},
			fVerbose,
		},
	},
	{
		name: "ls", group: "Set up",
		summary:  "list your boxes and how far each is set up (also: boxes)",
		examples: []example{{"keelbox ls", ""}},
		flags:    []flagDoc{{"--json", "the rows as JSON, for scripts and agents"}},
	},
	{
		name: "agent-skill", args: "install|remove", group: "Set up",
		summary: "teach Claude Code and Codex on this computer to use keelbox",
		about: []string{
			"Puts keelbox's skill in ~/.claude/skills/keelbox and ~/.codex/skills/keelbox, for the CLIs",
			"set up here, with rules that ask you before keelbox commands that change a box and refuse",
			"approve and deny: permissions in ~/.claude/settings.json, and ~/.codex/rules/keelbox.rules.",
		},
		examples: []example{{"keelbox agent-skill install", ""}, {"keelbox agent-skill remove", ""}},
	},
	{
		name: "init", args: "[path]", group: "Set up",
		summary: "write a commented config to edit by hand",
		examples: []example{
			{"keelbox init", "a config for this machine"},
			{"keelbox init acme.yaml --box acme --provider ssh", ""},
		},
		flags: []flagDoc{
			{"--box NAME", "the box name"},
			{"--provider NAME", "the target block to write: local (default), ssh, hetzner, aws, digitalocean or gcp"},
			{"--force", "overwrite an existing file"},
		},
	},
	{
		name: "create", args: "[box]", group: "Set up",
		summary:  "create the Hetzner, AWS, DigitalOcean or Google Cloud server, then apply",
		examples: []example{{"keelbox create acme", ""}},
		flags:    []flagDoc{fBinary, fVerbose, {"--adopt", "replace conflicting software on the box"}},
	},
	{
		name: "resize", args: "[box]", group: "Set up",
		summary: "change the size of the box's cloud server, up or down",
		about: []string{
			"Lists the sizes the server can change to, with estimated prices where the cloud gives them,",
			"and asks. The box restarts, so running sessions stop; everything on its disk stays, then",
			"keelbox applies as usual and rewrites the size in the box's config. On AWS (Lightsail) a",
			"box can only grow: the instance is replaced by a larger one and cannot shrink again.",
		},
		examples: []example{
			{"keelbox resize acme", "list the sizes with prices, then ask"},
			{"keelbox resize acme --size cpx32 --yes", "without a terminal"},
		},
		flags: []flagDoc{
			{"--size SIZE", "the size to change to, as the cloud names it: the server type, bundle, Droplet size or machine type"},
			{"--yes", "restart the box without asking"},
			fBinary, fVerbose,
		},
	},
	{
		name: "park", args: "[box]", group: "Set up",
		summary: "stop paying for the server while the box is not needed; the disk is kept",
		about: []string{
			"Shuts the box down, saves its disk as a snapshot and, only once the cloud reports the",
			"snapshot complete, deletes the server. Snapshot storage stays billed; the public",
			"address is given up. keelbox resume brings the box back, with its files and sign-ins",
			"as they were. Before it asks, park estimates what the server costs and what the snapshot",
			"will, where the cloud gives prices, and says how long parking and resuming typically take.",
		},
		examples: []example{{"keelbox park acme", "estimates what it saves and costs, says how long it takes, then asks"}},
		flags:    []flagDoc{{"--yes", "park without asking"}, {"--verbose", "print a line for every step, as in a log, instead of the list of steps"}},
	},
	{
		name: "resume", args: "[box]", group: "Set up",
		summary: "bring a parked box back on a new server, from its snapshot",
		about: []string{
			"Creates the server again from the parked snapshot, with the same SSH host keys and a",
			"new public address, applies as usual, and deletes the snapshot once that apply passed.",
		},
		examples: []example{{"keelbox resume acme", ""}},
		flags:    []flagDoc{fBinary, fVerbose},
	},
	{
		name: "destroy", args: "[box]", group: "Set up",
		summary: "delete the Hetzner, AWS, DigitalOcean or Google Cloud server keelbox created",
		about: []string{
			"First reads the agent's projects on the box. Where one is not a git repository, has",
			"uncommitted changes, commits not pushed or no remote, or the box does not answer,",
			"destroy lists what it found and asks, No by default; without a terminal it refuses.",
			"On a parked box it deletes the snapshot, which holds everything that was on the box.",
		},
		examples: []example{{"keelbox destroy acme", "checks the projects, then asks for the box name"}},
		flags: []flagDoc{
			{"--yes", "delete without typing the box name; work that is only on the box still stops it"},
			{"--discard-work", "skip the check: delete the server with whatever work is only on it"},
		},
	},
	{
		name: "forget", args: "<box>", group: "Set up",
		summary: "stop managing a box from here; the machine is not touched",
		about: []string{
			"Removes the box's config from ~/.config/keelbox/boxes/ with its pinned host keys.",
			"A cloud server is not deleted, and forget removes what keelbox destroy needs to",
			"find it, so destroy comes before forget where the server is to go. forget says so,",
			"and its question keeps the box unless you choose otherwise.",
		},
		examples: []example{{"keelbox forget nas", "asks first"}},
		flags:    []flagDoc{{"--yes", "forget without asking"}},
	},
	{
		name: "attach", args: "[box]", group: "Use",
		summary:  "open the agent's tmux session",
		examples: []example{{"keelbox attach", ""}, {"keelbox attach acme --session review", ""}},
		flags:    []flagDoc{{"--session NAME", "the tmux session to open or create (default main)"}},
	},
	{
		name: "ssh", args: "[box] [-- command]", group: "Use",
		summary: "open a shell on the box as the agent, or run one command",
		about: []string{
			"After --, the command runs without a terminal, each argument as one word, and keelbox",
			"exits with its exit code; 255 when ssh did not log in. It asks nothing, so the host key",
			"must be pinned already. Shell syntax goes in one argument: -- sh -c 'cd app && make'.",
		},
		examples: []example{{"keelbox ssh acme", ""}, {"keelbox ssh acme --admin", "as the admin, with sudo"},
			{"keelbox ssh acme -- git -C projects/app status", "one command"}},
		flags: []flagDoc{{"--admin", "log in as the admin instead of the agent"}},
	},
	{
		name: "ssh-config", group: "Use",
		summary: "let ssh, scp and editors reach each box as BOX.keelbox",
		about: []string{
			"Writes ~/.config/keelbox/ssh_config: a Host for each box, as its agent, at its tailnet",
			"address, with keelbox's pinned host keys. Commands that move a box write it again.",
			"Asks before adding its Include as the first line of ~/.ssh/config.",
		},
		examples: []example{{"keelbox ssh-config", ""}, {"ssh acme.keelbox", "then"}},
		flags:    []flagDoc{{"--list", "print the file, and change nothing in ~/.ssh/config"}},
	},
	{
		name: "sessions", args: "[box]", group: "Use",
		summary:  "list the agent's tmux sessions",
		examples: []example{{"keelbox sessions acme", ""}},
		flags:    []flagDoc{{"--json", "the sessions as JSON"}},
	},
	{
		name: "skills", args: "[box]", group: "Use",
		summary: "copy skills from this computer to the agent on the box",
		about: []string{
			"Lists the skills in ~/.claude/skills, ~/.agents/skills and ~/.codex/skills to tick, none ticked,",
			"and copies those chosen to the same folders in the agent's home on the box.",
			"Files that may hold a secret (.env, keys, credentials) and links are left out.",
			"The agent can read everything copied and run a skill's scripts.",
		},
		examples: []example{{"keelbox skills acme", ""}, {"keelbox skills acme --skill review,deploy", "without a terminal"},
			{"keelbox skills acme --list", "the skills on this computer"}},
		flags: []flagDoc{{"--skill NAME[,NAME]", "copy these, without asking which"},
			{"--list", "list the skills on this computer, and copy nothing"},
			{"--overwrite", "replace a different skill of the same name on the box, without asking"}},
	},
	{
		name: "login", args: "[box] [sign-in]", group: "Use",
		summary: "sign in to Tailscale, the agent CLIs and GitHub",
		about: []string{
			"Each sign-in is the vendor's own flow on your terminal; keelbox never sees the credentials.",
			"For each: sign in now, skip it, or check again; Ctrl-C stops.",
			"Sign-ins: tailscale, claude-code, codex, gh.",
			"Claude Code and Codex can also sign in for API billing, with no plan.",
		},
		examples: []example{{"keelbox login acme", ""}, {"keelbox login acme codex", "only Codex"},
			{"keelbox login acme codex --api", "Codex with an OpenAI API key"}},
		flags: []flagDoc{{"--force", "sign in again, even where already signed in"},
			{"--api", "with claude-code or codex: sign in for API billing (an Anthropic Console account, an OpenAI API key)"}},
	},
	{
		name: "expose", args: "<port>", group: "Use",
		summary: "on the box: publish a dev server on the tailnet",
		about: []string{
			"Run by the agent on the box: publishes 127.0.0.1:<port> at an HTTPS URL on the",
			"tailnet only, with the box's Tailscale certificate.",
		},
		examples: []example{
			{"keelbox expose 3000", ""},
			{"keelbox expose 5173 --name web", ""},
			{"keelbox expose --list", ""},
		},
		flags: []flagDoc{
			{"--name NAME", "the preview's name (default: the port number)"},
			{"--list", "list previews"},
			{"--remove NAME", "stop publishing a preview"},
		},
	},
	{
		name: "report", args: "<title>", group: "Use",
		summary: "on the box: save a keelbox problem as a draft for the owner",
		about: []string{
			"Run by the agent on the box, only after the user said yes in the conversation. The draft",
			"stays in the box's inbox; nothing is sent. The owner reads it with keelbox reports.",
		},
		examples: []example{
			{`keelbox report "expose: says nothing listens" --area expose --detail "exact error"`, ""},
			{`keelbox report "codex sandbox fails" --area sandbox --detail - < error.txt`, ""},
		},
		flags: []flagDoc{
			{"--detail TEXT", "the exact error and what was run, at most 2000 characters; - reads it from stdin"},
			{"--area AREA", "the part of keelbox: " + strings.Join(reports.Areas, ", ") + " (default other)"},
		},
	},
	{
		name: "install", args: "<package>...", group: "Use",
		summary: "on the box: get Ubuntu packages installed, without sudo",
		about: []string{
			"Run by the agent on the box. It asks root's install service, which checks the request:",
			"package names only, from the apt sources the box already has, nothing on keelbox's",
			"deny-list, nothing removed. With users.agent.permissions: auto the packages are installed",
			"and the result printed (exit 0 when installed). With manual the request is recorded for",
			"the owner's keelbox approve and the command exits 3.",
		},
		examples: []example{
			{`keelbox install libpq-dev --why "psycopg builds against it"`, ""},
			{"keelbox install jq sqlite3", "several names are one request"},
			{"keelbox install --status", "what became of the last requests"},
		},
		flags: []flagDoc{
			{"--why TEXT", "what the work needs it for, in a line; the owner sees it as the agent's own words"},
			{"--status", "list the last requests and what became of each"},
		},
	},
	{
		name: "plan", args: "[box]", group: "Maintain",
		summary: "show what apply would change; changes nothing",
		about: []string{
			"--impact lists, for a machine already in use, what the owner may care about: services",
			"cut off from the network, software replaced, restarts, accounts, and what is kept,",
			"with the config that keeps each. The first apply stops for the items marked *.",
		},
		examples: []example{
			{"keelbox plan acme", ""},
			{"keelbox plan acme --impact", "on a machine already in use"},
		},
		flags: append(append([]flagDoc{}, remote...), fVerbose,
			flagDoc{"--impact", "what apply would change that the machine's owner may care about"},
			flagDoc{"--json", "with --impact: the report as JSON"}),
	},
	{
		name: "apply", args: "[box]", group: "Maintain",
		summary: "converge the box to its config; repeat any time",
		about: []string{
			"On a terminal, one line per module with the current step and elapsed time. Over",
			"SSH the apply runs on the box and finishes if this window closes; Ctrl-C asks",
			"whether to leave it running or stop it after the current step.",
		},
		examples: []example{
			{"keelbox apply acme", ""},
			{"keelbox apply acme --ssh-user root", "first apply on a new machine"},
			{"keelbox apply --local", "on the box itself (runs with sudo)"},
		},
		flags: append(append([]flagDoc{}, remote...), fVerbose,
			flagDoc{"--adopt", "accept what plan --impact marks on a machine in use, such as replacing Ubuntu's docker.io"},
			flagDoc{"--allow-downgrade", "move the box to an older release"},
			flagDoc{"--remove-keys", "take off SSH keys this config no longer lists"}),
	},
	{
		name: "doctor", args: "[box]", group: "Maintain",
		summary:  "check the box works; exit 1 on any failure",
		examples: []example{{"keelbox doctor acme", ""}},
		flags:    append(append([]flagDoc{}, remote...), fVerbose),
	},
	{
		name: "status", args: "[box]", group: "Maintain",
		summary:  "short summary of the box",
		about:    []string{"With --all, one line per box in the registry, read from each box's status snapshot over ssh."},
		examples: []example{{"keelbox status acme", ""}, {"keelbox status --all", "every box; exit 1 unless all are reached and healthy"}},
		flags: append(append([]flagDoc{}, remote...),
			flagDoc{"--all", "every box in the registry, read at once"}, flagDoc{"--json", "the box's status snapshot, read now, as JSON; with --all, the rows"}),
	},
	{
		name: "reports", args: "[box]", group: "Maintain",
		summary: "read the keelbox reports the agent saved, and send one yourself",
		about: []string{
			"Reports never leave your box unless you send them. --send shows the exact text with",
			"tokens, addresses and paths redacted, lets you edit it, asks, then opens a prefilled",
			"GitHub issue in your browser for you to submit; security reports go to the private",
			"advisory form. keelbox posts nothing and uses no token.",
		},
		examples: []example{
			{"keelbox reports acme", "list the drafts"},
			{"keelbox reports acme --show 1", ""},
			{"keelbox reports acme --send 1", ""},
		},
		flags: append(append([]flagDoc{}, remote...),
			flagDoc{"--show N", "print report N in full, with the box facts keelbox added"},
			flagDoc{"--send N", "review, redact and edit report N, then open it as a GitHub issue"},
			flagDoc{"--delete N", "delete report N from the box"}),
	},
	{
		name: "requests", args: "[box]", group: "Maintain",
		summary: "list the packages the agent asked for that wait for you",
		about: []string{
			"With users.agent.permissions: manual, keelbox install on the box records a request and",
			"installs nothing. This lists what waits: who asked, when, the package names, which passed",
			"keelbox's checks, and the agent's stated reason, which is the agent's own text.",
		},
		examples: []example{
			{"keelbox requests acme", ""},
			{"keelbox requests acme --all", "also the requests already decided"},
		},
		flags: append(append([]flagDoc{}, remote...),
			flagDoc{"--all", "also the last requests that were installed, refused, failed or denied"}),
	},
	{
		name: "approve", args: "[box] [id]", group: "Maintain",
		summary: "let a package request of the agent's be installed",
		about: []string{
			"Marks the request approved and starts root's install service on the box, which checks",
			"it again, with the same bounds as any request, and installs it with apt-get. The install",
			"carries on if this window closes. Without an ID it approves the only request waiting.",
		},
		examples: []example{
			{"keelbox approve acme 3f2a9c1e", ""},
			{"keelbox approve acme --all", "every request that waits"},
		},
		flags: append(append([]flagDoc{}, remote...), flagDoc{"--all", "approve every request that waits"}),
	},
	{
		name: "deny", args: "[box] <id>", group: "Maintain",
		summary:  "turn down a package request of the agent's",
		about:    []string{"Nothing is installed; the agent's keelbox install --status says the owner denied it."},
		examples: []example{{"keelbox deny acme 3f2a9c1e", ""}},
		flags:    append([]flagDoc{}, remote...),
	},
	{
		name: "notify", args: "test [box]", group: "Maintain",
		summary: "send a test message on each notification channel",
		about: []string{
			"Runs on the box as root, through the same senders the box uses, with the secret files",
			"the config names. Exit 1 if any channel fails.",
		},
		examples: []example{{"keelbox notify test acme", ""}, {"keelbox notify test acme --channel phone", "one channel"}},
		flags:    append(append([]flagDoc{}, remote...), flagDoc{"--channel NAME", "only the channel called NAME"}),
	},
	{
		name: "update", args: "[box]", group: "Maintain",
		summary: "update keelbox on this computer, then the box, in one command",
		about: []string{
			"Installs the current keelbox release here, as self-update does, then applies",
			"it to the box, as apply does: the box gets that keelbox and what it brings.",
			"With the current release already here, it applies it.",
		},
		examples: []example{
			{"keelbox update acme", ""},
		},
		flags: append(append([]flagDoc{}, remote...), fVerbose,
			flagDoc{"--adopt", "accept what plan --impact marks on a machine in use, such as replacing Ubuntu's docker.io"},
			flagDoc{"--allow-downgrade", "move this computer and the box to an older release"},
			flagDoc{"--remove-keys", "take off SSH keys this config no longer lists"}),
	},
	{
		name: "add", args: "[box] [what]", group: "Maintain",
		summary: "add an agent CLI or a tool to a box that is set up",
		about: []string{
			"Sets it in the box's config, applies the config, and runs the sign-in it needs.",
			"With nothing named, lists what the box has and what can be added.",
			"Adds: claude-code, codex, gh, docker, node.",
		},
		examples: []example{
			{"keelbox add acme", "what acme has, and what can be added"},
			{"keelbox add acme codex", "install Codex, then sign in"},
			{"keelbox add acme codex --api", "sign in with an OpenAI API key"},
		},
		flags: append(append([]flagDoc{}, remote...), fVerbose,
			flagDoc{"--api", "with claude-code or codex: sign in for API billing once it is installed"}),
	},
	{
		name: "rollback", args: "[box]", group: "Maintain",
		summary:  "re-apply with the keelbox the box ran before",
		examples: []example{{"keelbox rollback acme", ""}},
		flags:    append(append([]flagDoc{}, remote...), fVerbose, flagDoc{"--remove-keys", "take off SSH keys a later apply added"}),
	},
	{
		name: "reboot", args: "[box]", group: "Maintain",
		summary: "restart the box now and wait until it is back",
		about: []string{
			"Saves the agent's tmux sessions and reboots the box. Programs in the sessions stop.",
		},
		examples: []example{{"keelbox reboot acme", "asks first"}},
		flags:    append([]flagDoc{{"--yes", "reboot without asking"}}, remote...),
	},
	{
		name: "backup", args: "[box]", group: "Maintain",
		summary:  "back up the box now with restic",
		examples: []example{{"keelbox backup acme", ""}, {"keelbox backup acme --list", "list snapshots"}},
		flags:    append(append([]flagDoc{}, remote...), flagDoc{"--list", "list snapshots"}),
	},
	{
		name: "restore", args: "[snapshot] [box]", group: "Maintain",
		summary: "restore a snapshot into a new directory",
		about:   []string{"The latest snapshot unless one is given; over the original path with --overwrite."},
		examples: []example{
			{"keelbox restore acme", "the latest, into a new directory"},
			{"keelbox restore 4f3a9c1e acme --overwrite", ""},
		},
		flags: append(append([]flagDoc{}, remote...),
			flagDoc{"--target PATH", "a new or empty directory to restore into"},
			flagDoc{"--overwrite", "restore over the original path, replacing files the snapshot has"}),
	},
	{
		name: "forget-token", args: "[box]", group: "This computer",
		summary:  "remove the Hetzner token from the credential store",
		examples: []example{{"keelbox forget-token", "the token keelbox up stored"}, {"keelbox forget-token acme", "the one acme uses"}},
	},
	{
		name: "completion", args: "<shell>", group: "This computer",
		summary: "print shell completion for bash, zsh or fish",
		examples: []example{
			{"keelbox completion zsh > ~/.zfunc/_keelbox", "then fpath+=(~/.zfunc) before compinit"},
			{"keelbox completion bash > ~/.local/share/bash-completion/completions/keelbox", ""},
			{"keelbox completion fish > ~/.config/fish/completions/keelbox.fish", ""},
		},
	},
	{
		name: "self-update", group: "This computer",
		summary:  "update keelbox to the current stable release",
		examples: []example{{"keelbox self-update", ""}},
		flags:    []flagDoc{{"--allow-downgrade", "install an older keelbox"}},
	},
	{
		name: "version", group: "This computer",
		summary:  "print versions (also: -v, --version)",
		examples: []example{{"keelbox version", ""}, {"keelbox -v", "the same"}},
	},
}

var groups = []string{"Set up", "Use", "Maintain", "This computer"}

func findCmd(name string) (cmdDoc, bool) {
	if name == "boxes" {
		name = "ls"
	}
	for _, c := range commands {
		if c.name == name {
			return c, true
		}
	}
	return cmdDoc{}, false
}

func (c cmdDoc) synopsis() string { return strings.TrimSpace(c.name + " " + c.args) }

// Help is laid out for the width it is read in: the terminal's, between
// 60 and 120 columns, and helpDocWidth off a terminal, which is the form
// docs/reference/cli.md carries. Prose wraps and hangs under itself; a
// command to type is never wrapped, whatever the width.
const helpDocWidth = 100

// helpCols is the width help is laid out in for st.
func helpCols(st ui.Style) int {
	if st.Width <= 0 {
		return helpDocWidth
	}
	return min(max(st.Cols(), 40), 120)
}

// overview is keelbox help: every command, one line each.
func overview() string { return overviewIn(ui.Style{}) }

// overviewCol is where a command's summary starts in the overview.
const overviewCol = 28

// overviewIn is the overview in st: group headings in brass, the commands
// to type in the accent.
func overviewIn(st ui.Style) string {
	cols := helpCols(st)
	var b strings.Builder
	b.WriteString(ui.Hang(st.Bold("keelbox")+": "+lowerFirst(ui.Tagline), cols, 0) + "\n\n")
	b.WriteString(st.Bold("Usage:") + " " + st.Cmd("keelbox COMMAND [box] [flags]") + "\n")
	for _, g := range groups {
		fmt.Fprintf(&b, "\n%s\n", st.Heading(g+":"))
		for _, c := range commands {
			if c.group == g {
				b.WriteString(helpRow(st.Cmd(c.synopsis()), c.summary, overviewCol-4, cols))
			}
		}
	}
	b.WriteString("\n")
	for _, para := range []string{
		"[box] is a box name from ~/.config/keelbox/boxes/ or a config file path. Left out: /etc/keelbox/keelbox.yaml on a box, else ./keelbox.yaml, else the only box in ~/.config/keelbox/boxes/; with several, keelbox asks which. Flags go after the command.",
		"",
		"Exit codes: 0 done; 1 failed; 2 usage, config, or an answer missing without a terminal, with nothing changed; 3 a step only you can do; 4 an apply carries on on the box; 130 Ctrl-C. ssh BOX -- CMD exits with the command's code, or 255 when ssh did not log in.",
		"",
		"keelbox COMMAND --help shows a command's flags and examples.",
	} {
		b.WriteString(ui.Hang(para, cols, 0) + "\n")
	}
	b.WriteString("Docs: " + st.URL(docsURL) + "\n")
	return b.String()
}

// helpRow is one row of help: a command or a flag in a column key wide,
// and what it does beside it, wrapped and hanging under itself. Where the
// width leaves that column too narrow to read, or the key is wider than
// its column, the text goes under the key instead. The key is never
// wrapped.
func helpRow(key, text string, keyWidth, cols int) string {
	const under = 6 // the indent of text put under its key
	beside := 2 + keyWidth + 2
	if text == "" {
		return "  " + key + "\n"
	}
	if ui.Width(key) > keyWidth || cols-beside < 28 {
		return "  " + key + "\n" + strings.Repeat(" ", under) + ui.Hang(text, cols-under, under) + "\n"
	}
	return "  " + ui.Pad(key, keyWidth) + "  " + ui.Hang(text, cols-beside, beside) + "\n"
}

// aboutLines are the lines after a command's summary: as they are written
// where each fits the width, and otherwise wrapped to it as one paragraph.
func aboutLines(about []string, cols int) string {
	if len(about) == 0 {
		return ""
	}
	for _, l := range about {
		if len([]rune(l)) > cols {
			return ui.Hang(strings.Join(about, " "), cols, 0) + "\n"
		}
	}
	return strings.Join(about, "\n") + "\n"
}

// commandHelp is keelbox COMMAND --help.
func commandHelp(c cmdDoc) string { return commandHelpIn(ui.Style{}, c) }

// commandHelpIn is a command's help in st.
func commandHelpIn(st ui.Style, c cmdDoc) string {
	cols := helpCols(st)
	var b strings.Builder
	b.WriteString(ui.Hang(st.Bold("keelbox "+c.synopsis())+": "+c.summary+".", cols, 0) + "\n")
	b.WriteString(aboutLines(c.about, cols))
	if len(c.examples) > 0 {
		b.WriteString("\n" + st.Heading("Examples:") + "\n")
		// The column fits the longest command that has a note, up to 44.
		width := 0
		for _, e := range c.examples {
			if e.what != "" && len(e.cmd) <= 44 {
				width = max(width, len(e.cmd))
			}
		}
		for _, e := range c.examples {
			b.WriteString(helpRow(st.Cmd(e.cmd), st.Dim(e.what), width, cols))
		}
	}
	if len(c.flags) > 0 {
		b.WriteString("\n" + st.Heading("Flags:") + "\n")
		width := 0
		for _, f := range c.flags {
			width = max(width, len(f.flag))
		}
		for _, f := range c.flags {
			b.WriteString(helpRow(st.Cmd(f.flag), f.help, width, cols))
		}
	}
	return b.String()
}

// startCol is the width of the first column of the start screen's rows:
// a box's name, a command.
const startCol = 14

// startCols is the width the start screen wraps to: the terminal's, or one
// line a row where there is none.
func startCols(st ui.Style) int {
	if st.Width <= 0 {
		return 1 << 20
	}
	return st.Cols()
}

// startScreen is keelbox with no arguments: what it is, the two commands
// to start with, the boxes there are, and where the docs are.
func startScreen(out io.Writer, st ui.Style) {
	if b := st.Banner(version.String(), ui.Tagline); b != "" {
		fmt.Fprintln(out, b)
	} else {
		fmt.Fprintf(out, "%s %s\n", st.Bold("keelbox"), st.Dim(version.String()))
		fmt.Fprintln(out, ui.Tagline)
		fmt.Fprintln(out)
	}
	boxes := append(registeredBoxes(), pendingBoxes()...)
	if len(boxes) > 0 {
		fmt.Fprintln(out, st.Heading("Your boxes:"))
		for i, b := range boxes {
			if i == 5 {
				fmt.Fprintf(out, "  ... and %d more: %s\n", len(boxes)-5, st.Cmd("keelbox ls"))
				break
			}
			r := listRow(b)
			fmt.Fprint(out, helpRow(st.Bold(b), statusCell(st, r.status), startCol, startCols(st)))
		}
		fmt.Fprintln(out)
	}
	if st.Live {
		fmt.Fprintln(out, st.Heading("Start here:"))
	}
	row := func(cmd, what string) {
		fmt.Fprint(out, helpRow(st.Cmd(cmd), what, startCol, startCols(st)))
	}
	row("keelbox up", "set up a box: a server you reach over SSH, this machine, or a new one")
	row("keelbox ls", "list your boxes")
	row("keelbox help", "every command; keelbox COMMAND --help for one")
	fmt.Fprintf(out, "\nDocs: %s\n", st.URL(docsURL))
}
