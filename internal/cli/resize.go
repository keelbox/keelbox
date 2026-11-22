package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/prompt"
	"github.com/keelbox/keelbox/internal/ui"
)

// keelbox resize (docs/adr/0071-resize-park-resume.md): change the size of
// a box's server, up or down, keeping the box as it is. The server is shut
// down, changed and started, then applied as usual, and the size key of
// the box's config is rewritten in place.

// resizeWarner is a cloud whose resize does more than change the size, and
// which no resize changes back: what resizeWarning returns is said before
// the go-ahead, also with --yes.
type resizeWarner interface {
	resizeWarning() string
}

// sizeAdvice is what a box needs, in the words of keelbox up's size
// question; each cloud's note above the list of sizes ends with it.
const sizeAdvice = "2 vCPU and 4 GB suit one or two agents."

// priced is the size's price, "EUR 7.49/month", or "" when the cloud gives
// none.
func (s cloudSize) priced() string {
	if s.price <= 0 {
		return ""
	}
	return fmt.Sprintf("%s %.2f/month", s.currency, s.price)
}

// phrase describes the size in a line: cores, memory, what else it comes
// with and the price. The disk is in it only withDisk, which is where the
// box's disk is not the same at every size: elsewhere a resize leaves the
// disk as it is, and a disk beside each size would read as if it changed.
func (s cloudSize) phrase(withDisk bool) string {
	parts := []string{s.cpu, s.memory}
	if withDisk && s.disk > 0 {
		parts = append(parts, fmt.Sprintf("%d GB disk", s.disk))
	}
	for _, x := range []string{s.more, s.priced()} {
		if x != "" {
			parts = append(parts, x)
		}
	}
	d := strings.Join(parts, ", ")
	if s.small {
		d += "; small for agents"
	}
	return d
}

// disksDiffer reports whether the box's disk is not the same at every size
// listed, as on a cloud where it grows with the size.
func disksDiffer(sizes []cloudSize) bool {
	for _, s := range sizes {
		if s.disk != sizes[0].disk {
			return true
		}
	}
	return false
}

// sizeChoices are the sizes as the rows of the size question. In a plain
// list each is its phrase. On a drawn menu (aligned) each kind of figure
// has a column, the numbers right-aligned, so that the sizes compare at a
// glance: the cells are padded with spaces no line breaks at, and a menu
// too narrow for a row breaks it between two cells.
func sizeChoices(sizes []cloudSize, aligned bool) []prompt.Choice {
	withDisk := disksDiffer(sizes)
	marks := func(s cloudSize) []string {
		var m []string
		if s.small {
			m = append(m, "small for agents")
		}
		if s.current {
			m = append(m, "the size now")
		}
		return m
	}
	var choices []prompt.Choice
	if !aligned {
		for _, s := range sizes {
			d := s.phrase(withDisk)
			if s.current {
				d += "; the size now"
			}
			choices = append(choices, prompt.Choice{Key: s.name, Label: s.name, Detail: d})
		}
		return choices
	}
	// One column per figure any size has; the price's number in a column
	// of its own width, after the currency.
	amount := 0
	for _, s := range sizes {
		if s.price > 0 {
			amount = max(amount, len(fmt.Sprintf("%.2f", s.price)))
		}
	}
	cells := func(s cloudSize) []string {
		c := []string{s.cpu, s.memory}
		if withDisk {
			c = append(c, fmt.Sprintf("%d GB disk", s.disk))
		}
		c = append(c, s.more, "")
		if s.price > 0 {
			c[len(c)-1] = fmt.Sprintf("%s %*.2f/month", s.currency, amount, s.price)
		}
		return c
	}
	var width []int
	for _, s := range sizes {
		for i, c := range cells(s) {
			if i == len(width) {
				width = append(width, 0)
			}
			width[i] = max(width[i], len(c))
		}
	}
	for _, s := range sizes {
		var row []string
		for i, c := range cells(s) {
			switch {
			case width[i] == 0: // no size has it
			case i == 0:
				row = append(row, fmt.Sprintf("%-*s", width[i], c))
			default:
				row = append(row, fmt.Sprintf("%*s", width[i], c))
			}
		}
		if m := marks(s); len(m) > 0 {
			row = append(row, strings.Join(m, ", "))
		}
		// Between two cells, one space a line may break at and then two it
		// may not, so that a row broken there ends without spaces.
		for i := range row {
			if i > 0 {
				row[i] = "  " + row[i]
			}
			row[i] = ui.Unbroken(row[i])
		}
		choices = append(choices, prompt.Choice{Key: s.name, Label: s.name, Detail: strings.Join(row, " ")})
	}
	return choices
}

