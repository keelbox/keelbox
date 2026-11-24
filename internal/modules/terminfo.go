package modules

import (
	"context"
	"fmt"
	"strings"

	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/system"
)

// TerminfoDir is where keelbox compiles its terminfo aliases. ncurses
// looks there before /usr/share/terminfo, and no package installs into it.
const TerminfoDir = "/etc/terminfo"

// terminfoAliases are TERM names that terminals send over ssh and that
// Ubuntu's ncurses-term lacks, with the entry of its own each stands for.
// Ghostty (and cmux, built on it) sends xterm-ghostty and kitty sends
// xterm-kitty; without an entry, bash's line editing, vim and less
// misbehave and tmux will not start.
var terminfoAliases = []struct{ Name, Base string }{
	{"xterm-ghostty", "ghostty"},
	{"xterm-kitty", "kitty"},
}

// terminfoFallback stands in for a base entry the release does not have:
// Ubuntu 24.04's ncurses-term has kitty but not ghostty. Both terminals
// speak it; only their extras, such as styled underlines, go unused.
const terminfoFallback = "xterm-256color"

// terminfoMark is in the description of every alias keelbox compiles, so a
// later run can tell its own entries from one another tool installed.
const terminfoMark = "keelbox alias of "

// terminfoStep compiles each alias as a copy of its base entry. An entry
// of that name that keelbox did not compile is left alone.
func terminfoStep() engine.Step {
	pending := func(ctx context.Context, e *engine.Env) []string {
		var src []string
		for _, a := range terminfoAliases {
			base := a.Base
			if !system.Succeeds(ctx, e.X, system.C("infocmp", base)) {
				base = terminfoFallback
			}
			want := a.Name + "|" + terminfoMark + base + ","
			out, err := system.Output(ctx, e.X, system.C("infocmp", "-x", "-1", a.Name))
			if err == nil {
				got := terminfoNames(out)
				if got == want || !strings.Contains(got, terminfoMark) {
					continue
				}
			}
			src = append(src, want+"\n\tuse="+base+",\n")
		}
		return src
	}
	return engine.Step{
		Name: "terminfo",
		Check: func(ctx context.Context, e *engine.Env) (engine.Status, error) {
			if src := pending(ctx, e); len(src) > 0 {
				names := make([]string, len(src))
				for i, s := range src {
					names[i], _, _ = strings.Cut(s, "|")
				}
				return engine.Pending("add terminfo for %s in %s", strings.Join(names, ", "), TerminfoDir), nil
			}
			return engine.Converged(), nil
		},
		Apply: func(ctx context.Context, e *engine.Env) error {
			src := pending(ctx, e)
			if len(src) == 0 {
				return nil
			}
			if _, err := e.X.Run(ctx, system.Cmd{Name: "tic", Args: []string{"-x", "-o", TerminfoDir, "-"}, Stdin: []byte(strings.Join(src, ""))}); err != nil {
				return fmt.Errorf("compile terminfo aliases: %w", err)
			}
			return nil
		},
	}
}

// terminfoNames is the names line of infocmp's output: the first line
// that is not a comment.
func terminfoNames(out string) string {
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			return l
		}
	}
	return ""
}
