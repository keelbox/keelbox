# 0066: Use the hcloud CLI's token without copying it

Date: 2026-11-12. Status: accepted. Adds a token source to [ADR 0026](0026-credential-store.md).

## Context
Hetzner has no delegated sign-in (ADR 0027), so a first Hetzner box means creating a project token in the console and pasting it. People who already use Hetzner's own CLI, `hcloud`, have such a token in one of its contexts. Asking them to make a second one is a step that adds a credential instead of removing one. Copying the first into keelbox's store would leave two copies to rotate.

Facts the design relies on, read 2026-11-12 in `hetznercloud/cli` at v1.70.2:
- The config is `~/.config/hcloud/cli.toml` (or `HCLOUD_CONFIG`), written mode 600, TOML with `active_context` and a `[[contexts]]` list of `name` and `token`.
- There is a supported way to read a token: `hcloud config get token --allow-sensitive` prints the effective token on standard output (since v1.44.0, 2024-06-20). Without the flag it refuses: "'token' is sensitive".
- Precedence is flags, then environment, then the context, then the global config. `HCLOUD_TOKEN` beats the context's token, and `HCLOUD_CONTEXT` beats `active_context`. A context that does not exist is a warning on standard error, and hcloud carries on with none.
- `hcloud context active` prints the active context's name, or nothing, and exits 0.

## Decision
- `target.hetzner.hcloud_context: NAME` names an hcloud context. When set, keelbox gets the box's token by running `hcloud config get token --allow-sensitive` with `HCLOUD_CONTEXT=NAME` and `HCLOUD_TOKEN` removed from the child's environment, each time a command needs the API. It reads the token from standard output, holds it in memory for that command, and stores it nowhere: not in the credential store, a file or the config, which holds only the context's name.
- keelbox does not parse `cli.toml`. The command is the supported interface, it follows hcloud's own precedence and file location, and keelbox needs no TOML parser for it.
- The order keelbox looks in is: `HCLOUD_TOKEN`, `target.hetzner.token_file`, `target.hetzner.hcloud_context`, the credential store, `~/.config/keelbox/hetzner.token`. `hcloud_context` and `token_file` together are a config error. With `hcloud_context` set, a failure is an error with its fix; keelbox does not fall through to another source.
- `keelbox up`, when no token is stored and it can ask, offers the active context first: "Use the token of the hcloud context ...?". Yes writes `hcloud_context`; no, or a failure to read it, goes on to the token walk-through of ADR 0026. `--hcloud-context NAME` is the same choice without a terminal. A token already in keelbox's store is used without the question.
- The token is never in an argument, the environment of a child, output or an error. Errors carry hcloud's last standard error line (never its standard output), dropped if it were to contain the token. A context name must be letters, digits, `.`, `_` and `-`.
- `keelbox forget-token BOX` says that there is no copy to remove and names `hcloud context delete`.

## Consequences
- The box depends on the hcloud CLI staying installed and the context keeping its name. When either goes, the next command says so and how to move to a token of keelbox's own.
- hcloud keeps its token in a plain mode 600 file, weaker at rest than the OS credential store keelbox uses for its own copy (ADR 0026). keelbox does not change where hcloud keeps it; `docs/guides/credentials.md` says so.
- The token reaches every server in its project, as any Hetzner token does. A context for a project used only for keelbox keeps other servers out of reach.
- hcloud older than 1.44.0 has no `config get`; the error names the version.
