# 0079: `keelbox add` puts an agent CLI or a tool on a box that is set up

Date: 2026-11-16. Status: accepted.

## Context
What a box has is decided in `keelbox up` and written to its config. To have something more afterwards, such as Codex on a box set up with Claude Code alone, the user would have to find the box's config file, know the line to change, run `keelbox apply`, and then run `keelbox login`.

The config stays the record of what a box is: apply reads nothing else, and a box rebuilt from it must come out the same. So a command cannot install something behind the config's back.

## Decision
- **`keelbox add [box] [what]`** does the three steps: it sets the thing in the box's config, runs apply with that config, and then runs the thing's sign-in where it has one.
- **A fixed list.** It adds what keelbox already installs and looks after and a config can leave out: the agent CLIs (`claude-code`, `codex`) and the runtime tools (`gh`, `docker`, `node`). A name outside the list is refused, exit 2, with the list.
- **With nothing named it lists** what the box has of these and the command for each of the rest. It reaches no box.
- **The config is edited in place.** One value changes: a name joins the `agents` list, or a `runtime` switch becomes `true`. Comments and every other line stay. The edit is kept only when the file then loads and asks for the thing. A file in a form keelbox cannot edit that way is left as it was, and the line to set is printed, exit 1.
- **What the box has already** is said, with the sign-in's command, and nothing is applied.
- **An apply that fails** leaves the config asking for the thing: the next apply brings it, and the message gives that command.
- **Without a terminal** the sign-in is not run and its command is printed.
- **No `remove`.** apply installs what the config asks for and has no steps that take an agent CLI or a runtime tool off a box. A command that only edited the config would say something it did not do.

## Consequences
- `keelbox login BOX codex`, on a box without Codex, ends with `keelbox add BOX codex`.
- The list is in `internal/cli/add.go`.
