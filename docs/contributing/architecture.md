# Architecture tour

A map of the code for someone about to change it. Read [how keelbox works](../concepts/how-it-works.md) first for the user-level picture, and [CONTRIBUTING.md](../../CONTRIBUTING.md) for the rules.

## Packages

```text
cmd/keelbox            main: calls cli.Main
internal/cli           commands, flags, output; local and ssh dispatch, and the cloud interface for hetzner, aws, digitalocean and gcp
internal/config        the YAML schema, defaults, validation, example.yaml.tmpl
internal/manifest      stable.yaml, the release manifest embedded in the binary
internal/engine        Env, Step, Module; Plan, Apply, Doctor, Impact; step helpers
internal/inventory     read-only look at a machine in use: listeners, UFW, system Docker, accounts, sshd
internal/modules       host, access, runtime, sessions, primer, logbook, previews, backup, notify, doctor, watch, permissions, agent-policy, agents
internal/system        the Executor seam: Real (the machine), Fake (tests), UserFS
internal/logbook       the box's JSONL record of applies and day-two actions
internal/events        event kinds, the root event queue, the agent's events file
internal/installs      the agent's package requests: the checks, the deny-list, root's record of them
internal/snapshot      the status snapshot the watch run writes
internal/watch         the watch run
internal/projects      the agent's projects and their git state, read as the agent
internal/runlock       the apply lock and the day-two lock
internal/runs          a detached apply's run log on the box: records, summary, the scripts that follow it
internal/provider      ssh transport, host-key pinning; hetzner/ API client; aws/, digitalocean/ and gcp/ through their CLIs
internal/release       channel index, release keys, the one verified download path
internal/selfupdate    keelbox self-update
internal/agentlimits   what the agent account uses, against its limits
internal/backup        restic against S3-compatible storage
internal/credstore     provider tokens in the OS credential store (Keychain, Secret Service)
internal/doctorrun     scheduled doctor, what keelbox-doctor.timer starts
internal/notify        notification channels and their senders
internal/plain         text from outside keelbox, sanitised for printing
internal/previews      the agent's loopback dev servers at HTTPS URLs on the tailnet
internal/procnet       the kernel's TCP socket tables
internal/reports       the box's inbox of keelbox reports
internal/services      the home-server services keelbox knows by name
internal/docscheck     the tests that keep docs/ to its rules
internal/testbuild     switches for a keelbox built for its own tests
internal/ui            terminal style: palette, marks, cards, banner, the apply progress view
internal/prompt        questions: line, yes or no, menu, secret; drawn on a capable terminal
internal/term          termios and window size, without x/term
internal/version       version, commit, date (set by the build)
internal/tools         releasesign signs SHA256SUMS in the release workflow; indexcheck checks a stable.json
test/e2e               real-machine acceptance test, build tag e2e
get/                   get.keelbox.dev: install.sh, vercel.json, security.json (the security notices)
```

One dependency: `go.yaml.in/yaml/v3`. No CLI framework (stdlib `flag`), no SSH library (system `ssh` and `scp`).

## A command, end to end

`keelbox apply acme.yaml` with `target.provider: ssh`:

1. `cli.Main` parses flags (`parse` accepts them anywhere after the command), loads and validates the config (`config.Load`).
2. Provider `ssh`: `runSSH` builds a `provider.SSH` from `target.ssh` and calls `Run`.
3. `SSH.Run` pins the host key if needed (`TrustHost`), opens one multiplexed ssh connection, reads the box's architecture, picks the linux binary to upload (`binaryFor`), resolves the config for the box (`ShipConfig`: key files inlined, target `local`, auth key re-pointed at the stage), uploads both with `scp`, and runs `RemoteScript`: `sudo -n /var/lib/keelbox/stage/keelbox apply --local /run/keelbox/keelbox.yaml`.
4. On the box, the same `cli.Main` sees `--local` and calls `runLocal`: root check, `NewEnv` (gather facts, check the platform against the manifest), take the apply lock, run the downgrade guard, then `cli.Apply`.
5. `cli.Apply` calls `engine.Apply` with `modules.All()`, appends the logbook record whatever the outcome, and prints the summary. Its exit code travels back through ssh.

`plan` and `doctor` follow the same path and call `engine.Plan` and `engine.Doctor`.

