package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/modules"
	"github.com/keelbox/keelbox/internal/ui"
)

// moduleNames are the modules an apply of cfg runs, in order.
func moduleNames(cfg *config.Config) []string {
	var out []string
	for _, m := range modules.All() {
		if m.Enabled(cfg) {
			out = append(out, m.Name())
		}
	}
	return out
}

// applyView is where an apply's log goes: on a terminal, the compact
// progress view (one line per module, the current step and elapsed time);
// with --verbose or without a terminal, every line as it comes. The
// returned function settles the view.
func applyView(cfg *config.Config, o options, stdout, stderr io.Writer) (io.Writer, io.Writer, func()) {
	return applyViewModules(moduleNames(cfg), o, stdout, stderr)
}

func applyViewModules(modules []string, o options, stdout, stderr io.Writer) (io.Writer, io.Writer, func()) {
	if !o.style.Live || o.verbose {
		return stdout, stderr, func() {}
	}
	p := ui.NewProgress(stdout, o.style, modules).Start()
	return p, p.Err(stderr), p.Close
}

// say writes one line of what keelbox is doing or found: "keelbox: text"
// in plain text, the mark and the text, wrapped, on a styled terminal.
func say(out io.Writer, st ui.Style, mark, text string) {
	if !st.Live {
		fmt.Fprintln(out, st.Plain("keelbox: "+text, 2))
		return
	}
	if text != "" {
		text = strings.ToUpper(text[:1]) + text[1:]
	}
	sayAsIs(out, st, mark, text)
}

// sayAsIs is say for text that starts with a name, such as a host or a
// box, kept as it is written.
func sayAsIs(out io.Writer, st ui.Style, mark, text string) {
	if !st.Live {
		fmt.Fprintln(out, st.Plain("keelbox: "+text, 2))
		return
	}
	fmt.Fprintln(out, "  "+mark+" "+ui.Hang(text, st.Cols()-4, 4))
}