// cloudResize is keelbox resize.
func cloudResize(ctx context.Context, cl cloud, cfg *config.Config, o options, p *prompt.Prompter, logf func(string, ...any), stdout, stderr io.Writer) int {
	box, ref := cfg.Box, boxRef(cfg, o.configFile)
	fail := func(err error) int {
		fmt.Fprintln(stderr, "keelbox:", err)
		return 1
	}
	public, srv, err := cloudPublic(ctx, cl, cfg, ref)
	if err != nil {
		return fail(err)
	}
	sizes, note, err := cl.sizes(ctx, srv)
	if err != nil {
		return fail(err)
	}
	cur := -1
	for i, s := range sizes {
		if s.current {
			cur = i
		}
	}
	if cur < 0 || len(sizes) < 2 {
		return fail(fmt.Errorf("%s lists no other size %s can change to and keep its disk", cl.title(), srv.ref))
	}
	// The sizes it can change to, and one of them for an example: the next
	// one up, or the next one down from the largest.
	var others []string
	for _, s := range sizes {
		if !s.current {
			others = append(others, s.name)
		}
	}
	example := sizes[(cur+1)%len(sizes)].name
	withDisk := disksDiffer(sizes)
	want := -1
	switch {
	case o.size != "":
		for i, s := range sizes {
			if s.name == o.size {
				want = i
			}
		}
		if want < 0 {
			fmt.Fprintf(stderr, "keelbox: --size %q is not a size %s can change to and keep its disk. It is %s now and can change to %s: keelbox resize %s --size %s\n",
				o.size, box, sizes[cur].name, strings.Join(others, ", "), ref, example)
			return 2
		}
	case !p.Interactive():
		// The list a terminal would show, then the command.
		fmt.Fprintf(stderr, "keelbox: resize needs a size, and there is no terminal to ask on. %s is %s now (%s) and can change to:\n", box, sizes[cur].name, sizes[cur].phrase(withDisk))
		name := 0
		for _, n := range others {
			name = max(name, len(n))
		}
		for _, s := range sizes {
			if !s.current {
				fmt.Fprintf(stderr, "  %-*s  %s\n", name, s.name, s.phrase(withDisk))
			}
		}
		fmt.Fprintf(stderr, "Name one, for example: %s\n", orOwner("keelbox resize "+ref+" --size "+example+" --yes", "keelbox resize "+ref+" --size "+example))
		return 2
	default:
		if note != "" {
			if o.style.Live {
				// Wrapped to the terminal: a figure stays with its unit.
				note = amounts(note)
			}
			say(stdout, o.style, o.style.Faint(o.style.Bullet()), note)
		}
		// As the prompter decides it: a menu is drawn on a terminal that
		// takes cursor movement and has no screen reader at work.
		drawn := p.Style().Live && !p.Style().Accessible
		i, err := p.Choose("Size for "+box+" (it is "+sizes[cur].name+" now):", sizeChoices(sizes, drawn), cur)
		if err != nil {
			fmt.Fprintln(stdout, "Nothing changed.")
			return 1
		}
		want = i
	}
	if want == cur {
		sayInfo(stdout, o.style, fmt.Sprintf("%s is %s already. Nothing changed.", box, sizes[cur].name))
		return 0
	}
	from, to := sizes[cur], sizes[want]
	// Where only the size changes, the size the box has now can be changed
	// back to.
	warning, back := "", true
	if w, ok := cl.(resizeWarner); ok {
		warning, back = w.resizeWarning(), false
	}
	// The disk is said once: with what stays where the resize leaves it
	// alone, beside each size where it changes.
	stays := "its disk, its address and everything on it"
	switch {
	case from.disk != to.disk:
		stays = "its address and everything on its disk"
	case from.disk > 0:
		stays = fmt.Sprintf("its %d GB disk, its address and everything on it", from.disk)
	}
	if !keepsHostKeys(ctx, cl, cfg, public, "resize", ref, stderr) {
		return 1
	}
	if !o.yes {
		if !p.Interactive() {
			fmt.Fprintf(stderr, "keelbox: resize asks first, because the box restarts; %s\n",
				orOwner("without a terminal: keelbox resize "+ref+" --size "+to.name+" --yes", "keelbox resize "+ref+" --size "+to.name))
			return 2
		}
		st := o.style
		rows := []ui.Line{
			ui.Row("from", from.name+": "+amounts(from.phrase(withDisk))),
			ui.Row("to", to.name+": "+billed(st, to.phrase(withDisk))),
		}
		rows = append(rows, priceChange(from, to, "now")...)
		rows = append(rows,
			ui.Row("what happens", fmt.Sprintf("%s restarts: running sessions, agents, dev servers and containers stop. It is back in %s, typically", box, cl.typical().resize)),
			ui.Row("what stays", amounts(stays)))
		if warning != "" {
			rows = append(rows, ui.Row(st.Warn("warning"), warning))
		}
		if back {
			rows = append(rows, ui.Row("change back", command(st, "keelbox resize "+ref+" --size "+from.name)))
		}
		showGoAhead(stdout, st, "Resize "+box+" from "+from.name+" to "+to.name, rows)
		ok, err := p.Confirm("Resize "+box+"?", false)
		if err != nil || !ok {
			fmt.Fprintln(stdout, "Nothing changed.")
			return 1
		}
	}
	if o.yes && warning != "" {
		sayNote(logf, o.style, warning)
	}
	start := time.Now()
	// From the shutdown to the start, Ctrl-C waits: stopped in between,
	// the box would stay off.
	outer, ctx := ctx, context.WithoutCancel(ctx)
	// As in cloudPark: steps on a terminal, the log's lines elsewhere.
	v := newStepView(stdout, o, logf)
	defer v.close()
	stdout, stderr = v.writer(stdout), v.writer(stderr)
	v.begin("Shutting "+box+" down", "")
	v.say("shutting %s down", box)
	if err := cl.stop(ctx, srv, v.note); err != nil {
		v.failed()
		fmt.Fprintln(stderr, "keelbox: the server did not shut down:", err)
		return startAgain(ctx, cl, srv, box, from.name, ref, v, stderr)
	}
	v.finished("Shut "+box+" down", "")
	v.begin(fmt.Sprintf("Changing %s from %s to %s", srv.ref, from.name, to.name), "")
	v.say("%s is off; changing it from %s to %s", srv.ref, from.name, to.name)
	resized, err := cl.resize(ctx, srv, to.name, v.note)
	if err != nil {
		v.failed()
		fmt.Fprintln(stderr, "keelbox: the size was not changed:", err)
		var left *errLeftParked
		if errors.As(err, &left) {
			// No server to start: the box is its snapshot now.
			_ = saveParkMark(box, parkMark{Since: left.parked.since, Address: srv.ip, Snapshot: left.parked.ref})
			fmt.Fprintf(stderr, "keelbox: %s is parked: %s holds its disk. Bring it back: keelbox resume %s\n", box, left.parked.ref, ref)
			return 1
		}
		return startAgain(ctx, cl, srv, box, from.name, ref, v, stderr)
	}
	v.finished(fmt.Sprintf("Changed %s from %s to %s", srv.ref, from.name, to.name), "")
	v.begin("Starting "+resized.ref, "")
	srv, err = cl.start(ctx, resized)
	if err != nil {
		v.failed()
		fmt.Fprintf(stderr, "keelbox: %s is %s now, but starting it failed: %v. Start it: keelbox resume %s; then: keelbox apply %s\n", box, to.name, err, ref, ref)
		setSize(cfg, o.configFile, cl.sizeKey(), to.name, v.did, stderr)
		return 1
	}
	v.finished(fmt.Sprintf("Started %s at %s", srv.ref, srv.ip), "")
	ctx = outer
	// The config says what the server is before the apply ships it.
	setSize(cfg, o.configFile, cl.sizeKey(), to.name, v.did, stderr)
	if srv.ip != public.host && cfg.Target.SSH.Host == "" {
		// A cloud that gave the address up at the stop: the keys are the
		// box's own, pinned under the address it had.
		next := addr{srv.ip, cfg.Target.SSH.Port}
		movePins(cfg, next, public.host)
		public = next
	}
	o.publicIP = srv.ip
	v.begin("Waiting for SSH at "+public.host, "")
	v.say("%s is %s and running at %s; waiting for SSH", srv.ref, to.name, srv.ip)
	if err := boxComesBack(ctx, cfg, public, stdout, stderr); err != nil {
		v.failed()
		fmt.Fprintln(stderr, "keelbox:", err)
		fmt.Fprintf(stderr, "keelbox: %s is %s now. Once it answers: keelbox apply %s\n", box, to.name, ref)
		return 1
	}
	v.finished("SSH answers at "+public.host, "")
	// The apply has its own view, under the steps so far.
	o.sshUser, o.askSudo = "", false
	if code := applyExisting(ctx, cl, cfg, srv, public, o, v.note, stdout, stderr); code != 0 {
		if code != exitDetached {
			fmt.Fprintf(stderr, "keelbox: %s is %s now, but the apply did not pass; fix the cause above, then: keelbox apply %s\n", box, to.name, ref)
		}
		return code
	}
	showOutcome(stdout, o.style, resizedOutcome(o.style, cfg, ref, time.Since(start), from, to, withDisk, stays, back))
	return 0
}