`apply` over SSH is detached ([ADR 0063](../adr/0063-detached-apply.md)). Before step 3, `applyDetached` ([`internal/cli/follow.go`](../../internal/cli/follow.go)) asks the box for its newest run and follows a running one or reports a finished one (`rejoin`). In step 4 `RemoteScript` sets `KEELBOX_DETACH=1`, and `runLocal` calls `startRun` ([`internal/cli/detach.go`](../../internal/cli/detach.go)) instead of applying: it starts `systemd-run --unit keelbox-apply-<id>` with `KEELBOX_RUN=<id>`, prints the id and returns. In that unit `runLocal` calls `detachedRun`, which tees apply's output into the run log (`runs.Log` turns apply's lines into records) and does steps 4 and 5 as above. The laptop's `follower` tails the log over its own ssh (`SSH.Follow`) and renders each record back into apply's line (`Record.Line`) for the progress view, so the view knows nothing of where its lines come from.

## The engine

[`internal/engine/engine.go`](../../internal/engine/engine.go):

```go
type Module interface {
	Name() string
	Enabled(c *config.Config) bool
	Steps(e *Env) []Step
	Verify(ctx context.Context, e *Env) []Finding
}

type Step struct {
	Name  string
	Check func(ctx context.Context, e *Env) (Status, error) // no side effects
	Apply func(ctx context.Context, e *Env) error           // must make Check pass
	Soft  bool                                              // failure does not stop the run
	User  string                                            // non-root account it acts for
}
```

- `Env` carries the Executor (`X`), the config with defaults applied, the manifest (`M`), facts about the machine (`Facts`: Ubuntu version, codename, architecture, the `SUDO_USER`), the progress writer, the running binary (`Self`) and `--adopt`.
- `engine.Apply` runs Check, then Apply when pending, then Check again. A step that errors or does not converge stops the run, unless it is `Soft` or has a `User`: then the failure is recorded, that account's later steps are skipped, and the rest of the box applies (exit 1, logbook `partial`; [ADR 0017](../adr/0017-account-scoped-failures.md)). An Apply may return `engine.Defer(...)` to stay pending without failing.
- `engine.Plan` runs every Check. `engine.Impact` runs Plan and asks each module that implements the optional `Impacter` what its pending steps would do to the `inventory` of a machine in use; `plan --impact` prints it, and the first apply stops on the items marked `Gate` ([ADR 0038](../adr/0038-adoption-impact.md)). A step that can cut off, replace or restart something on such a machine adds its item there. `engine.Doctor` reports pending Checks as `warn` drift, then appends each module's `Verify` findings.
- `Steps` is called when the run starts. It has no context and must not do I/O beyond reading cheap facts; do the work inside Check and Apply.

Step helpers in [`internal/engine/steps.go`](../../internal/engine/steps.go) cover most needs:

| Helper | Converges |
|---|---|
| `FileStep(name, File{...})` | A file's exact content, mode and owner, with optional `Validate` (rolled back on failure) and `OnChange`. |
| `BlockStep(name, Block{...})` | A marked block inside a file the user also edits. |
| `LineStep` | One exact line in a file. |
| `DirStep` | A directory's mode and owner. |
| `PackagesStep` | apt packages are installed (runs `apt-get update` when sources changed). |
| `ServiceStep` | A systemd unit is enabled and active. |
| `CommandStep` | A check command plus an apply command. |

[`internal/modules/util.go`](../../internal/modules/util.go) adds `aptKeyStep` (download a repository key and refuse it unless its fingerprint matches the manifest), `aptSourceStep`, `curl`, `tempDir` and the finding helpers `ok`, `warn`, `fail`. `usersvc.go` has user-unit helpers (`userUnitFile`, `userUnitStep`, `userctl`).

## The Executor seam

Modules never call `os/exec` or touch the filesystem. Everything goes through [`system.Executor`](../../internal/system/system.go):

```go
type Executor interface {
	Run(ctx context.Context, c Cmd) (Result, error)
	FS                       // root's file access
	UserFS(user string) FS   // file access by a process running as user
	LookupUser(name string) (User, error)
}

type FS interface {
	ReadFile(path string) ([]byte, error)
	Stat(path string) (FileInfo, error)  // a final symlink is reported, not followed
	WriteFile(path string, data []byte, mode fs.FileMode, owner, group string) error
	MkdirAll(path string, mode fs.FileMode, owner, group string) error
	Symlink(target, path string) error
	Remove(path string) error
}
```

