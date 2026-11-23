# keelbox documentation

keelbox sets up an Ubuntu machine as a box for AI coding agents. New to it? Follow the first page under Start: it takes you from installing keelbox to an agent at work. Come back to the other sections as you need them. Output shown on these pages is an example: names, prices and times will be yours.

## Start

- [Your first box](getting-started.md): install, `keelbox up`, a running agent.
- [Where a box can run](guides/where-to-run.md): the four clouds, a server you have, a machine at home, and what each costs.
- [Install keelbox](guides/installing.md): the installer, Homebrew, checking the download, removing keelbox.

## Other ways to set up

Read these if your box is not a new cloud server, or if you set up boxes from scripts.

- [A server you already have](guides/adopting-a-box.md): over SSH, or one you create with a cloud-config.
- [A machine already in use, at home or this one](guides/machine-in-use.md): what changes on it, `plan --impact`, `--adopt`.
- [Cloud sign-ins and permissions](guides/credentials.md): what to install, the sign-in command, what it can reach.
- [Without the wizard: flags, CI, config by hand](guides/automation.md): `--yes`, exit codes, `init`, `plan`, `apply`.

## Use it

Day-to-day work on a box that is set up.

- [Sign in](guides/signing-in.md): `keelbox login` for Tailscale, Claude Code, Codex and GitHub.
- [Attach, leave and come back](guides/sessions.md): `keelbox attach`, sessions, a phone.
- [The Claude app](guides/remote-control.md): start sessions on the box with Remote Control.
- [Work on the box: git, packages, root](guides/work-on-the-box.md): what the agent may change, `keelbox install`, `approve`.
- [Preview a dev server](guides/previews.md): `keelbox expose`.
- [Your computer's agents and your boxes](guides/agents-here.md): `agent-skill`, `ssh -- CMD`, `ssh-config`, `skills`.

## Look after it

- [Check the box and fix problems](guides/troubleshooting.md): `status`, `doctor`, `ls`, and problems by symptom.
- [Update keelbox and the agent CLIs](guides/upgrades.md): `update`, `self-update`, `apply`, `rollback`.
- [Back up and restore](guides/backups.md): `keelbox backup`, `keelbox restore`.
- [Notifications](guides/notifications.md): alerts from the box, `keelbox notify test`.
- [Reports](guides/reports.md): problems the agent writes up for you to send.
- [Change size or pause billing: resize, park and resume](guides/resize-park-resume.md)
- [Closing public SSH, and getting back in when locked out](guides/public-ssh.md)
- [Remove a box](guides/removing-a-box.md): `destroy`, `forget`, undoing keelbox on a machine you keep.

## Understand

- [How keelbox works](concepts/how-it-works.md): one config, plan, apply, doctor.
- [Security model: what keelbox enforces and what it does not stop](security-model.md): read it before you give an agent credentials.

## Reference

Every command, config key and file, for when you know what you are looking for.

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
