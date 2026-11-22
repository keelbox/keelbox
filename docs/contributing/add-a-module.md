# Add a module

A worked example: a `direnv` module that installs direnv and hooks it into the agent's shell, off unless `runtime.direnv: true`. The code below compiles and passes `make check`; it was verified on a scratch branch and is not part of keelbox.

Before writing a module, check it is a module at all. A new tool that belongs with an existing one is a step in that module (the GitHub CLI is three steps in `runtime`). A new module is for a separate concern with its own toggle and its own doctor checks. Open an issue first if it adds a default to every box.

Read [the architecture tour](architecture.md) for the engine and the Executor seam.

## 1. The config toggle

Add the key to [`internal/config/config.go`](../../internal/config/config.go), as a tri-state `*bool` so "unset" means "the default", with a helper that applies the default:

```go
type Runtime struct {
	Docker *bool `yaml:"docker,omitempty"`
	Node   *bool `yaml:"node,omitempty"`
	GH     *bool `yaml:"gh,omitempty"` // GitHub CLI, for the agent's own GitHub sign-in
	Direnv *bool `yaml:"direnv,omitempty"`
}

func (c *Config) Direnv() bool { return On(c.Runtime.Direnv, false) }
```

Modules call the helper, never the field. If the key has rules, add them to `Validate` with the key path in the message. Keep the schema additive: a change that breaks existing files needs a `version` bump and an ADR.

Document the key in [`docs/reference/config.md`](../reference/config.md). `TestConfigReferenceListsEveryKey` fails until you do:

```text
--- FAIL: TestConfigReferenceListsEveryKey
    docs_test.go:67: docs/reference/config.md has no row for: runtime.direnv
```

```markdown
| `runtime.direnv` | bool | `false` | direnv, hooked into the agent's interactive shell. |
```

Add it, commented, to [`internal/config/example.yaml.tmpl`](../../internal/config/example.yaml.tmpl) (a Go template: keep the comment column and run `go test ./internal/config`) if users should discover it there.

## 2. The module

`internal/modules/direnv.go`:

```go
package modules

import (
	"context"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/engine"
)

// Direnv installs direnv and hooks it into the agent's interactive shell,
// so a project's .envrc loads when the agent enters its directory.
type Direnv struct{}

func (Direnv) Name() string                  { return "direnv" }
func (Direnv) Enabled(c *config.Config) bool { return c.Direnv() }

// DirenvHook is the .bashrc block. It goes after Ubuntu's interactive
// guard (Top: false): the hook only matters in interactive shells.
const DirenvHook = `eval "$(direnv hook bash)"`

func (Direnv) Steps(e *engine.Env) []engine.Step {
	u := e.AgentUser()
	return []engine.Step{
		engine.PackagesStep("direnv-package", func(*engine.Env) []string { return []string{"direnv"} }),
		engine.BlockStep("direnv-hook", engine.Block{
			Path: u.Home + "/.bashrc", ID: "direnv", Content: DirenvHook, Mode: 0o644, Owner: u.Name,
		}),
	}
}

func (Direnv) Verify(ctx context.Context, e *engine.Env) []engine.Finding {
	v, err := versionOf(ctx, e, "direnv", "version")
	if err != nil {
		return []engine.Finding{fail("direnv", "direnv", "direnv does not run for "+e.Cfg.Users.Agent.Name)}
	}
	return []engine.Finding{ok("direnv", "direnv", "direnv "+v)}
}
```

Rules that apply to every step:

