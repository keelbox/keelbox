package modules

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/system"
)

// TerminfoDir is where keelbox compiles its terminfo aliases: the
// system-local directory, which ncurses searches before the packages'
// (ncurses-base owns only its README). ADR 0094.
const TerminfoDir = "/etc/terminfo"

// terminfoAliases are TERM names terminals send over ssh that no small
// package provides, each with the release's entry it copies. Ghostty
// (and cmux, built on it) sends xterm-ghostty; only the ghostty package,
// the whole terminal, ships that name. kitty's xterm-kitty comes from the
// kitty-terminfo package instead (BasePackages).
var terminfoAliases = []struct{ Name, Base string }{
	{"xterm-ghostty", "ghostty"},
}

// terminfoFallback stands in for a base entry the release does not have:
// Ubuntu 24.04's ncurses-term has no ghostty. Ghostty speaks it; only its
// extras, such as styled underlines, go unused.
const terminfoFallback = "xterm-256color"

// terminfoMark is in the description of every alias keelbox compiles, so a
// later run tells its own entries from one another tool installed.
const terminfoMark = "keelbox alias of "

// terminfoFile is where tic puts a compiled entry.
func terminfoFile(name string) string { return TerminfoDir + "/" + name[:1] + "/" + name }

type terminfoAction int

const (
	terminfoNone terminfoAction = iota
	terminfoCompile
	terminfoRemove
	terminfoModes
)

type terminfoTodo struct {
	action terminfoAction
	name   string
	detail string
	src    string // for terminfoCompile
}

// terminfoStep keeps each alias a readable copy of its base entry as the
// release has it now, and removes keelbox's copy once a package installs
// the name. An entry of that name keelbox did not compile is left alone.
// Nothing later depends on it, so a failure here stops no other step.
func terminfoStep() engine.Step {
	plan := func(ctx context.Context, e *engine.Env) ([]terminfoTodo, error) {
		var todo []terminfoTodo
		for _, a := range terminfoAliases {
			t, err := terminfoPlan(ctx, e, a.Name, a.Base)
			if err != nil {
				return nil, err
			}
			if t.action != terminfoNone {
				todo = append(todo, t)
			}
		}
		return todo, nil
	}
	return engine.Step{
		Name: "terminfo",
		Soft: true,
		Check: func(ctx context.Context, e *engine.Env) (engine.Status, error) {
			todo, err := plan(ctx, e)
			if err != nil {
				return engine.Status{}, err
			}
			if len(todo) == 0 {
				return engine.Converged(), nil
			}
			details := make([]string, len(todo))
			for i, t := range todo {
				details[i] = t.detail
			}
			return engine.Pending("%s", strings.Join(details, "; ")), nil
		},
		Apply: func(ctx context.Context, e *engine.Env) error {
			todo, err := plan(ctx, e)
			if err != nil {
				return err
			}
			for _, t := range todo {
				f := terminfoFile(t.name)
				switch t.action {
				case terminfoRemove:
					if err := e.X.Remove(f); err != nil {
						return err
					}
					continue
				case terminfoCompile:
					if _, err := e.X.Run(ctx, system.Cmd{Name: "tic", Args: []string{"-x", "-o", TerminfoDir, "-"}, Stdin: []byte(t.src)}); err != nil {
						return fmt.Errorf("compile terminfo %s: %w", t.name, err)
					}
				}
				// tic creates with root's umask; every account reads them.
				for p, mode := range map[string]string{path.Dir(f): "0755", f: "0644"} {
					if _, err := e.X.Run(ctx, system.C("chmod", mode, p)); err != nil {
						return err
					}
				}
			}
			return nil
		},
	}
}