// startAgain starts a server resize stopped and could not change.
func startAgain(ctx context.Context, cl cloud, srv *cloudServer, box, size, ref string, v *stepView, stderr io.Writer) int {
	v.begin("Starting "+srv.ref+" again", "")
	if s, err := cl.start(ctx, srv); err != nil {
		v.failed()
		fmt.Fprintf(stderr, "keelbox: %s is still %s, but starting it again failed: %v. Start it: keelbox resume %s\n", box, size, err, ref)
	} else {
		v.finished(fmt.Sprintf("%s runs again at %s", s.ref, s.ip), "")
		fmt.Fprintf(stderr, "keelbox: %s is still %s and runs again at %s. Try again: keelbox resize %s\n", box, size, s.ip, ref)
	}
	return 1
}

// setSize makes the box's config say the server's new size: in the file,
// keeping its comments, and in cfg, which the apply that follows ships to
// the box. A file that cannot be edited that way is left as it is, and the
// line to set is printed. did says the edit.
func setSize(cfg *config.Config, path, key, size string, did func(string, ...any), stderr io.Writer) {
	parts := strings.Split(key, ".")
	leaf, parent := parts[len(parts)-1], strings.Join(parts[:len(parts)-1], ".")
	byHand := func(why error) {
		fmt.Fprintf(stderr, "keelbox: could not set %s in %s (%v). Set it yourself, so the config says what the server is; under %s:\n      %s: %s\n", key, tildePath(path), why, parent, leaf, size)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		byHand(err)
		return
	}
	if v, _ := yamlValue(data, parts); v == size {
		return
	}
	out, err := setYAMLKey(string(data), parts, size)
	if err == nil {
		// The edit is kept only if the file then says the new size and
		// still loads.
		if v, verr := yamlValue([]byte(out), parts); verr != nil || v != size {
			err = errors.New("the edited file would not say the new size")
		}
	}
	if err != nil {
		byHand(err)
		return
	}
	fi, _ := os.Stat(path)
	mode := os.FileMode(0o644)
	if fi != nil {
		mode = fi.Mode().Perm()
	}
	if err := os.WriteFile(path, []byte(out), mode); err != nil { //nolint:gosec // G703: the box's own config, the file this command was given
		byHand(err)
		return
	}
	next, err := config.Load(path)
	if err != nil {
		// Not this edit: put the file back as it was.
		_ = os.WriteFile(path, data, mode) //nolint:gosec // G703: the file as it was
		byHand(err)
		return
	}
	// The cloud holds cfg too: both see the new size.
	*cfg = *next
	did("set %s: %s in %s", key, size, tildePath(path))
}