- **Check has no side effects.** It runs in `plan` and `doctor`. Reading files and running read-only commands is fine; installing, writing or restarting is not.
- **Apply must make Check pass.** The engine checks again after Apply and fails the run otherwise.
- **Use the helpers.** `FileStep` for files keelbox owns (start the content with a `Managed by keelbox` comment), `BlockStep` for files the user also edits (never rewrite those), `PackagesStep`, `ServiceStep`, `aptKeyStep` for a new apt repository (the fingerprint goes in the manifest).
- **All machine access through the Executor.** No `os/exec`, no `os.ReadFile`. Run commands as the agent with `system.As(user, ...)`. Files a user owns go through `e.FS(user)`, never `e.X`: the step helpers do this from `Owner`, and root's own access refuses home paths ([architecture](architecture.md#files-are-touched-as-their-owner)).
- **No secrets in commands.** Arguments and environment show up in `--verbose` and errors.
- **Versions go in the manifest**, never in module code ([release manifest](../reference/release.md#release-manifest)). Here, `direnv` comes from Ubuntu's archive and is not pinned; a tool from upstream needs a manifest field and a checksum.
- **Steps is cheap.** It runs at the start of every command and has no context. Do work in Check and Apply.
- **Set `User`** on a step that runs as an account or touches its home, if it is not built from `FileStep`, `DirStep`, `LineStep` or `BlockStep` (which set it from `Owner`). Its failure then skips only that account's steps.
- **`Soft: true`** only for a step nothing later depends on and whose failure should not stop the box, like an agent CLI's installer.

## 3. Register it

[`internal/modules/util.go`](../../internal/modules/util.go):

```go
func All() []engine.Module {
	return []engine.Module{Host{}, Access{}, Runtime{}, Sessions{}, Primer{}, Logbook{}, Direnv{}, Agents{}}
}
```

Order matters: a module may depend on everything before it. Keep `host` and `access` first, so a box stays reachable if a later module fails, and `agents` last.

## 4. Teach the simulated box

The sim answers commands it knows and fails the rest. `PackagesStep` already works (the sim's apt installs anything), but `direnv version` from `Verify` needs an answer. In `sim.run` in [`sim_test.go`](../../internal/modules/sim_test.go):

```go
	case "direnv":
		if _, ok := s.pkgs["direnv"]; !ok {
			return failed(c)
		}
		return res("2.37.1\n")
```

## 5. Test it

`internal/modules/direnv_test.go`:

```go
package modules

import (
	"context"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/engine"
)

func TestDirenvConvergesAndHooksTheShell(t *testing.T) {
	s := newSim(t)
	e := simEnv(t, s, testConfig(t, "runtime:\n  direnv: true\n"))
	ctx := context.Background()
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatal(err)
	}
	second, err := engine.Apply(ctx, e, All())
	if err != nil || len(second.Changes) != 0 {
		t.Fatalf("second apply: %v %v", second.Changes, err)
	}
	rc, _ := s.ReadFile("/home/agent/.bashrc")
	if !strings.Contains(string(rc), "# >>> keelbox direnv >>>\n"+DirenvHook+"\n") {
		t.Errorf(".bashrc has no direnv block:\n%s", rc)
	}
	for _, f := range engine.Doctor(ctx, e, All()) {
		if f.Module == "direnv" && f.Level != engine.LevelOK {
			t.Errorf("doctor: %+v", f)
		}
	}
}

func TestDirenvIsOffByDefault(t *testing.T) {
	e := simEnv(t, newSim(t), testConfig(t, ""))
	if (Direnv{}).Enabled(e.Cfg) {
		t.Error("direnv enabled without runtime.direnv")
	}
}
```

Then `make check`. `TestApplyConvergesAndIsIdempotent` and `TestFreshBoxPlansChangesInEveryModule` run every enabled module; a default-on module must converge there too.

## 6. On a real machine

`make e2e` exercises the whole box on a fresh LXD VM. For a quick manual check, apply your build to a VM of your own, following [config by hand](../guides/automation.md#config-by-hand):

```sh
make build                             # dist/keelbox, dist/keelbox-linux-{amd64,arm64}
lxc launch ubuntu:24.04 kb-dev --vm -c limits.cpu=2 -c limits.memory=2GiB \
  --config=cloud-init.user-data="$(printf '#cloud-config\nssh_authorized_keys:\n  - %s\n' "$(cat ~/.ssh/id_ed25519.pub)")"
dist/keelbox apply vm.yaml --ssh-user ubuntu   # vm.yaml: provider ssh, host: the VM's address
dist/keelbox plan vm.yaml                      # after a change: your steps show as +
dist/keelbox doctor vm.yaml
lxc delete --force kb-dev
```

These commands need a Linux host: `lxc` runs there, and `dist/keelbox` is the Linux build for the host's CPU.

A module that touches Docker, sshd, the tmux environment or the agent's network must add a check to [`test/e2e/features_test.go`](../../test/e2e/features_test.go).

## 7. Docs in the same change

- [`docs/reference/config.md`](../reference/config.md): the key (enforced by the test).
- [`docs/reference/box-files.md`](../reference/box-files.md): every file, package and service the module manages.
- [`docs/concepts/how-it-works.md`](../concepts/how-it-works.md): the modules table.
- The primer ([`primer.go`](../../internal/modules/primer.go)) if agents should know about it.
- [`docs/security-model.md`](../security-model.md) if it changes what the box enforces.
- An ADR in `docs/adr/` if you chose between real alternatives.

Commit as `feat(modules): add direnv for the agent` ([CONTRIBUTING.md](../../CONTRIBUTING.md#commits-and-pull-requests)).
