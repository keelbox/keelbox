# 0058: Terminal styling without a TUI library

Date: 2026-11-09. Status: accepted. Amends ADR 0025 (prompts) and builds on ADR 0001 (dependencies) and ADR 0056 (plain text).

## Context
Unstyled, `keelbox up` reads as plain, dense text: a two-column menu whose descriptions wrap mid-word at some widths, a token instruction block, apply progress whose columns drift with long module names, and three prefixes (`keelbox up:`, `keelbox apply:`, `keelbox:`) for one voice. The bar is the first run of gh, flyctl, uv or a Charm-built CLI: a banner, steps, arrow-key menus, cards, a live progress view.

The obvious route is charmbracelet/lipgloss for layout and huh for forms. huh pulls in bubbletea, bubbles, lipgloss, x/ansi, x/term, x/cellbuf, termenv, go-colorful, reflow and golang.org/x/sys among about twenty modules, all in the binary that runs as root on every box. ADR 0001 keeps one dependency; ADR 0025 keeps prompts stdlib-only. What keelbox draws is small: a handful of question kinds, cards, one live region.

## Decision
- **No new dependency.** Styling is `internal/ui`, prompts `internal/prompt`, terminal modes `internal/term` (termios through `syscall` ioctls on Linux and macOS: echo off, key-at-a-time with signals kept, window size, the background colour query). golang.org/x/term is not added either: the three ioctls it would replace are already here and tested.
- **Palette.** keelbox's terminal palette: Night Chart on dark backgrounds, Paper Chart on light. Truecolor when `COLORTERM` says so, the nearest of the 256 xterm colours when `TERM` does, else the 16 the terminal's theme defines. Light or dark comes from `COLORFGBG`, else one OSC 11 query answered within 400 ms (skipped in 16 colours).
- **One style decision per stream** (`ui.Detect`): not a terminal or `TERM=dumb` is plain text, one line per event; `NO_COLOR` keeps layout and live redraws without colour; `ACCESSIBLE` (the Charm convention) keeps colour but drops art and redraws, and questions are asked one plain line at a time. Meaning never rests on colour: every mark is a glyph or a word too (✓/ok, ✗, !, ○).
- **Questions.** On a capable terminal a menu takes the arrow keys, a number or a choice's key, then Enter; each answered question folds to one line. A secret shows a dot per character (at most 24) and a count, never the text. Ctrl-C ends a question (`ErrInterrupted`) and the terminal's modes and cursor are restored. Elsewhere the questions are plain line prompts, so scripts and tests that type answers work.
- **Layout.** All keelbox text is wrapped to the terminal at word boundaries with hanging indents; menu details go beside the labels only when every one fits on one line. Cards (rounded box on UTF-8 terminals) hold the impact report, the go-ahead summary with the price, the token steps, the sign-ins and Ready. Problems render as one block: a title, the cause, the next command; "needs you" is drawn apart from a failure.
- **Progress.** Apply's output is read as events (begin, module, step, done, note) and drawn grouped into five phases with human labels, a spinner and elapsed time on the running one, and only phases with changes or failures kept. `--verbose` and plain output keep the engine's step lines.
- **Banner.** A five-row compass rose with the wordmark, only on a UTF-8 terminal of 80 columns or more; one line below that; none when not a terminal or with `ACCESSIBLE`.

## Consequences
- The binary keeps one dependency; the styling is about 2,000 lines of our own code with golden tests at 60, 80, 110, 120 and 200 columns, with and without colour, and pty tests for the drawn questions and Ctrl-C.
- We own the terminal edge cases a library would have absorbed: terminals that do not answer OSC 11 (the timeout), late answers arriving as input (stripped), OSC 8 links shown only on terminals known to support them (`FORCE_HYPERLINK` overrides).
- Vendor sign-in output still goes straight to the terminal through ssh; keelbox frames it with two rules and never reads it.
