# 0090: The box reboots itself for an update, at night once idle, and resumes the agents' conversations

Date: 2026-11-20. Status: accepted. Amends ADR 0005 (the reboot default).

## Context
unattended-upgrades installs security updates daily, and some of them, a new kernel among them, take effect only at a reboot. With `host.auto_reboot: never`, ADR 0005's default, a box waits for its owner. The fix for a waiting reboot, `keelbox ssh BOX --admin, then sudo reboot`, is root on the box, which keelbox keeps from agents: an agent on the owner's computer, asked to look after a box, can only say that the reboot is the owner's to run.

The other setting, a time `HH:MM`, has unattended-upgrades reboot at that time with users logged in or not. On a box that runs agents for hours, that can stop one part-way through a task. A reboot also ends every program in the agent's tmux sessions: an agent CLI left waiting for an answer is gone, and its owner has to find the conversation again.

## Decision
- **`keelbox up` asks when the box restarts for an update**: nightly (preselected), weekly or manually, written as `host.auto_reboot: nightly`, `weekly` or `never`. `--auto-reboot` answers without a terminal; the default is `nightly`, which `keelbox init` writes too.
- **Nightly**: the watch run, every two minutes as root, reboots the box when `/var/run/reboot-required` exists, between 01:00 and 06:00 in the box's timezone, once the box has been idle for 30 minutes without a gap in the runs. unattended-upgrades does not reboot. It reboots once per 20 hours at most, so an update that still asks after a reboot does not reboot the box every run of that night.
- **Weekly**: the same, for an update that was waiting at 01:00 on Sunday, that night or the first idle night after it, once a week. An update that arrives later in the week waits for the next Sunday.
- **Idle is what the watch run can see**, all of it, at each run:
  - no `user` session in logind that is a desktop (`x11`, `wayland`, `mir`) or is not idle by logind's idle hint, which for a terminal is input within the last few minutes;
  - no output in the agent's tmux windows (`window_activity`) for 5 minutes;
  - no file under the agent's `~/.claude/projects` or `~/.codex/sessions` changed for 5 minutes;
  - no running Claude Code process whose `~/.claude/sessions/PID.json` gives the status `busy`, whether in a terminal or started by Remote Control;
  - the agent's slice under 10% of all CPUs since the last run;
  - no backup running.
  
  The watch run does not run during an apply.
- **Before the reboot** it raises `rebooting` (info), sends its queued events, notes each tmux pane where Claude Code or Codex runs (below), saves the agent's tmux sessions with tmux-resurrect, records the reboot in the logbook (`power`), and has systemd reboot five seconds later (`systemd-run --on-active=5 systemctl reboot`).
- **After the reboot the conversations open again.** The note, `/var/lib/keelbox/watch/resume.json` (root, 0600), holds each pane (`session:window.pane`), its directory, and the command to type: `claude --resume ID` with the session ID from `~/.claude/sessions/PID.json`, or `claude --continue` without one; `codex resume --last` for Codex, which keeps no such file. Flags that say how the agent works (`--model`, `--permission-mode`, `--dangerously-skip-permissions`, Codex's `-m`, `-s`, `-a`, `-p`, `-c`, `--search`, `--add-dir`) are kept; a prompt and other flags are not. A pane that runs a subcommand that is not a conversation (`claude remote-control`, `codex exec`) is left out. Once the boot ID has changed, each watch run types the command, as the agent with `tmux send-keys`, into each noted pane that tmux-resurrect has brought back at a shell, with a `cd` first if the pane came back elsewhere. It gives up 20 minutes after the note.
- **`keelbox reboot BOX`** does the same on request, as the admin over SSH, and waits until the box answers with a new boot ID. It asks first, and without a terminal needs `--yes`. It refuses while an apply runs. It joins the agent skill's list of commands to ask about, and `updates-pending`, doctor's `reboot-required` and `keelbox status` name it.
- **`never` and `HH:MM` keep their meaning.**

## Consequences
- A box keeps current without its owner, and no agent needs root on the box to reboot it.
- A Claude Code at work keeps the box from rebooting. One left waiting for an answer is stopped and comes back in the same conversation, in the same pane. Two Codex panes in one directory both open its latest conversation.
- `~/.claude/sessions/PID.json` is Claude Code's own file and not documented. If it changes, Claude Code's status no longer counts towards idle, and its panes resume with `claude --continue`, the latest conversation in the directory.
- Other programs in the tmux sessions stop. A long job that writes nothing to tmux or a conversation and uses little CPU, a slow download for one, looks idle and is stopped.
- A session that reaches the box without a terminal (VS Code's remote server, an SSH command) is never idle in logind, so while one is connected the box does not reboot. The weekly `updates-pending` says that a reboot still waits.
- A box whose timezone is UTC, the default on a cloud server, reboots in UTC's night.
- A desktop session logged in on the machine keeps it from rebooting at all; its owner reboots it.