`system.Real` is the machine. `system.Fake` is in memory: commands are answered by handlers matched on their rendered string; files, symlinks, owners and permissions live in maps. `system.C("apt-get", ...)` runs as root; `system.As("agent", "mise", ...)` runs as a user with a login-like environment.

### Files are touched as their owner

apply runs as root, but a path another account can change (anything in the agent's or admin's home) is read and written by a process running as that account ([ADR 0015](../adr/0015-file-access-as-the-owner.md)). Then a symlink the agent plants gives it nothing it could not already do.

- `Env.FS(owner)` picks the access: root's own for `""` or `root`, `UserFS(owner)` otherwise. `FileStep`, `BlockStep`, `LineStep` and `DirStep` use it with the file's `Owner`, so a step built from them is covered.
- A step that reads or writes a home file by hand must call `e.FS(user)`, never `e.X.ReadFile` or `e.X.WriteFile`.
- Root's access (`e.X` directly) runs `CheckRootPath` first and refuses a path unless every directory on the way is owned by root and writable by no one else. A home path sent through it fails closed, with an error naming the path.
- `UserFS` reads are capped at 16 MiB and refuse anything but a regular file.

`TestAgentSymlinksGainNothing` replays the symlink attacks against the sim; `test/e2e` replays them on a real box.

A `Cmd`'s arguments and environment appear in `--verbose` output and in errors, so they must never hold a secret. Pass secrets by file path.

## The simulated box

[`internal/modules/sim_test.go`](../../internal/modules/sim_test.go) wraps `system.Fake` into a stateful fake Ubuntu: apt installs packages, `useradd` creates users and shadow entries, `systemctl enable` makes units active, `ufw` keeps rules, `mise` and `git` remember what they installed. It answers just enough for every module to converge. `newSim(t)`, `testConfig(t, extraYAML)` and `simEnv(t, sim, cfg)` set up a test.

`TestApplyConvergesAndIsIdempotent` applies every module to a fresh sim, checks nothing is still pending, applies again and requires zero changes, then requires doctor to find no failure. A step whose Check and Apply disagree fails it. When you add a command a step runs, teach the sim to answer it.

## Day-two runs

Between applies keelbox runs short root commands from systemd units, never a daemon ([ADR 0030](../adr/0030-day-two-runs.md)). [`internal/modules/rootunit.go`](../../internal/modules/rootunit.go) renders every such unit: `rootService` (with `DayTwo` set: an `ExecCondition` on `keelbox has-command`, no start limit, CPU and I/O weight, a time limit), `rootTimer` and `rootPath`, and `serviceStep`, `timerSteps` and `pathSteps` install them. A day-two command is an internal command (`internalCommands` in `internal/cli/daytwo.go`, not in help) dispatched from `runLocal` to `dayTwo`, which takes the locks in `internal/runlock` and skips while an apply runs.

- `keelbox-watch.timer` runs `watch-run` (`internal/watch`) every two minutes: heartbeat, status snapshot (`internal/snapshot`), and `events.Dispatch` of the queue. Checks added there must keep the run to a few seconds.
- `keelbox-events.path` runs `events-sync` when the agent's events file changes: `events.SyncAgent` reads it as the agent and queues what passes ([ADR 0032](../adr/0032-agent-events.md)).
- `keelbox-install.path` runs `install-run` when the agent's package request file changes: `installs.Run` reads it as the agent, checks it (`CheckNames`, the deny-list in `names.go`, `plan` against apt) and installs it or keeps it for `keelbox approve` ([ADR 0074](../adr/0074-agent-permissions.md)). It is the one day-two run that adds software, and the only place apt-get runs for a request; root's record of the requests is changed under `runlock.InstallsPath`, which is not held while apt-get runs.
- Root sources raise events with `events.Emit`; a new kind is a row in `internal/events/events.go`. Channels implement `events.Sender`.
- An action a run takes is a logbook record with a `kind`, written with `logbook.Action` ([ADR 0031](../adr/0031-logbook-kinds.md)).

## Agent CLI policy

