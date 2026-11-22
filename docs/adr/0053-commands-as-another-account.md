# 0053: Commands root runs, and commands it runs as another account

Date: 2026-11-07. Status: accepted. Amends ADR 0015 and ADR 0039.

## Context
Root's runs that started agent tools by bare name were open to a shim. `system.As(agent, "tmux", ...)` ran `runuser -u agent -- env -i PATH=<the agent's PATH> tmux ...`, so `env` resolved `tmux` after the switch, in the agent's PATH, where `~/.local/bin` comes first. A shim there ran as the agent (no capabilities, no root files) but inside root's service cgroup, outside `users.agent.limits`, and, under an apply started from a terminal, with that terminal as its controlling tty. Its output went into the status snapshot and from there to the owner's terminal (ADR 0056). Commands with no time limit and no `WaitDelay` let a child holding stdout open stall the watch run until its unit timeout, and stall `apply`, which has no unit timeout, indefinitely. A read script for the agent's files (`[ -f ] && head`) could be raced into opening a FIFO.

`runuser` and `env` themselves resolved in root's PATH, so no root process ran an agent binary.

## Decision
- **Root's commands resolve in a fixed PATH.** `Real.Run` replaces the environment's `PATH` with `/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin` and resolves the program there before starting it. A `Cmd` whose `Env` sets `PATH` is refused, in `Real` and in the fake, so a caller cannot reintroduce one.
- **Commands as an account resolve before the switch.** keelbox looks the program up itself, only in directories that root owns and that no group or other can write (the `CheckRootPath` rule), then runs `/usr/sbin/runuser -u NAME -- /usr/bin/env -i HOME=... USER=... PATH=<fixed account PATH> /abs/prog args`. The account's PATH inside is the same fixed list, used only by what the program itself starts. A program in the agent's home (tmux-resurrect's `save.sh`, a mise shim) is named by its absolute path on purpose: the agent can change it and it runs as the agent. `Env.AgentTool` names the agent CLI the agent's own PATH would find, for version and sign-in checks, which run as the agent.
- **In the account's slice, without a terminal.** With systemd booted and a UID of 1000 or more, the command runs under `/usr/bin/systemd-run --scope --slice=user-UID.slice`, so the agent's limits apply. It starts with `Setsid`: no controlling terminal, so it cannot open `/dev/tty` of an admin's `sudo keelbox apply`.
- **Bounded.** Every command has a timeout (30 minutes by default, less where the caller sets one: 20 seconds for status, a minute for `--version`), `WaitDelay` of 5 seconds after the process exits, and a cancel that kills the whole process group. A run that times out says so in its error.
- **Reads as the account by a helper, not a script.** The read re-executes keelbox (`/proc/self/exe __keelbox-read-as-account`) as the account. It opens with `O_NONBLOCK|O_NOCTTY`, and `O_NOFOLLOW` where a final link is refused, checks with `fstat` that it has a regular file, and reads at most the caller's bound. A FIFO, socket or device is refused without a blocking read. The helper runs with the same session, timeout and group kill.

## Consequences
- A shim on the agent's PATH does not run from root's runs; `tmux`, `systemctl` and the rest are root's binaries.
- The agent's commands are under its limits, so past its memory slow-down mark a look at its tmux server can take longer than the watch run's 20 seconds. The watchdog takes the server as gone only when tmux says there is no server; a look that times out changes nothing, so a running server is not restarted for it.
- A tool keelbox needs and that is installed only in a directory another account can write is not found; doctor reports it as missing rather than running it.
- The test `TestRootCommandIgnoresTheCallersPATH` puts a `true` shim first on the test's PATH and fails if it runs; `TestAccountCommandResolvesInRootDirectoriesOnly` and `TestLookPathRefusesDirectoriesAnotherAccountCanChange` cover the account side; the end-to-end test plants a `tmux` shim and a save script that leaves a child behind.
