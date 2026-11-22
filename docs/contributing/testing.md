# Testing

The layers run from free and fast to slow and billed. Every change needs the first two; changes to Docker, sshd, tmux environment or the agent's network need the third as well.

| Layer | Command | Needs | Time | Cost |
|---|---|---|---|---|
| Unit and sim | `make check` | `mise install` | 1 to 2 min | none |
| Installer smoke | `make test-install` | goreleaser (via mise), python3 | 1 to 2 min | none |
| LXD e2e | `make e2e` | LXD with KVM | about 2 h 45 min; about 40 min with `KEELBOX_E2E_JOBS=6` | none |
| Hetzner e2e | `make e2e-hetzner` | a Hetzner API token | about 30 min for the box check (`TestBox`), 11 to 14 min for `TestResizeParkResume`, more for the whole run | about EUR 0.01 a server |
| Contract, real CLI | [`go test` with the CLI named](#contract-tests-against-the-real-clis) | the vendor's CLI binary, no account | under 1 min | none |
| AWS e2e | `make e2e-aws` | the AWS CLI, signed in | about 25 min; about 23 min for `TestResizeParkResume` | about USD 0.02 a run (one hour of the bundle) |
| DigitalOcean e2e | `make e2e-digitalocean` | doctl, signed in | about 45 min; about 20 min for `TestResizeParkResume` | about USD 0.03 a run (one hour of the size) |
| Google Cloud e2e | `make e2e-gcp` | gcloud, signed in | about 35 min; about 26 min for `TestResizeParkResume` | not known: gcloud prints no price (one e2-small, a 20 GB disk and an address for the run) |

Never run `keelbox apply` against your own workstation. It hardens sshd, enables UFW and rewrites Docker's `daemon.json`. Use a VM.

## make check

`scripts/check.sh`, also what CI runs: `gofmt`, `go vet` (also for darwin and linux/arm64, so the laptop CLI builds everywhere, and for the e2e harness), `golangci-lint`, `shellcheck`, `actionlint`, then `go test -race ./...`. It must pass before every push.

What the unit tests cover, by package:

- `config`: defaults, validation messages, strict decoding, key file inlining. `TestConfigReferenceListsEveryKey` fails when `docs/reference/config.md` misses a schema key or lists one that does not exist.
- `cli`: flag parsing, exit codes, the downgrade guard, session names, the sign-in table, the box registry and name resolution, the Hetzner token order and prompt against a fake API, and `keelbox up` (`up_test.go`): every choice and its config, the questions it may not ask without a terminal, the stops at exit 3 and resuming, the cloud-config path, run with scripted input against `hetznertest` and a fake runner that records create, apply and login. `park_test.go` and `resize_test.go` run `keelbox resize`, `park`, `resume` and `destroy` against the same fake, which keeps servers, firewalls and snapshots and can be told to fail a call: the order of park's calls, a server never deleted without a snapshot read back as available, what each failure leaves and says, the pinned host keys moved to the new address, every other command stopping on a parked box, and the size key rewritten in place. `awspark_test.go`, `digitaloceanpark_test.go` and `gcppark_test.go` run the same commands against `awstest`, `dotest` and `gcptest`: park then resume, no server deleted without a confirmed snapshot, a resize and a resize that fails, destroy of a parked box, nothing touched that lacks keelbox's tags, and on AWS the check for the cloud-init file on the box and a resize that leaves the box parked. `parksteps_test.go` and `parkend_test.go` hold what the three commands show while they run and at their end, on each cloud and at 60, 80 and 120 columns, and `parkterminal_linux_test.go` runs them on a pseudo-terminal with the keys a person presses; with `KEELBOX_SHOTS=DIR` it saves each screen, which is where the transcripts in the [guide](../guides/resize-park-resume.md) come from. `TestCLIReferenceMatchesHelp` fails when `docs/reference/cli.md` differs from `keelbox help`.
- `engine`: Plan, Apply and Doctor semantics, the step helpers, soft and deferred steps.
- `system`: root path checks and file access as the owner.
- `modules`: every module against the simulated box ([architecture](architecture.md#the-simulated-box)). `TestApplyConvergesAndIsIdempotent` is the one that must never go red.
- `provider`: host-key parsing, pinning and rotation; the cloud-config snippet; the Hetzner client, token check, server type offers and SSH key upload against an `httptest` API (`hetzner/hetznertest`); stop, start, type change, snapshot and restore from a snapshot against the same fake (`hetzner/park_test.go`), and the same calls on Lightsail, DigitalOcean and Google Cloud against the fakes of their CLIs (`park_test.go` in `aws`, `digitalocean` and `gcp`).
- `prompt`: numbered choices, defaults, retries and end of input; the hidden prompt turns echo off on a real pty.
- `credstore`: the Keychain and Secret Service stores against a fake `security` and `secret-tool` (the token must reach them on stdin, never in arguments), and the mode 600 file fallback.
- `release`, `selfupdate`: signatures, the host allowlist, index agreement and the self-update downgrade floor, against a local server with throwaway keys.

Run one package or test while working:

```sh
mise exec -- go test ./internal/modules/ -run TestApplyConvergesAndIsIdempotent -v
```

### Writing a sim test

```go
func TestMyStep(t *testing.T) {
	s := newSim(t)                                   // fresh Ubuntu 26.04, cloud user ubuntu
	e := simEnv(t, s, testConfig(t, "runtime:\n  node: false\n"))
	ctx := context.Background()
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatal(err)
	}
	if !s.Ran("systemctl enable --now my.service") { // commands are matched as rendered
		t.Error("my.service not enabled")
	}
}
```

`s.Ran(prefix)`, `s.Commands()`, `s.ReadFile(path)` and `s.Paths()` inspect what happened. `s.OK(prefix, stdout)` and `s.Fail(prefix)` answer a command differently for one test; later registrations win. A command the sim does not know exits 1, which most checks read as "not converged", so a new step's command usually needs a `case` in `sim.run`.

## make test-install

Builds a snapshot release with goreleaser, serves it on `127.0.0.1` in the layout of get.keelbox.dev, signs it with a throwaway release key (never the real one), and runs `get/install.sh` through a fresh install (with completion and the uninstall line), a rerun, the PATH line per shell and adding it on a terminal, `--prefix`, `up` after installing, a pinned version, the `ssh-keygen` path for an openssl without Ed25519, then each refusal: no tool to check the signature, `SHA256SUMS` signed by an untrusted key, index and `SHA256SUMS` disagreeing, an unknown release host, a tampered binary, and a cosign that does not run. It also checks the snapshot's `SHA256SUMS` lists `get/install.sh`. Then `keelbox version`, `init` and `plan`. Run it after changing `get/install.sh`, `.goreleaser.yaml` or `internal/release`.

## Homebrew formula

`scripts/test-homebrew.sh [DIST]` writes the tap's formula for a snapshot (`scripts/homebrew-formula.sh`) with `file://` URLs, puts it in a local tap, and runs `brew style`, `brew install` and `brew test`, then checks the bash, zsh and fish completion files. CI runs it on macOS after the installer test; on Linux, run it in the `homebrew/brew` image: `docker run --rm -v "$PWD":/src:ro homebrew/brew bash -c 'cp -r /src /tmp/kb && cd /tmp/kb && scripts/test-homebrew.sh'`.

## make e2e (LXD)

`scripts/e2e.sh lxd` runs `test/e2e` against a fresh LXD virtual machine:

1. Builds keelbox from your tree.
2. Launches `ubuntu:26.04` as `kb-e2e-N` (2 vCPU, 2 GiB), with an SSH key delivered by cloud-init and the host key read out of band.
3. Bootstrap apply as `ubuntu`, a second apply that must change nothing, then doctor.
4. Feature checks (`features_test.go`): rootless Docker as the agent, the system socket closed to the agent, agent and container SSH to the box refused while `github.com:22` is allowed, published ports reachable on the box but not from outside, the cloud user locked, root not expired, attach and sessions, the downgrade guard, rollback on the box and from the laptop. The network limits as they apply to every account but root: `nobody` is refused the box's SSH port and the metadata address, root reaches SSH, and names, time and `apt-get update` still work with the table loaded. `keelbox install` as the agent: with `users.agent.permissions: auto` a package is installed, and a deny-listed name, a version, an option, a local file, a removal, a request written by hand and a link in place of the request file are refused; with `manual` a request is recorded and nothing installed, `keelbox requests` lists it, `keelbox approve` from the laptop has it installed and `keelbox deny` turns another down. The welcome of a new tmux session, with its `Packages` and `Need root?` lines in both modes.
   Previews and backups (`previews_backup_test.go`): `expose` refused off the tailnet; a dev server exposed by the agent and fetched over HTTPS; Caddy listening on tailnet addresses only, the preview port unreachable on the LAN address, the UFW rule and `IPAddressDeny`; hostile and symlinked previews files refused. Then a MinIO container on the VM (`pgsty/minio`, loopback only) as the bucket: two backups, `--list`, restore into a new directory and `diff -r`, a restore into a non-empty directory refused, a deleted file brought back with `--overwrite`, and the bucket secrets searched for in output, the logbook, the applied config and the journal.
5. Reboot, then the checks again.
6. Deletes the VM, and sweeps any other `kb-e2e-*` VM.

Needs a working `lxc` with KVM; it never changes LXD's own configuration. Because of the sweep, two runs on one LXD host need different `KEELBOX_E2E_VM_PREFIX` values. The transcript goes to `e2e-logs/` (git-ignored).

`make e2e` runs every test in `test/e2e`, one after another: about 2 h 45 min on LXD. `KEELBOX_E2E_JOBS=6 make e2e` runs six at a time, each test as its own process with its own prefix (`kb-box-`, `kb-notify-`), and finishes in about 40 minutes; logs go to `e2e-logs/lxd-<time>/<test>.log` with a `summary` beside them. Each VM takes 2 vCPU and 2 GiB and some tests use two, so allow about 5 GiB of free memory per job. A test that fails only in a parallel run (a download or a name lookup that timed out) should be run again alone before it is believed: `KEELBOX_E2E_TARGET=lxd KEELBOX_E2E_VM_PREFIX=kb-name- go test -tags e2e -run 'TestName$' -v ./test/e2e/`. The cloud targets always run one test at a time.

Knobs: `KEELBOX_E2E_IMAGE=24.04`, `KEELBOX_E2E_KEEP=1` (keep the VM for debugging; delete it yourself), `KEELBOX_E2E_UPGRADE_FROM=<commit>` (apply with keelbox built from that commit first, publish a preview with it on the stand-in tailnet, then upgrade with your tree: the upgrade must leave the preview's Caddy config current, rewriting one an older keelbox wrote, end its run log `ok` and update Remote Control's unit; with `KEELBOX_E2E_IMAGE=24.04` it must also load the bwrap profile, replacing the copy an older keelbox wrote with Ubuntu's own).

Public SSH (`publicssh_test.go`), last in the run because everything before it uses the VM's public address: a stand-in `tailscale` reports a running tailnet at 100.100.1.1, which the test host cannot reach, and apply must leave SSH open ("no proof, no close"). Then tailscaled moves to userspace networking, a macvlan named `tailscale0` gets a second address on the LXD network, and an nftables DNAT sends its port 22 to 100.100.1.1; with that address in `target.ssh.tailnet_host`, apply closes public SSH, the public port stops answering, `apply`, `doctor`, `status` and `sessions` work over the stand-in, and `public_ssh: open` reopens it. Remote Control is enabled in the e2e config and checked waiting for a sign-in (installed, inactive, no restarts, doctor and `sessions` say so); a live Remote Control run needs a real claude.ai sign-in and is not tested.

Tailnet: if `~/.config/keelbox/tailscale-e2e.key` exists (mode 600; ideally an ephemeral, pre-approved auth key for a tailnet with HTTPS certificates on), the VM joins that tailnet and the preview is also fetched from the test host over it. Without it, a stand-in `tailscale` on the VM reports 100.100.1.1 on `tailscale0` and issues a self-signed certificate; the log says the tailnet fetch was not run.

A pull request that touches Docker, sshd, tmux environment or `agentnet.go` must add a check to `features_test.go` and pass `make e2e`.

### Notifications

`notify_test.go` (`TestNotify`) runs on its own LXD VM, with a config that has a webhook channel and an ntfy channel and nothing outside the VM: a webhook receiver (`python3`) and an ntfy container (`binwiederhier/ntfy`) on the VM's loopback, which keelbox reaches over plain `http://` because it allows that to loopback. It checks the secret copies (0600 root, unreadable by the agent) and the managed hook files; `keelbox notify test` reaching both; a real `claude -p` with a bogus API key, whose managed `StopFailure` hook must give exactly one `agent-limit` delivery even after another watch run; the Codex hook command from `requirements.toml` as `agent-needs-input`; forged lines (root kinds, a level, a source, text with `agent_text` off) that raise nothing; scheduled doctor with the watch timer stopped and started again, `doctor-failed` and `doctor-cleared` exactly once each over repeated runs; `status` and `doctor`; and the channel secrets in no output, state file, policy file, journal line or process command line. Run it alone with `KEELBOX_E2E_TARGET=lxd KEELBOX_E2E_VM_PREFIX=kb-name- go test -tags e2e -run 'TestNotify$' -v ./test/e2e/`. It needs Docker Hub for the ntfy image.

`agents_test.go` (`TestVendorAgentCLIs`) runs on its own LXD VM with a minimal config: Claude Code and Codex from the vendors' installers in the agent's home, `claude update` and `codex update` run as the agent, and no agent store left on the box. Run it with `-run 'TestVendorAgentCLIs$'`; it downloads the vendors' current releases.

`sandbox_test.go` (`TestAgentSandboxes`) runs on its own LXD VM with Claude Code and Codex: the bwrap AppArmor profile is Ubuntu's on both, loaded by keelbox from Ubuntu's `apparmor-profiles` package on 24.04 (which stays uninstalled) and by Ubuntu itself on 26.04, with `kernel.apparmor_restrict_unprivileged_userns` still 1; doctor's `sandbox` and `codex-sandbox` pass; `codex sandbox` in workspace-write runs a command and writes a file in the workspace and is refused outside it; a program bwrap starts has no capabilities and cannot map a user namespace, and the agent cannot outside bwrap; Anthropic's sandbox runtime (`@anthropic-ai/sandbox-runtime`, Claude Code's sandbox) runs a command with `allowAllUnixSockets`, and the run logs whether its Unix socket filter works under Ubuntu's profile (it does not on 24.04 or 26.04). Run it on both releases: `KEELBOX_E2E_IMAGE=24.04` and the default. A live Codex or Claude Code turn needs a signed-in account and is not tested.

`devserver_test.go` (`TestPreviewDevServer`) installs a stock Vite 6 with no `allowedHosts` as the agent, exposes port 5173 before the server runs (the first expose must say it waits for the certificate, and say nothing listens), starts Vite on `[::1]` only (the test fails if it also listens on IPv4), then checks `expose --list` sees it, Vite itself answers 403 to the tailnet name, and the preview fetched over HTTPS returns the page with 200. It uses the same stand-in tailnet as the preview checks unless `tailscale-e2e.key` exists.

`reports_test.go` (`TestAgentOnBox`) runs keelbox as the agent on its box: `status` reads the snapshot without sudo, `ssh` says the agent is already there and `doctor` says it needs root without asking sudo for a password. Then the agent files a keelbox report with a token and a home path in it; the laptop reads it from root's inbox (`reports`, `--show 1`, with the box facts), the agent cannot list the inbox, no event in the queue names it, `--send` refuses without a terminal, and `--delete` removes it.

`work_test.go` (`TestWorkOnBox`) runs on its own LXD VM with no agent CLIs and `users.agent.git` set ([ADR 0064](../adr/0064-work-on-the-box.md)): the agent's `user.name`, `user.email` and `init.defaultBranch` after apply, an alias and a default branch of the agent's own kept through the next apply and a changed email put back; then the agent makes a project in each state (not a repository, no remote, commits not pushed, uncommitted changes, clean and pushed), one whose name holds an escape sequence and a symlink to `/etc`. `keelbox status` from the laptop and as the agent, doctor's `projects` warning and `status --json` (what `keelbox destroy` reads) must agree, with no escape sequence and no symlink in any of them and nothing root-owned left in the projects; doctor is ok once everything is pushed; and with the key removed nothing is written and doctor warns that commits will fail. The destroy question itself is tested against the fake Hetzner API in `internal/cli`. Run it with `KEELBOX_E2E_TARGET=lxd KEELBOX_E2E_VM_PREFIX=kb-name- go test -tags e2e -run 'TestWorkOnBox$' -v ./test/e2e/` (about 3 minutes).

### keelbox up

`up_test.go` (`TestUp`) runs `keelbox up` as an agent or CI would, with no terminal and every answer a flag, with its own `HOME`, so the registry, `known_hosts` and the file credential store start empty. On LXD:

- existing server: a fresh `ubuntu:26.04` VM with the key on `ubuntu`; `up --provider ssh --host IP --ssh-user ubuntu --host-key FPR --yes` applies as `ubuntu`, then as `admin`.
- existing server, new key, on a terminal (`upterminal_test.go`): a VM that takes only a password for `ubuntu`, and a `HOME` with no SSH key. `keelbox up` runs on a 120x50 pseudo-terminal, answered as each question shows: it makes the key with `ssh-keygen`, the server refuses it, `ssh-copy-id` puts it there with the password, the check passes, the box name defaults to the hostname (no rename question), a git identity is typed, the agent's permissions stay at the default (`auto`), restarts stay at the default (nightly), the LAN keeps SSH (`public_ssh: lan`), and both sign-ins (Tailscale, GitHub) are skipped. `up` must exit 0 with `NAME is set up` and both under "Left to do", `ls` must say `ready, sign-ins left (tailscale, gh)`, the hostname must be unchanged, the admin must have the new key, the agent's git config the typed identity, no agent CLI installed and `ubuntu` locked. The terminal output is kept in `$KEELBOX_E2E_LOG.pty`.
- cloud-config: `up --provider cloud-init --yes` prints the snippet and exits 3; the test launches a VM with it as `cloud-init.user-data` (LXD standing in for a provider console) and runs `up NAME --host IP`, which must pin the snippet's host key, rotate it away and delete the saved snippet; the image's `ubuntu` account must not exist.

- this machine: a VM with the key on `ubuntu` and two services listening; the snapshot release (`make e2e` builds it, signed with a throwaway key) is served on the VM's own `127.0.0.1`. The installer runs piped (`curl | sh -s -- --test`), then `keelbox up NAME --provider local --lan-ports 8095` runs on a pseudo-terminal and gets Ctrl-C while the runtime module applies: it must exit 130, saying how to continue, with the progress view in colour. `curl | sh -s -- --test up NAME` must then continue the box to sign-in. The config must keep `ubuntu` unlocked, the machine's timezone and port 8095; 8095 must be reachable from the test host and 8096 not; a third `up` resumes at sign-in and `sudo keelbox plan` on the box shows no changes.

The first two must stop at sign-in with exit 3, `ls` must show "needs sign-in", a second `up` must resume there without applying, `plan NAME` must show no changes, and the agent CLIs must be installed and signed out. VMs are named `kb-up-N` (`KEELBOX_E2E_UP_PREFIX` changes the prefix, to keep runs apart) and deleted, with a sweep of any other VM with the prefix. With `KEELBOX_E2E_TARGET=hetzner`, the same against a new Hetzner server (`up --provider hetzner`), with the token copied file to file into the test `HOME`'s credential store, never on a command line, and `TMPDIR` as long as macOS's per-user one, so ssh's control socket must fall back to `/tmp`. The first `up` is left with SIGTERM while the server applies the runtime module: it must exit 4 and name `keelbox up NAME`, the server must finish the apply, and the next `up` must report it once before stopping at sign-in. The run checks the token is not in the output and destroys the server.

`detach_test.go` (`TestDetach`) checks that an apply survives losing the laptop ([ADR 0063](../adr/0063-detached-apply.md)), one LXD VM at a time, with keelbox in its own `HOME` and no terminal. `laptop-killed`: `keelbox up` is killed (SIGKILL) while the box applies the runtime module; the box's run log must end `ok`, and the next `up` must report the run once (`setup on NAME finished ...: N steps, M changed, ok`) and end with `NAME is set up`. Then, with the run held by SIGSTOP so the order is certain: a second `apply` while one runs must follow it (one run, both exit 0); an apply left running whose laptop is killed must send exactly one `apply-finished` to a webhook on the VM's loopback, and the next `apply` must report it once and start no run; a hard reset (`lxc restart --force`) mid-run must be reported by the follower (exit 1), by `status` (`setup interrupted`) and by doctor (`FAIL setup-run`) until the next apply. `connection-dropped`: nftables on the VM drops SSH both ways for 100 seconds mid-apply; `up` must say it lost and regained the connection and end with `NAME is set up`. Run it with `KEELBOX_E2E_TARGET=lxd KEELBOX_E2E_DETACH_PREFIX=kb-name- go test -tags e2e -run TestDetach -v ./test/e2e/` (about 20 minutes).

## make e2e-hetzner

The same test against a real Hetzner server: `keelbox create` with the cheapest shared x86 type orderable in an EU location, Ubuntu 26.04 (24.04 if 26.04 is not offered), labelled `keelbox-e2e=true`, deleted at the end, plus a sweep of any server with that label. It refuses to run with two such servers already present.

Needs a read-write API token at `~/.config/keelbox/hetzner.token` (mode 600) in a project you are happy to bill. It registers the e2e key as project SSH key `keelbox-e2e` if missing. The run prints its estimated spend; the 2026-11-06 run on cpx02 cost about EUR 0.01. If a run is interrupted, check the Hetzner console for a leftover `kb-e2e-*` server, `keelbox-kb-e2e-*` firewall and a snapshot described as `keelbox: parked box kb-e2e-*`; the next run's sweep deletes them.

`KEELBOX_E2E_TARGET=hetzner go test -tags e2e -run TestHetznerPublicSSH ./test/e2e/` is the Cloud Firewall test on its own: create (firewall attached, 22 and UDP 41641), rules tampered with and the firewall detached through the API then restored by apply, SSH kept with no reachable tailnet, then a tunnel to the box's stand-in tailnet address as `tailnet_host`: apply closes public SSH, 22 leaves the Cloud Firewall, and `destroy` deletes server and firewall.

### TestResizeParkResume

`KEELBOX_E2E_TARGET=hetzner go test -tags e2e -run 'TestResizeParkResume$' -v -timeout 80m ./test/e2e/` (`park_test.go`) takes one server through `keelbox resize`, `park`, `resume` and `destroy` ([ADR 0071](../adr/0071-resize-park-resume.md)). It runs on every cloud target: `hetzner`, `aws`, `digitalocean` and `gcp`. After create it writes a file as the agent, puts data in a Docker volume of the agent's and starts a container with a restart policy, and reads the server's ed25519 host key fingerprint with `ssh-keyscan`. Then:

- resize up one size: the memory or vCPU count must change, the config file must say the new size, doctor must pass, and the data and the container must be there. Then back down, to the same shape as before. On AWS, where a box cannot be made smaller, the resize down is replaced by a check that the smaller size is refused, exit 2, and the box stays at the bigger size for the rest of the test.
- `resize` and `park` without a terminal and without their flags must exit 2 and change nothing.
- park: the cloud must hold no server, one snapshot with the parked labels and the firewall; `status`, `apply`, `doctor`, `sessions` and `create` must stop with the line that names `keelbox resume`; a second `park` must exit 0. With no server, the count also fails on Google Cloud if a disk or the reserved address of the box is left, and on AWS if its static IP is.
- resume: the cloud must hold one server and no snapshot, doctor must pass over a connection that checks the host key strictly, the host key fingerprint must be the same, the file, the volume and the container must be there, a further apply must change nothing, and `checks` and `isolationChecks` of the main test must pass. What decides who can log in and become root is read on the box before the park and again after the resume and must be the same: every account with its uid, shell and password state, the members of the privileged groups, the sudo rules and each account's authorised SSH keys. When the box comes back at another address, the old address must be gone from keelbox's `known_hosts` and resume must name both. On Hetzner a placeholder server of the harness holds the address the box gave up while it resumes, so that it does come back at another one.
- a second park, a `destroy --yes` that must refuse without `--discard-work`, and the destroy that must leave no server, snapshot or firewall.

What it took in our tests, on the size the target picks, each time for the whole command (a resize and a resume include their apply):

| Target | Whole test | resize | park | resume |
|---|---|---|---|---|
| Hetzner | 11 to 14 min | 61 to 91 s | 47 to 118 s | 58 to 131 s |
| DigitalOcean (`fra1`, `s-2vcpu-2gb`, 60 GB disk) | about 20 min, 5 of them the create | about 2.5 min each way | 1.5 to 2 min | about 2.5 min |
| Google Cloud (`europe-west3-a`, `e2-small`, 20 GB disk) | about 26 min, 9 of them the create | 2.5 to 3 min | about 2.5 min | about 2 min |
| AWS (`eu-west-1`, `small_3_0`, 60 GB disk) | about 23 min | 8 min 42 s up (the instance is replaced); down is refused | about 2 min | about 1.5 min |

The test has passed on a live account on each of the four clouds.

The cards before `keelbox park`'s and `keelbox resize`'s questions say these times, rounded to minutes, as what the commands typically take (`typical()` in each cloud's `*park.go` in `internal/cli`). When a run measures something else, change both.

On Hetzner it costs under EUR 0.02 on the smallest type (the box and the placeholder, an hour each), and the snapshot was 2.5 to 2.6 GB. On DigitalOcean the snapshot of the fresh box was 6.7 GB; on Google Cloud 2.6 GB were stored of the 20 GB disk. Each target's sweep also deletes the parked snapshots of the harness's boxes, which a run that failed between park and resume leaves: on Hetzner the snapshots labelled `keelbox-e2e=true`, `keelbox=true` and `keelbox-parked=true`, and the placeholder.

The test is written against the harness's `parker` interface (`SizeKey`, `Size`, `BiggerSize`, `Holds`), which a target implements beside `target`. All four cloud targets implement it; for a target that does not, the test is skipped and says it is not a cloud target, which resize, park and resume need. Two more interfaces are optional: `oneWay` (`CannotShrink`), for a cloud that cannot change a server back to a smaller size, which the AWS target implements, and `HoldAddress`, which the Hetzner target implements. `BiggerSize` reads the catalogue again when it is called: what a cloud can deliver in a location changes by the minute, and a size picked at the start of the run may be gone when the resize comes. The runs had no tailnet key, so the public address was open throughout: a park and resume of a box that has closed public SSH, and the Tailscale rejoin, are not covered by a live run.

## make e2e-aws

The same test against a new Lightsail instance: `keelbox create` with the cheapest Linux bundle that has 2 vCPU, 2 GB and a public IPv4 address, the default blueprint, tagged `keelbox-e2e=true` through `target.aws.tags`, deleted at the end with its static IP. Before and after, a sweep deletes every instance tagged both `keelbox=true` and `keelbox-e2e=true`, the snapshots `keelbox park` and `keelbox resize` made of the harness's boxes, and releases unattached static IPs named `keelbox-kb-e2e-*` (static IPs carry no tags). It refuses to run with two such instances already present, and prints its estimated spend.

Needs the AWS CLI on `PATH`, signed in to an account you are happy to bill. `KEELBOX_E2E_AWS_PROFILE` names the profile and `KEELBOX_E2E_AWS_REGION` the region (default `eu-central-1`). Without the CLI or a sign-in the test is skipped as pending. If a run is interrupted, check the Lightsail console for a leftover `kb-e2e-*` instance, `keelbox-kb-e2e-*` static IP and `keelbox-kb-e2e-*` snapshot; the next run's sweep removes them.

Everything the provider does is also tested without AWS: `internal/provider/aws` and `internal/cli` run against `awstest`, an in-memory Lightsail account behind the AWS CLI's arguments, JSON and exit codes. The [contract tests](#contract-tests-against-the-real-clis) hold the real AWS CLI to what `awstest` assumes.

## Contract tests against the real CLIs

A provider that runs a vendor's CLI is unit-tested against a fake of that CLI, and the fake is only as right as its author's reading of the documentation. A contract test closes that gap without an account: it runs the provider's own code through the real CLI binary, with the CLI pointed at a stub of the vendor's API on `127.0.0.1`. Every command the provider issues goes through the CLI's own argument parsing, the stub sees the request the CLI makes of it, and keelbox reads what the CLI really prints, on success and on failure.

They cost nothing and need no sign-in. The CLI gets a temporary `HOME` and its own configuration files, so it does not read or write yours, and made-up credentials in the test's environment. Each test is skipped unless an environment variable names the CLI binary, so `make check` and CI do not run them. Run them when you change a command the provider issues, how it reads the CLI's output or errors, or the fake; and against a new release of the CLI.

When a contract test fails, fix the provider, then make the fake behave as the real CLI did, in the same pull request.

### AWS CLI

`internal/provider/aws/realcli_test.go`, with `KEELBOX_TEST_AWS_CLI` naming the `aws` binary (version 2; written against 2.40):

```sh
KEELBOX_TEST_AWS_CLI=/path/to/aws mise exec -- go test ./internal/provider/aws/ -run TestRealCLI -v
```

About 35 seconds. The CLI is sent to the stub with `AWS_ENDPOINT_URL`; the stub answers Lightsail (JSON 1.1) and STS (the query protocol, XML) with responses in the services' shapes.

An AWS CLI you already have will do: `KEELBOX_TEST_AWS_CLI="$(command -v aws)"`. To get one without installing it, unpack AWS's Linux archive anywhere and name the binary inside it (the [install guide](https://docs.aws.amazon.com/cli/latest/userguide/getting-started-install.html) has the arm64 archive and how to check the archive's signature):

```sh
curl -fsSLo /tmp/awscliv2.zip https://awscli.amazonaws.com/awscli-exe-linux-x86_64.zip
unzip -q /tmp/awscliv2.zip -d /tmp/awscli
export KEELBOX_TEST_AWS_CLI=/tmp/awscli/aws/dist/aws
```

What it checks:

- Every command of preflight, create, find, the firewall calls and destroy is accepted (the CLI exits 252 for arguments it refuses), with the global options after the operation's own, and `--region` is the region the request is signed for.
- `--user-data file://PATH` reaches Lightsail as the file's content, the host key is in no argument, and the file is removed.
- The `--tags key=...,value=...` and `--port-infos fromPort=...,cidrs=...` shorthand arrive as the structures keelbox means: several tags, every character a tag may hold, an empty value, ports as numbers and address ranges as lists.
- The CLI follows `nextPageToken` for `get-instances`, `get-bundles` and `get-blueprints` and prints one list.
- Errors: a missing resource, access denied, credentials STS refuses, a throttled call, no credentials (exit 253), an unknown profile and an endpoint that does not answer (exit 255), each with the code, message and exit code keelbox reports.
- `AWS_CLI_ERROR_FORMAT=legacy`, which keelbox sets, wins over the user's `cli_error_format`, and keelbox reads both that format and the CLI's default.
- `ConfiguredRegion` is the region the CLI itself uses when a command names none: `AWS_REGION`, `AWS_DEFAULT_REGION`, then the profile.
- `TestRealCLIPark`: every command `keelbox resize`, `park` and `resume` issue (stop, the forced stop, start, creating, reading, listing and deleting a snapshot, `tag-resource`, `create-instances-from-snapshot` with its launch script, `get-bundles --include-inactive`, `detach-static-ip`) is accepted by the CLI and arrives as the request keelbox means, and keelbox reads what the CLI prints for a snapshot, its timestamps included.

The stub is not AWS. Whether Lightsail accepts a request, what it answers, and how long an instance takes are only tested by `make e2e-aws`.

### doctl

`internal/provider/digitalocean/realcli_test.go`, with `KEELBOX_TEST_DOCTL` naming the `doctl` binary (written against 2.4.0):

```sh
KEELBOX_TEST_DOCTL=/path/to/doctl mise exec -- go test ./internal/provider/digitalocean/ -run TestRealCLI -v
```

A few seconds. doctl is sent to the stub with `DIGITALOCEAN_API_URL`, and gets its token from `DIGITALOCEAN_ACCESS_TOKEN` in the test's environment, which doctl reads for its default context; the token is on no command line. The stub is a small in-memory DigitalOcean API: Droplets and their actions, snapshots, firewalls, tags, SSH keys and the catalogue.

A doctl you already have will do: `KEELBOX_TEST_DOCTL="$(command -v doctl)"`. To get one without installing it, unpack a release archive anywhere, after checking it against the release's checksum file:

```sh
v=2.4.0
cd "$(mktemp -d)"
curl -fsSLO "https://github.com/digitalocean/doctl/releases/download/v$v/doctl-$v-linux-amd64.tar.gz"
curl -fsSLO "https://github.com/digitalocean/doctl/releases/download/v$v/doctl-$v-checksums.sha256"
grep linux-amd64.tar.gz "doctl-$v-checksums.sha256" | sha256sum -c -
tar xzf "doctl-$v-linux-amd64.tar.gz"
export KEELBOX_TEST_DOCTL="$PWD/doctl"
```

What it checks:

- Every command of preflight, create, find, the firewall calls and destroy is accepted by doctl, with `--output json`, `--interactive=false` and `--context` after the command's own flags.
- `--user-data-file PATH` reaches the API as the file's content, the host key is in no argument, and the file is removed.
- `--tag-names a,b,c` arrives as a list of tags, `--ssh-keys` as the account key's id, and the `--inbound-rules` and `--outbound-rules` strings as rule structures with both address families; an update keeps the firewall's tag.
- keelbox reads what doctl prints for a Droplet (its public address among several), a firewall, a tag, the account and the catalogue.
- doctl follows the API's pages for a list and prints one list.
- Errors, which doctl prints as JSON on standard output with exit 1: a missing resource (404), a token without the scope (403), a token the API refuses (401), no token, a context doctl does not have, and a flag doctl does not know (plain text on standard error).
- `--context` selects a token saved in doctl's config file, and the environment's token is not used for it.
- `TestRealCLIResizeParkResume`: the commands and flags of `keelbox resize`, `park` and `resume` exist and send the request keelbox means. The resize asks for the size and not for the disk; the snapshot is named, then tagged as an image, one tag a call; the Droplet resume creates has the snapshot as its image, the box's tags and the user data that keeps what is on the disk, and is named in the firewall by id; a deleted snapshot has its tags taken off.

The stub is not DigitalOcean. Whether the API accepts a request, how the image treats the cloud-config, how soon a new Droplet is behind its firewall, and how long a shutdown, a snapshot or a resize takes are only tested by `make e2e-digitalocean`.

### gcloud

`internal/provider/gcp/realcli_test.go`, with `KEELBOX_TEST_GCLOUD` naming the `gcloud` binary (written against Google Cloud SDK 593.0.0):

```sh
KEELBOX_TEST_GCLOUD=/path/to/google-cloud-sdk/bin/gcloud mise exec -- go test ./internal/provider/gcp/ -run TestRealCLI -v
```

About half a minute: gcloud takes a second to start, and a create is some twenty commands. gcloud is sent to the stub with `CLOUDSDK_API_ENDPOINT_OVERRIDES_COMPUTE`, and gets its token from a file named by `CLOUDSDK_AUTH_ACCESS_TOKEN_FILE` in the test's environment; the token is on no command line. Its configuration directory (`CLOUDSDK_CONFIG`) is the test's own, so it does not see yours. The stub is a small in-memory Compute Engine API for one project: VMs, snapshots, firewall rules, addresses, operations, the aggregated lists and their pages.

A gcloud you already have will do: `KEELBOX_TEST_GCLOUD="$(command -v gcloud)"`. To get one without installing it, unpack Google's archive anywhere (the [install guide](https://cloud.google.com/sdk/docs/install) has the other platforms); it brings its own Python:

```sh
cd /tmp
curl -fsSLO https://dl.google.com/dl/cloudsdk/channels/rapid/downloads/google-cloud-cli-linux-x86_64.tar.gz
tar xzf google-cloud-cli-linux-x86_64.tar.gz
export KEELBOX_TEST_GCLOUD="$PWD/google-cloud-sdk/bin/gcloud"
```

What it checks:

- Every command of preflight, create, find, the firewall calls and destroy is accepted by gcloud, with `--format=json`, `--quiet` and `--project` after the command's own flags.
- `--metadata-from-file user-data=PATH` reaches the API as the file's content, the host key is in no argument, and the file is removed. `--no-service-account --no-scopes` sends no service account; `--labels`, `--tags`, `--metadata`, `--address` and the boot disk flags arrive as the structures keelbox means, with the image as the family `ubuntu-2404-lts-amd64` of `ubuntu-os-cloud`.
- The two firewall rules arrive with their priority, action, source range, target tag and description, the deny rule first; `firewall-rules update` changes the protocols and enables a disabled rule, and keeps the description and the tag.
- keelbox reads what gcloud prints for a VM (its address, labels, tags, metadata), a firewall rule, an address and its users, the zones and the machine types.
- gcloud follows the API's pages for the lists and prints one list, and its `--filter` finds a VM by label, by name or by network tag in any zone.
- Errors, which gcloud prints as text on standard error: a missing resource, a project the sign-in cannot see, the Compute Engine API off, a token Google refuses, no account, no project, and a flag gcloud does not know. A list one request failed for is an error for keelbox, where gcloud exits 0 with a warning (VMs, addresses) and where it exits 1 (firewall rules).
- `Settings` is the account, project and zone of gcloud's own configuration.
- `TestRealCLIResizeParkResume`: the calls of `keelbox resize`, `park` and `resume` as the real gcloud takes them and sends them on. Stop, `set-machine-type` and start, and the API's refusal for a VM that runs; a snapshot of the boot disk with the parked labels, read until it is `READY`, and one that fails deleted; the boot disk set to be deleted with its VM; the VM created from the snapshot.

The stub is not Google Cloud. Whether the API accepts a request, how gcloud words the errors of a real account, whether the image applies the cloud-config and the guest agent leaves the injected host key in place, and how soon a new VM is behind its rules are only tested by `make e2e-gcp`.

## make e2e-digitalocean

The same test against a new Droplet: `keelbox create` with the cheapest size that has 2 vCPU and 2 GB in the region, the default image, tagged `keelbox-e2e` through `target.digitalocean.tags`, deleted at the end with its firewall and tags. Before and after, a sweep deletes every Droplet tagged both `keelbox` and `keelbox-e2e`, then the parked snapshots of the harness's boxes, then the `keelbox-kb-e2e-*` firewalls that apply to no Droplet. It refuses to run with two such Droplets already present, and prints its estimated spend.

Needs doctl on `PATH`, signed in to a team you are happy to bill. `KEELBOX_E2E_DO_CONTEXT` names the doctl context and `KEELBOX_E2E_DO_REGION` the region (default `fra1`). Without doctl or a sign-in the test is skipped as pending. If a run is interrupted, check the control panel for a leftover `kb-e2e-*` Droplet, `keelbox-kb-e2e-*` firewall and a snapshot named `keelbox-parked-kb-e2e-*`; the next run's sweep removes them.

Everything the provider does is also tested without DigitalOcean: `internal/provider/digitalocean` and `internal/cli` run against `dotest`, an in-memory account behind doctl's arguments, JSON and exit codes. [`TestResizeParkResume`](#testresizeparkresume) runs here with `KEELBOX_E2E_TARGET=digitalocean`.

## make e2e-gcp

The same test against a new Compute Engine VM: `keelbox create` with `e2-small` (2 shared vCPUs, 2 GB) and a 20 GB disk, labelled `keelbox-e2e=true` through `target.gcp.labels`, deleted at the end with its firewall rules and its address. Before and after, a sweep deletes every VM labelled both `keelbox=true` and `keelbox-e2e=true`, then the parked snapshots labelled `keelbox-e2e=true`, then the `keelbox-kb-e2e-*` rules and addresses that no VM is behind and that carry keelbox's description, a reserved address left without a VM among them. It refuses to run with two such VMs already present. It prints no spend: gcloud has no price list.

Needs gcloud on `PATH`, signed in, with a project you are happy to bill that has the Compute Engine API on and a `default` network. `KEELBOX_E2E_GCP_PROJECT` names the project (default: gcloud's own) and `KEELBOX_E2E_GCP_ZONE` the zone (default `europe-west3-a`). Without gcloud or a sign-in the test is skipped as pending. If a run is interrupted, check the project for a leftover `kb-e2e-*` VM, `keelbox-kb-e2e-*` firewall rules, address and snapshot; the next run's sweep removes them.

Everything the provider does is also tested without Google Cloud: `internal/provider/gcp` and `internal/cli` run against `gcptest`, an in-memory project behind gcloud's arguments, JSON, error text and exit codes. [`TestResizeParkResume`](#testresizeparkresume) runs here with `KEELBOX_E2E_TARGET=gcp`; its count of what the cloud holds also fails when a parked box still has a disk or a reserved address, which Google Cloud bills.

## CI

- `ci.yml`, every push and pull request: `make check`, and the installer smoke test on Linux (macOS when installer or release code changes).
- `pull-request.yml`: the title is a Conventional Commit; area labels.
- `e2e.yml`, on demand: `make e2e` on a hosted runner against the newest Claude Code and Codex.