A feature that needs a setting the agent cannot change adds one `policySource` to `policySources` in [`internal/modules/agentpolicy.go`](../../internal/modules/agentpolicy.go) and calls the `AgentPolicy` builder: `AddEnv` and `SetClaude` for Claude Code's managed settings, `SetCodex` for Codex requirements (booleans), `AddHook` for either CLI's managed hooks. The `agent-policy` module alone renders and writes the files ([ADR 0033](../adr/0033-agent-policy-files.md)); never write them from a feature's own steps.

## Providers

- **local** is `cli.runLocal`.
- **ssh** is [`internal/provider/ssh.go`](../../internal/provider/ssh.go) (transport, upload, `RemoteScript`, `Interactive`) and [`hostkey.go`](../../internal/provider/hostkey.go) (`TrustHost`, `Pin`, `RotateHostKeys`, `GenerateHostKey`, `CopyPins`).
- **hetzner** is [`internal/provider/hetzner/`](../../internal/provider/hetzner/) (a small API client, `Create`, `Find`, `Destroy`), wired in as `hetznerCloud`, an implementation of the `cloud` interface in [`internal/cli/cloud.go`](../../internal/cli/cloud.go). `cloudCreate`, `cloudDestroy` and `runCloud` there are written once for every cloud, as are `cloudResize` ([`resize.go`](../../internal/cli/resize.go)) and `cloudPark` and `cloudResume` ([`park.go`](../../internal/cli/park.go)), which a provider serves with the calls in its own `<provider>park.go` ([ADR 0071](../adr/0071-resize-park-resume.md)); the labels and the cloud-init `UserData` they share are in `internal/provider/cloud.go`.
- **aws** is [`internal/provider/aws/`](../../internal/provider/aws/): Lightsail through the user's installed AWS CLI (`Client.Run` is the seam; `awstest` is the fake), wired in as `awsCloud`. keelbox holds no AWS credential ([ADR 0065](../adr/0065-aws-through-the-aws-cli.md)).
- **digitalocean** is [`internal/provider/digitalocean/`](../../internal/provider/digitalocean/): a Droplet behind a Cloud Firewall, through the user's installed doctl (the same `Runner` seam; `dotest` is the fake), wired in as `doCloud`. keelbox holds no DigitalOcean credential ([ADR 0067](../adr/0067-digitalocean-through-doctl.md)).
- **gcp** is [`internal/provider/gcp/`](../../internal/provider/gcp/): a Compute Engine VM on a reserved address, behind two VPC firewall rules on the box's network tag, through the user's installed gcloud (the same `Runner` seam; `gcptest` is the fake), wired in as `gcpCloud`. keelbox holds no Google credential ([ADR 0068](../adr/0068-google-cloud-through-gcloud.md)).

Clouds implement the `cloud` interface; local and ssh are cases in `cli.Main` and `cli.boxShell`. See [add a provider](add-a-provider.md).

## Interactive commands

`login`, `attach`, `sessions` and `ssh` ([`internal/cli/interactive.go`](../../internal/cli/interactive.go)) log in as the admin or agent directly. `SSH.Interactive` hands the process's own stdin, stdout and stderr to `ssh -t`, so keelbox never reads what passes through. Status checks (`shell.succeeds`, and `shell.exitCode` for a check with more than two answers) discard output and use exit codes only. Keep it that way: no code may read, parse or log a vendor's sign-in output.

## Release and distribution

`.goreleaser.yaml` builds the binaries. `.github/workflows/release.yml` signs `SHA256SUMS` with the Ed25519 release key through `internal/tools/releasesign` (`SHA256SUMS.sig` and `SHA256SUMS.sshsig`), and keylessly with cosign. `scripts/publish.sh` verifies both, merges the channel index's checksum history, and promotes the release to `keelbox/releases` and `get.keelbox.dev`.

Every download of keelbox's own binary, by `self-update` or for the box, goes through `release.Fetcher.Fetch`: the release key signature on `SHA256SUMS`, the binary's checksum in `SHA256SUMS`, the same checksum in the index, and a release host from the allowlist. `get/install.sh` does the same steps in shell. The public keys are in `internal/release/keys/` and in `install.sh`; a test keeps the two lists equal. [`docs/maintaining.md`](../maintaining.md) has the runbook, [ADR 0013](../adr/0013-distribution.md) the distribution design and [ADR 0016](../adr/0016-release-signatures.md) the verification.
