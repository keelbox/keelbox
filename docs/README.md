# keelbox documentation

keelbox sets up an Ubuntu machine as a box for AI coding agents. The pages follow the order you meet things in. Output shown on them is an example: names, prices and times will be yours.

## Start

- [Your first box](getting-started.md): install, `keelbox up`, a running agent.
- [Where a box can run](guides/where-to-run.md): the four clouds, a server you have, a machine at home.
- [Install keelbox](guides/installing.md): the installer, Homebrew, checking the download, removing it.

## Other ways to set up

- [A server you already have](guides/adopting-a-box.md): over SSH, or one you create with a cloud-config.
- [A machine already in use, at home or this one](guides/machine-in-use.md): what changes on it, `plan --impact`, `--adopt`.
- [Cloud sign-ins and permissions](guides/credentials.md): what to install, the sign-in command, what it can reach.
- [Without the wizard: flags, CI, config by hand](guides/automation.md): `--yes`, exit codes, `init`, `plan`, `apply`.

## Use it

- [Sign in](guides/signing-in.md): `keelbox login` for Tailscale, Claude Code, Codex and GitHub.
- [Attach, leave and come back](guides/sessions.md): `keelbox attach`, sessions, a phone.
- [The Claude app](guides/remote-control.md): Remote Control.
- [Work on the box: git, packages, root](guides/work-on-the-box.md): what the agent may change, `keelbox install`, `approve`.
- [Preview a dev server](guides/previews.md): `keelbox expose`.
- [Your computer's agents and your boxes](guides/agents-here.md): `agent-skill`, `ssh -- CMD`, `ssh-config`, `skills`.

## Look after it

- [Check the box and fix problems](guides/troubleshooting.md): `status`, `doctor`, `ls`, and problems by symptom.
- [Update keelbox and the agent CLIs](guides/upgrades.md): `update`, `self-update`, `apply`, `rollback`, and the agent CLIs.
- [Back up and restore](guides/backups.md): `keelbox backup`, `keelbox restore`.
- [Notifications](guides/notifications.md): `keelbox notify test`.
- [Reports](guides/reports.md): problems the agent writes up for you to send.
- [Change size or pause billing: resize, park and resume](guides/resize-park-resume.md)
- [Closing public SSH, and getting back in when locked out](guides/public-ssh.md)
- [Remove a box](guides/removing-a-box.md): `destroy`, `forget`, undoing keelbox on a machine you keep.

## Understand

- [How keelbox works](concepts/how-it-works.md): one config, plan, apply, doctor.
- [Security model: what keelbox enforces and what it does not stop](security-model.md)

## Reference

- [Commands and flags](reference/cli.md)
- [Config file](reference/config.md)
- [Known services](reference/services.md)
- [Releases and the release manifest](reference/release.md)
- [Files keelbox manages on a box](reference/box-files.md)
- [The four clouds](reference/clouds.md)

## Contributing

- [CONTRIBUTING.md](../CONTRIBUTING.md): set-up, commit rules, AI-assisted contributions.
- [Architecture tour](contributing/architecture.md)
- [Add a module](contributing/add-a-module.md)
- [Add a provider](contributing/add-a-provider.md)
- [Testing](contributing/testing.md)
- [Maintaining](maintaining.md): release and triage runbooks.

## Project records

- [CONTEXT.md](../CONTEXT.md): purpose, glossary, scope.
- [Issues](https://github.com/keelbox/keelbox/issues): bugs and accepted requests.
- [Architecture decision records](adr/)