func terminfoPlan(ctx context.Context, e *engine.Env, name, base string) (terminfoTodo, error) {
	t := terminfoTodo{name: name}
	f := terminfoFile(name)
	st, err := e.X.Stat(f)
	if err != nil {
		return t, err
	}
	var out string
	if st.Exists {
		if out, err = system.Output(ctx, e.X, system.C("infocmp", "-A", TerminfoDir, "-x", "-1", name)); err != nil {
			// Not keelbox's: it writes only entries infocmp reads.
			return t, fmt.Errorf("%s is not a terminfo entry infocmp can read, and keelbox did not write it. To have keelbox write its own, remove it: keelbox ssh %s --admin, then sudo rm %s, then keelbox apply %s", f, e.Cfg.Box, f, e.Cfg.Box)
		}
	}
	ours := st.Exists && strings.Contains(terminfoNames(out), terminfoMark)
	if st.Exists && !ours {
		return t, nil // another tool's entry
	}
	dirs, err := terminfoPackageDirs(ctx, e)
	if err != nil {
		return t, err
	}
	if _, ok := terminfoPackaged(ctx, e, dirs, name); ok {
		if ours {
			t.action, t.detail = terminfoRemove, fmt.Sprintf("remove keelbox's terminfo %s: a package installs it", name)
		}
		return t, nil
	}
	body, ok := terminfoPackaged(ctx, e, dirs, base)
	if !ok {
		base = terminfoFallback
		if body, ok = terminfoPackaged(ctx, e, dirs, base); !ok {
			return t, fmt.Errorf("terminfo %s: the release has neither ghostty nor %s in %s. Reinstall them: keelbox ssh %s --admin, then sudo apt-get install --reinstall ncurses-base ncurses-term, then keelbox apply %s", name, base, strings.Join(dirs, ", "), e.Cfg.Box, e.Cfg.Box)
		}
	}
	want := name + "|" + terminfoMark + base + ","
	switch {
	case !st.Exists:
		t.detail = fmt.Sprintf("add terminfo %s, a copy of %s", name, base)
	case terminfoNames(out) != want || terminfoBody(out) != body:
		t.detail = fmt.Sprintf("update terminfo %s from the release's %s", name, base)
	default:
		for p, mode := range map[string]uint32{path.Dir(f): 0o755, f: 0o644} {
			st, err := e.X.Stat(p)
			if err != nil {
				return t, err
			}
			if uint32(st.Mode.Perm()) != mode {
				t.action, t.detail = terminfoModes, fmt.Sprintf("make %s mode %o, so every account reads it", p, mode)
				return t, nil
			}
		}
		return t, nil
	}
	// The source is the base's capabilities as infocmp prints them, under
	// the alias's names: tic then reads nothing but this, wherever its own
	// search for a use= would have looked.
	t.action, t.src = terminfoCompile, want+"\n\t"+strings.ReplaceAll(body, "\n", "\n\t")+"\n"
	return t, nil
}

// terminfoPackageDirs are the directories ncurses searches, as infocmp -D
// lists them, without keelbox's own and any account's ~/.terminfo: where
// the packages' entries are.
func terminfoPackageDirs(ctx context.Context, e *engine.Env) ([]string, error) {
	out, err := system.Output(ctx, e.X, system.C("infocmp", "-D"))
	if err != nil {
		return nil, fmt.Errorf("list the terminfo directories: %w", err)
	}
	var dirs []string
	for _, d := range strings.Fields(out) {
		if d != TerminfoDir && path.IsAbs(d) && !strings.HasSuffix(d, "/.terminfo") && !contains(dirs, d) {
			dirs = append(dirs, d)
		}
	}
	return dirs, nil
}

// terminfoPackaged is a name's entry in the first package directory that
// has it, as infocmp prints its capabilities after the names line.
func terminfoPackaged(ctx context.Context, e *engine.Env, dirs []string, name string) (string, bool) {
	for _, d := range dirs {
		if out, err := system.Output(ctx, e.X, system.C("infocmp", "-A", d, "-x", "-1", name)); err == nil {
			return terminfoBody(out), true
		}
	}
	return "", false
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

// terminfoBody is the capabilities in infocmp's output: what follows the
// names line, without comments.
func terminfoBody(out string) string {
	var b []string
	names := false
	for _, l := range strings.Split(out, "\n") {
		t := strings.TrimSpace(l)
		switch {
		case t == "" || strings.HasPrefix(t, "#"):
		case !names:
			names = true
		default:
			b = append(b, t)
		}
	}
	return strings.Join(b, "\n")
}
