package cli

import (
	"errors"
	"fmt"
	"github.com/keelbox/keelbox/internal/ui"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/modules"
	"github.com/keelbox/keelbox/internal/prompt"
)

// The box registry: one config per box in ~/.config/keelbox/boxes/, so
// every command takes a box name, or nothing when there is one box
// (docs/adr/0025-keelbox-up.md). Config paths still work everywhere.

// configDir is keelbox's directory on this machine: known_hosts, the box
// registry and the file credential store live here.
func configDir() string { return config.ExpandHome("~/.config/keelbox") }

func boxesDir() string { return filepath.Join(configDir(), "boxes") }

// boxConfigPath is where the registry keeps a box's config.
func boxConfigPath(box string) string { return filepath.Join(boxesDir(), box+".yaml") }

var boxNameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// registeredBoxes lists the boxes in the registry, sorted by name.
func registeredBoxes() []string {
	matches, _ := filepath.Glob(filepath.Join(boxesDir(), "*.yaml"))
	var out []string
	for _, m := range matches {
		if name := strings.TrimSuffix(filepath.Base(m), ".yaml"); boxNameRe.MatchString(name) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// pendingBoxes are boxes keelbox up is waiting on a server for: they have
// a progress record but no config yet.
func pendingBoxes() []string {
	matches, _ := filepath.Glob(filepath.Join(boxesDir(), "*.up.json"))
	var out []string
	for _, m := range matches {
		name := strings.TrimSuffix(filepath.Base(m), ".up.json")
		if st, ok := loadUpState(&config.Config{Box: name}); ok && st.Pending != nil && !isRegistered(name) {
			out = append(out, name)
		}
	}
	return out
}

// isRegistered reports whether the registry has a config for box.
func isRegistered(box string) bool {
	if !boxNameRe.MatchString(box) {
		return false
	}
	_, err := os.Stat(boxConfigPath(box))
	return err == nil
}

// resolveConfig finds the config a command means. An argument is a config
// file if one exists at that path, else a box name in the registry. With
// no argument: the applied config on a box, ./keelbox.yaml, or the only
// box in the registry; with several, the one picked at p's question
// (pickBox), and an error that lists them when p is nil or has no terminal.
func resolveConfig(cmd string, pos []string, p *prompt.Prompter) (string, error) {
	if len(pos) > 1 {
		return "", fmt.Errorf("%s takes one box name or config file, got %d arguments: keelbox %s %s", cmd, len(pos), cmd, pos[0])
	}
	if len(pos) == 1 {
		arg := pos[0]
		if fi, err := os.Stat(arg); err == nil && !fi.IsDir() {
			return arg, nil
		}
		if isRegistered(arg) {
			return boxConfigPath(arg), nil
		}
		if contains(pendingBoxes(), arg) {
			return "", notSetUpYet(arg)
		}
		if strings.ContainsRune(arg, os.PathSeparator) || strings.HasSuffix(arg, ".yaml") || strings.HasSuffix(arg, ".yml") {
			return "", fmt.Errorf("%s: no such config file; write one: keelbox init %s", arg, arg)
		}
		if near := nearest(arg, registeredBoxes()); near != "" {
			return "", fmt.Errorf("no box named %q; did you mean keelbox %s %s? List boxes: keelbox ls", arg, cmd, near)
		}
		return "", fmt.Errorf("no box named %q in %s; list boxes: keelbox ls, or set one up: keelbox up %s", arg, tildePath(boxesDir()), arg)
	}
	for _, p := range []string{modules.ConfigPath, "keelbox.yaml"} {
		_, err := os.Stat(p)
		if err != nil && !errors.Is(err, os.ErrPermission) {
			continue
		}
		// On the machine keelbox up set up, the box's own config is
		// root's; the registry has the same box, readable.
		if p == modules.ConfigPath && !readable(p) {
			if b := localBox(); b != "" {
				return boxConfigPath(b), nil
			}
		}
		return p, nil // root-only on a box: the command runs again with sudo
	}
	switch boxes := registeredBoxes(); len(boxes) {
	case 0:
		if pending := pendingBoxes(); len(pending) == 1 {
			return "", notSetUpYet(pending[0])
		}
		return "", errors.New("no box yet, and no ./keelbox.yaml here; set one up: keelbox up")
	case 1:
		return boxConfigPath(boxes[0]), nil
	default:
		box, err := pickBox(cmd, boxes, p)
		if err != nil {
			return "", err
		}
		return boxConfigPath(box), nil
	}
}

func readable(p string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	f.Close()
	return true
}

// localBox is the registry's one box that is this machine, if there is one.
func localBox() string {
	var found []string
	for _, b := range registeredBoxes() {
		if cfg, err := config.Load(boxConfigPath(b)); err == nil && cfg.Target.Provider == config.ProviderLocal {
			found = append(found, b)
		}
	}
	if len(found) == 1 {
		return found[0]
	}
	return ""
}

// boxRef is how messages name a box in commands: its name when its config
// is the registry's, otherwise the config path.
func boxRef(cfg *config.Config, path string) string {
	if path == "" {
		return cfg.Box
	}
	if a, err := filepath.Abs(path); err == nil {
		if b, err := filepath.Abs(boxConfigPath(cfg.Box)); err == nil && a == b {
			return cfg.Box
		}
	}
	return path
}

// boxRow is one line of keelbox ls.
type boxRow struct {
	name, provider, address, status string
	// state is what stands in the way of using the box, in the words of
	// state.go, for the question "Which box?"; "" for a box that is set
	// up, or one keelbox up never handled.
	state string
}

func listRow(name string) boxRow {
	row := boxRow{name: name, provider: "?", address: "-"}
	if st, ok := loadUpState(&config.Config{Box: name}); ok && st.Pending != nil {
		row.provider, row.status, row.state = "ssh (cloud-init)", st.describe(name), st.what(name)
		return row
	}
	cfg, err := config.Load(boxConfigPath(name))
	if err != nil {
		row.status, row.state = stateBroken+": keelbox plan "+name, stateBroken
		return row
	}
	row.provider = cfg.Target.Provider
	st, hasState := loadUpState(cfg)
	if hasState && st.Via == viaCloudInit {
		row.provider = "ssh (cloud-init)"
	}
	switch {
	case cfg.Target.Provider == config.ProviderLocal:
		row.address = "this machine"
	case hasState && st.Address != "":
		row.address = st.Address
	case cfg.Target.SSH.Host != "":
		row.address = cfg.Target.SSH.Host
	}
	if r, ok := loadRecord(cfg); ok {
		row.address = r.TailnetHost + " (tailnet)"
	}
	row.status = "-"
	if hasState {
		row.status = st.describe(name)
		if st.Stage != stageReady {
			row.state = st.what(name)
		}
	}
	// Package requests of the agent's that wait for keelbox approve, as
	// this computer last heard from the box: keelbox ls reaches no box.
	if c := requestsCell(name); c != "" {
		if row.status == "-" {
			row.status = c
		} else {
			row.status += "; " + c
		}
	}
	if m, ok := loadDestroyedMark(name); ok && isCloud(cfg.Target.Provider) {
		// keelbox destroy deleted the server and kept the config.
		row.address, row.status, row.state = "-", destroyedStatus(name, m.At), stateDestroyed
	}
	if m, ok := loadParkMark(name); ok && isCloud(cfg.Target.Provider) {
		// No server, so no address; the date is when it was parked.
		row.address, row.status, row.state = "-", stateParked+" since "+day(m.Since)+": keelbox resume "+name, stateParked
	}
	return row
}

// statusCell marks a box's status and paints the commands in it: a tick
// for ready, a bang for every state that needs something done (keelbox up
// to continue, a sign-in left), a cross for a config error, a bullet for a
// box without a server (parked, destroyed). The words carry the meaning;
// the mark and colour help. A command stays whole when the cell wraps.
func statusCell(st ui.Style, s string) string {
	if !st.Live {
		return s
	}
	painted := paintCommands(st, s)
	switch {
	case s == stateReady:
		return st.Tick() + " " + s
	case strings.HasPrefix(s, stateBroken):
		return st.Cross() + " " + painted
	case strings.HasPrefix(s, stateParked), strings.HasPrefix(s, stateDestroyed):
		return st.Faint(st.Bullet()) + " " + painted
	case strings.Contains(s, ": keelbox "):
		return st.Bang() + " " + painted
	}
	return s
}

// commandRe is a keelbox command as a status or a note names it: the
// command, the box, and a flag or two.
var commandRe = regexp.MustCompile(`keelbox [a-z][a-z-]*( [a-z0-9][a-z0-9.-]*)?( --[a-z-]+( [A-Za-z0-9._-]+)?)*`)

// paintCommands is text with each keelbox command in it in the accent
// colour and never broken across lines.
func paintCommands(st ui.Style, text string) string {
	return commandRe.ReplaceAllStringFunc(text, func(c string) string { return st.Cmd(ui.Unbroken(c)) })
}

// cmdList prints the registry's boxes.
func cmdList(st ui.Style, stdout, stderr io.Writer) int {
	boxes := append(registeredBoxes(), pendingBoxes()...)
	sort.Strings(boxes)
	if len(boxes) == 0 {
		fmt.Fprintf(stdout, "No boxes in %s. Set one up: %s\n", tildePath(boxesDir()), st.Cmd("keelbox up"))
		return 0
	}
	var rows [][]string
	for _, b := range boxes {
		r := listRow(b)
		rows = append(rows, []string{st.Bold(r.name), r.provider, r.address, statusCell(st, r.status)})
	}
	fmt.Fprint(stdout, st.Table([]string{"BOX", "PROVIDER", "ADDRESS", "STATUS"}, rows))
	if _, err := os.Stat("keelbox.yaml"); err == nil {
		fmt.Fprintln(stdout, "\n"+st.Plain("./keelbox.yaml is used when a command names no box.", 0))
	}
	return 0
}
