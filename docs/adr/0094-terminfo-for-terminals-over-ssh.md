# 0094: Terminfo for Ghostty, cmux and kitty: kitty's package, and a compiled copy for Ghostty

Date: 2026-11-24. Status: accepted.

## Context
ssh hands the box the laptop's `TERM`. Ghostty, and cmux, which is built on it, send `xterm-ghostty`; kitty sends `xterm-kitty`. Ubuntu's ncurses-term has neither name: 24.04's has `kitty` but no `ghostty`, and 26.04's has both, under those names only. Without an entry on the box, tmux stops with "missing or unsuitable terminal", vim falls back to a bare terminal, and `tput` fails. `keelbox attach` already sets `xterm-256color` when the box lacks the entry; plain `ssh` and `keelbox ssh` had no such fallback.

The alternatives:
- **A package for each name.** `kitty-terminfo` (universe, about 100 KB, no dependencies) installs kitty's own `xterm-kitty`, and apt keeps it current. Ghostty's `xterm-ghostty` comes only with the `ghostty` package on 26.04, which is the whole terminal: 30 MB and GTK.
- **Vendoring the terminals' terminfo source.** kitty's is GPL-3, which keelbox should not embed, and a vendored entry goes stale unless keelbox tracks each terminal's releases.
- **Rewriting `TERM` at login,** as attach does. It would apply to every login rather than the terminals that need it, and it hides the real terminal from programs that read `TERM`.
- **Compiling an alias** of the release's own entry under the name the terminal sends.

## Decision
- **xterm-kitty comes from `kitty-terminfo`**, one of the base packages.
- **xterm-ghostty is compiled with `tic -x` into `/etc/terminfo`**, as a copy of the release's `ghostty`, or of `xterm-256color` where the release has none (24.04). ncurses searches `/etc/terminfo` before the packages' directories, and no package installs entries there.
- **apply keeps the copy current.** tic copies the base's capabilities when it compiles, so Check compares the copy with the release's entry and compiles it again after an upgrade or an ncurses update. It sets the entry to 0644 and its directory to 0755, whatever root's umask was.
- **A package's entry wins.** Once a package installs `xterm-ghostty`, apply removes keelbox's copy, which would otherwise hide it. Each copy's description is `keelbox alias of <entry>`, so an entry of that name keelbox did not compile is left alone.

## Consequences
- Over `ssh` from Ghostty, cmux or kitty, tmux, vim, less and line editing work on 24.04 and 26.04. On 24.04 Ghostty's extras, such as styled underlines, go unused until the release has a `ghostty` entry.
- A box gains one package and at most one file. Each `plan` runs a few `infocmp` calls.
- The attach fallback stays, for a box keelbox has not yet applied and for terminals neither entry covers.