// yamlValue is the scalar at a path of mapping keys in a YAML document.
func yamlValue(doc []byte, path []string) (string, error) {
	var v any
	if err := yaml.Unmarshal(doc, &v); err != nil {
		return "", err
	}
	for _, k := range path {
		m, ok := v.(map[string]any)
		if !ok {
			return "", fmt.Errorf("%s is not a mapping", k)
		}
		v = m[k]
	}
	s, ok := v.(string)
	if !ok {
		return "", errors.New("not a string")
	}
	return s, nil
}

var (
	yamlKeyLine  = regexp.MustCompile(`^( *)([A-Za-z0-9_-]+):(.*)$`)
	yamlValueRe  = regexp.MustCompile(`^(\s*)("[^"]*"|'[^']*'|[A-Za-z0-9][^#]*?)?(\s+#.*|\s*)$`)
	plainScalar  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	errNotBlock  = errors.New("the key is not on a line of its own")
	errNoSuchKey = errors.New("the key is not in the file")
)

// setYAMLKey sets the scalar at a path of mapping keys in a YAML document
// and returns the document with only that value changed: every other
// line, the comments, the order and the trailing comment of the line stay.
// It edits only block-style mappings with each key on its own line, as
// keelbox writes configs; anything else is an error, and the caller leaves
// the file alone.
func setYAMLKey(doc string, path []string, value string) (string, error) {
	if !plainScalar.MatchString(value) {
		return "", fmt.Errorf("%q is not a plain value", value)
	}
	lines := strings.SplitAfter(doc, "\n")
	// The block being searched: lines[lo:hi], whose keys are indented more
	// than parent.
	lo, hi, parent := 0, len(lines), -1
	for depth, key := range path {
		found, indent := -1, -1
		for i := lo; i < hi; i++ {
			m := yamlKeyLine.FindStringSubmatch(strings.TrimRight(lines[i], "\r\n"))
			text := strings.TrimSpace(lines[i])
			if text == "" || strings.HasPrefix(text, "#") {
				continue
			}
			if m == nil {
				if indent < 0 {
					return "", errNotBlock // a list or a flow mapping where keys were expected
				}
				continue
			}
			if indent < 0 {
				// The first key of the block sets its indent.
				if indent = len(m[1]); indent <= parent {
					return "", errNoSuchKey
				}
			}
			if len(m[1]) == indent && m[2] == key {
				found = i
				break
			}
		}
		if found < 0 {
			return "", errNoSuchKey
		}
		m := yamlKeyLine.FindStringSubmatch(strings.TrimRight(lines[found], "\r\n"))
		if depth == len(path)-1 {
			v := yamlValueRe.FindStringSubmatch(m[3])
			if v == nil || v[2] == "" {
				return "", errNotBlock
			}
			eol := lines[found][len(strings.TrimRight(lines[found], "\r\n")):]
			next := value
			if strings.HasPrefix(v[2], `"`) {
				next = `"` + value + `"`
			} else if strings.HasPrefix(v[2], "'") {
				next = "'" + value + "'"
			}
			tail := v[3]
			if strings.Contains(tail, "#") {
				// Keep the comment in its column where the new value fits.
				gap := len(v[2]) + len(tail) - len(strings.TrimLeft(tail, " \t")) - len(next)
				tail = strings.Repeat(" ", max(gap, 1)) + strings.TrimLeft(tail, " \t")
			}
			lines[found] = m[1] + m[2] + ":" + v[1] + next + tail + eol
			return strings.Join(lines, ""), nil
		}
		// A parent: nothing but a comment may follow its colon.
		if rest := strings.TrimSpace(m[3]); rest != "" && !strings.HasPrefix(rest, "#") {
			return "", errNotBlock
		}
		lo = found + 1
		end := hi
		for i := lo; i < hi; i++ {
			text := strings.TrimSpace(lines[i])
			if text == "" || strings.HasPrefix(text, "#") {
				continue
			}
			if len(lines[i])-len(strings.TrimLeft(lines[i], " ")) <= indent {
				end = i
				break
			}
		}
		hi, parent = end, indent
	}
	return "", errNoSuchKey
}
