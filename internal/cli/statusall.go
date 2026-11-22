package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/keelbox/keelbox/internal/ui"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/plain"
	"github.com/keelbox/keelbox/internal/provider/aws"
	"github.com/keelbox/keelbox/internal/provider/digitalocean"
	"github.com/keelbox/keelbox/internal/provider/gcp"
	"github.com/keelbox/keelbox/internal/provider/hetzner"
	"github.com/keelbox/keelbox/internal/snapshot"
)

// keelbox status --all (docs/adr/0041-status-all.md): one table of every
// box in the registry, from the status snapshot each box's watch run
// writes. For each box it runs `cat` on that snapshot over plain ssh as the
// admin, with keelbox's known_hosts, strictly: nothing is uploaded and
// nothing on the box runs as root. Boxes are read ten at a time, each
// within statusAllTimeout, so a registry of boxes that are all off still answers
// in seconds.

var (
	statusAllParallel = 10
	statusAllTimeout  = 12 * time.Second
	// statusAllConnect is ssh's ConnectTimeout, in seconds.
	statusAllConnect = 8
	// statusAllFetch reads one box; tests replace it.
	statusAllFetch = fetchStatusAllRow
)

// statusAllRow is one box: whether it was reached and what its snapshot says.
type statusAllRow struct {
	Box string `json:"box"`
	// Reach is ok, stale (reached, but the snapshot is over 10 minutes
	// old), parked (keelbox park deleted its server and keeps its disk),
	// destroyed (keelbox destroy deleted its server and the config is
	// kept), unreachable or error.
	Reach string `json:"reach"`
	Error string `json:"error,omitempty"`
	// Parked is when a parked box was parked.
	Parked *time.Time `json:"parked,omitempty"`
	// Destroyed is when a destroyed box's server was deleted.
	Destroyed *time.Time       `json:"destroyed,omitempty"`
	Status    *snapshot.Status `json:"status,omitempty"`
}

const (
	reachOK          = "ok"
	reachStale       = "stale"
	reachParked      = "parked"
	reachDestroyed   = "destroyed"
	reachUnreachable = "unreachable"
	reachError       = "error"
)

func cmdStatusAll(ctx context.Context, o options, pos []string, stdout, stderr io.Writer) int {
	if len(pos) > 0 {
		failf(stderr, "status --all reads every box in %s and takes no box; for one box: keelbox status %s\n", tildePath(boxesDir()), pos[0])
		return 2
	}
	boxes := registeredBoxes()
	if len(boxes) == 0 {
		if o.json {
			fmt.Fprintln(stdout, "[]")
			return 0
		}
		fmt.Fprintf(stdout, "No boxes in %s. Set one up: keelbox up\n", tildePath(boxesDir()))
		return 0
	}
	rows := statusAllRows(ctx, boxes, time.Now())
	// What each box that answered says of the agent's package requests
	// is kept for keelbox ls.
	for _, r := range rows {
		if r.Status != nil && (r.Reach == reachOK || r.Reach == reachStale) {
			saveRequestsNote(r.Box, r.Status.RequestsWaiting, r.Status.Written)
		}
	}
	if o.json {
		b, err := json.MarshalIndent(rows, "", "  ")
		if err != nil {
			fail(stderr, err)
			return 1
		}
		fmt.Fprintln(stdout, string(b))
	} else {
		printStatusAllStyled(stdout, o.style, rows, time.Now())
	}
	return statusAllExit(rows)
}

// statusAllRows reads every box, statusAllParallel at a time.
func statusAllRows(ctx context.Context, boxes []string, now time.Time) []statusAllRow {
	rows := make([]statusAllRow, len(boxes))
	sem := make(chan struct{}, statusAllParallel)
	var wg sync.WaitGroup
	for i, b := range boxes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			ctx, cancel := context.WithTimeout(ctx, statusAllTimeout)
			defer cancel()
			r := statusAllFetch(ctx, b)
			r.Box = b
			if r.Reach == reachOK && r.Status != nil && r.Status.Stale(now) {
				r.Reach = reachStale
			}
			rows[i] = r
		}()
	}
	wg.Wait()
	return rows
}

// statusAllExit is 0 when every box was reached, its snapshot is fresh and
// its last doctor found no failure. A parked box, or one whose server was
// destroyed, is as its owner left it.
func statusAllExit(rows []statusAllRow) int {
	for _, r := range rows {
		if r.Reach == reachParked || r.Reach == reachDestroyed {
			continue
		}
		if r.Reach != reachOK || (r.Status != nil && r.Status.Doctor != nil && r.Status.Doctor.Failures > 0) {
			return 1
		}
	}
	return 0
}

func fetchStatusAllRow(ctx context.Context, box string) statusAllRow {
	cfg, err := config.Load(boxConfigPath(box))
	if err != nil {
		return statusAllRow{Reach: reachError, Error: "the config does not load: keelbox plan " + box}
	}
	if cfg.Target.Provider == config.ProviderLocal {
		data, err := os.ReadFile(snapshot.Path)
		if err != nil {
			return noSnapshot(box)
		}
		return parsedRow(box, data)
	}
	// A box this computer parked, or found parked, has no server to ask.
	if m, ok := loadParkMark(box); ok && isCloud(cfg.Target.Provider) {
		return parkedRow(box, m.Since)
	}
	// Nor has one whose server keelbox destroy deleted.
	if m, ok := loadDestroyedMark(box); ok && isCloud(cfg.Target.Provider) {
		return statusAllRow{Reach: reachDestroyed, Destroyed: &m.At, Error: destroyedStatus(box, m.At)}
	}
	public, err := statusAllPublic(ctx, cfg)
	if err != nil {
		if since, ok := statusAllParked(ctx, cfg); ok {
			return parkedRow(box, since)
		}
		return statusAllRow{Reach: reachUnreachable, Error: err.Error()}
	}
	a := public
	if t, ok := tailnetAddr(cfg); ok && cfg.Tailscale() && t != public {
		s := sshTarget(cfg, t, options{}, io.Discard, io.Discard)
		if public.host != "" {
			_ = s.CopyPins(public.host, public.port)
		}
		if (s.IsKnown() || public.host == "") && dialable(ctx, t, 3*time.Second) == nil {
			a = t
		}
	}
	if a.host == "" {
		if _, ok := tailnetAddr(cfg); ok {
			return statusAllRow{Reach: reachUnreachable, Error: "unreachable: no answer over the tailnet; keelbox ssh " + box}
		}
		return statusAllRow{Reach: reachUnreachable, Error: "no address for it on this machine: keelbox status " + box}
	}
	s := sshTarget(cfg, a, options{}, io.Discard, io.Discard)
	s.ConfirmKeys = nil // never ask: an unknown host key is an error here
	s.ConnectTimeout = statusAllConnect
	out, err := s.Output(ctx, "cat "+snapshot.Path)
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() != 255 && ctx.Err() == nil {
			return noSnapshot(box) // reached; cat found no snapshot
		}
		return statusAllRow{Reach: reachUnreachable, Error: unreachable(ctx, err, box)}
	}
	return parsedRow(box, []byte(out))
}

// statusAllPublic is the box's address outside the tailnet: target.ssh.host,
// the address keelbox up recorded, or for a cloud box the
// provider's, with a credential that needs no prompt.
func statusAllPublic(ctx context.Context, cfg *config.Config) (addr, error) {
	if cfg.Target.SSH.Host != "" {
		return addr{cfg.Target.SSH.Host, cfg.Target.SSH.Port}, nil
	}
	if st, ok := loadUpState(cfg); ok && st.Address != "" {
		return addr{st.Address, cfg.Target.SSH.Port}, nil
	}
	if !isCloud(cfg.Target.Provider) {
		return addr{}, nil
	}
	if t, ok := tailnetAddr(cfg); ok && cfg.Tailscale() {
		return addr{port: t.port}, nil // reached over the tailnet only
	}
	if cfg.Target.Provider == config.ProviderAWS {
		if !awsInstalled() {
			return addr{}, fmt.Errorf("the AWS CLI is not installed, so its address is not known: keelbox status %s", cfg.Box)
		}
		in, err := aws.Find(ctx, awsClient(cfg.Target.AWS.Profile, cfg.Target.AWS.Region), cfg.Box)
		if err != nil {
			return addr{}, fmt.Errorf("finding it in AWS: %v", err)
		}
		return addr{in.PublicIPAddress, cfg.Target.SSH.Port}, nil
	}
	if cfg.Target.Provider == config.ProviderDigitalOcean {
		if !doInstalled() {
			return addr{}, fmt.Errorf("doctl is not installed, so its address is not known: keelbox status %s", cfg.Box)
		}
		d, err := digitalocean.Find(ctx, doClient(cfg.Target.DigitalOcean.Context), cfg.Box)
		if err != nil {
			return addr{}, fmt.Errorf("finding it in DigitalOcean: %v", err)
		}
		return addr{d.PublicIPv4(), cfg.Target.SSH.Port}, nil
	}
	if cfg.Target.Provider == config.ProviderGCP {
		if !gcpInstalled() {
			return addr{}, fmt.Errorf("gcloud is not installed, so its address is not known: keelbox status %s", cfg.Box)
		}
		in, err := gcp.Find(ctx, gcpClient(cfg.Target.GCP.Project), cfg.Box)
		if err != nil {
			return addr{}, fmt.Errorf("finding it in Google Cloud: %v", err)
		}
		return addr{in.PublicIPv4(), cfg.Target.SSH.Port}, nil
	}
	tok, err := hetznerToken(ctx, cfg, cfg.Box, noPrompt(), io.Discard)
	if err != nil {
		return addr{}, fmt.Errorf("no Hetzner token to find its address: keelbox status %s", cfg.Box)
	}
	srv, err := hetzner.Find(ctx, newHetznerClient(tok), cfg.Box)
	if err != nil {
		return addr{}, fmt.Errorf("finding it in the Hetzner project: %v", err)
	}
	return addr{srv.PublicNet.IPv4.IP, cfg.Target.SSH.Port}, nil
}

func parkedRow(box string, since time.Time) statusAllRow {
	return statusAllRow{Reach: reachParked, Parked: &since, Error: stateParked + " since " + day(since) + ": keelbox resume " + box}
}

// statusAllParked asks the box's cloud, once it has said there is no server,
// whether the box is parked: it may have been parked from another
// computer. Like statusAllPublic it uses a credential that needs no prompt.
func statusAllParked(ctx context.Context, cfg *config.Config) (time.Time, bool) {
	cl, err := openCloud(ctx, cfg, options{}, noPrompt(), io.Discard)
	if err != nil {
		return time.Time{}, false
	}
	p, err := cl.parked(ctx)
	if err != nil || p == nil {
		return time.Time{}, false
	}
	_ = rememberParked(cfg, cfg.Box, p)
	return p.since, true
}

func noSnapshot(box string) statusAllRow {
	return statusAllRow{Reach: reachError, Error: "no status snapshot yet; to write one: keelbox apply " + box}
}

func parsedRow(box string, data []byte) statusAllRow {
	s, err := snapshot.Parse(data)
	if err != nil {
		return statusAllRow{Reach: reachError, Error: err.Error()}
	}
	return statusAllRow{Reach: reachOK, Status: &s}
}

// unreachable says why ssh did not reach the box, from ssh's own message.
func unreachable(ctx context.Context, err error, box string) string {
	m := err.Error()
	why := "ssh failed"
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded), strings.Contains(m, "timed out"):
		why = "ssh timed out (is it off or asleep?)"
	case strings.Contains(m, "Connection refused"):
		why = "ssh refused the connection"
	case strings.Contains(m, "No route to host"), strings.Contains(m, "Network is unreachable"):
		why = "no route to the box (is it off, or this machine off its network?)"
	case strings.Contains(m, "Could not resolve"), strings.Contains(m, "Name or service not known"):
		why = "its name does not resolve"
	case strings.Contains(m, "Host key verification failed"):
		why = "its host key is not the pinned one"
	case strings.Contains(m, "Permission denied"):
		why = "the admin key was refused"
	}
	return "unreachable: " + why + "; keelbox ssh " + box
}

// reachWord is what the REACH column says for a box that gave no status,
// in the words of state.go.
var reachWord = map[string]string{
	reachParked:      stateParked,
	reachDestroyed:   stateDestroyed,
	reachUnreachable: stateUnreachable,
	reachError:       "no status",
}

// printStatusAllStyled is the table of every box, the reach and doctor cells
// marked on a styled terminal; the words stay the same. A box that gave no
// status has its state in the REACH column and its reason, with the
// command that moves it on, on a line of its own under the table, so the
// table keeps its columns whatever a reason's length.
func printStatusAllStyled(out io.Writer, st ui.Style, rows []statusAllRow, now time.Time) {
	counts := map[string]int{}
	var stale []string
	var table [][]string
	var notes [][2]string
	for _, r := range rows {
		counts[r.Reach]++
		if r.Status == nil || r.Reach == reachParked || r.Reach == reachDestroyed {
			word := reachWord[r.Reach]
			switch r.Reach {
			case reachParked, reachDestroyed:
				word = st.Dim(word)
			default:
				word = st.Bad(word)
			}
			table = append(table, []string{st.Bold(plain.Line(r.Box)), word, "-", "-", "-", "-", "-", "-", "-"})
			notes = append(notes, [2]string{plain.Line(r.Box), plain.Line(r.Error)})
			continue
		}
		if r.Reach == reachStale {
			stale = append(stale, r.Box)
		}
		s := r.Status
		reach := r.Reach
		if r.Reach == reachStale {
			reach = stateStale + " (" + s.Written.Local().Format("15:04") + ")"
		}
		// A box's snapshot is its own to write: every cell is printed plain.
		cells := plain.Lines([]string{r.Box, reach, dash(s.Release), doctorCell(s.Doctor),
			sessionsCell(s.Sessions), updatesCell(s.Updates), diskCell(s.Disk), backupCell(s.Backup, now)})
		cells[0] = st.Bold(cells[0])
		switch r.Reach {
		case reachOK:
			cells[1] = st.Good(cells[1])
		case reachStale:
			cells[1] = st.Warn(cells[1])
		default:
			cells[1] = st.Bad(cells[1])
		}
		switch d := s.Doctor; {
		case d == nil:
		case d.Failures > 0:
			cells[3] = st.Bad(cells[3])
		case d.Warnings > 0:
			cells[3] = st.Warn(cells[3])
		default:
			cells[3] = st.Good(cells[3])
		}
		table = append(table, cells)
	}
	header := []string{"BOX", "REACH", "RELEASE", "DOCTOR", "SESSIONS", "UPDATES", "DISK", "BACKUP"}
	if t := st.Table(header, table); st.Width <= 0 || fits(t, st.Cols()) {
		fmt.Fprint(out, t)
	} else {
		fmt.Fprint(out, statusAllStacked(st, header, table))
	}
	var parts []string
	for _, k := range []string{reachOK, reachStale, reachParked, reachDestroyed, reachUnreachable, reachError} {
		if counts[k] > 0 {
			word := k
			if w, ok := reachWord[k]; ok {
				word = w
			}
			parts = append(parts, fmt.Sprintf("%d %s", counts[k], word))
		}
	}
	line := func(text string) {
		if st.Live {
			text = paintCommands(st, text)
		}
		if st.Width > 0 {
			text = ui.Hang(text, st.Cols()-2, 2)
		}
		fmt.Fprintln(out, text)
	}
	line(fmt.Sprintf("%s: %s.", plural(len(rows), "box", "boxes"), strings.Join(parts, ", ")))
	for _, n := range notes {
		line(n[0] + ": " + n[1])
	}
	for _, b := range stale {
		line(fmt.Sprintf("%s: the snapshot is over 10 minutes old, so its watch run has stopped; keelbox doctor %s says why.", b, b))
	}
	for _, r := range rows {
		if r.Status != nil && r.Status.RequestsWaiting > 0 {
			fmt.Fprintf(out, "%s: %s for approval: keelbox requests %s\n", plain.Line(r.Box),
				plural(r.Status.RequestsWaiting, "package request of the agent's waits", "package requests of the agent's wait"), plain.Line(r.Box))
		}
	}
}

// fits reports whether every line of text shows within cols columns.
func fits(text string, cols int) bool {
	for _, l := range strings.Split(text, "\n") {
		if ui.Width(l) > cols {
			return false
		}
	}
	return true
}

// statusAllStacked is the table for a terminal too narrow for its columns: a
// line for each box with how it was reached, then the cells that say
// something, one a line under their column's name.
func statusAllStacked(st ui.Style, header []string, table [][]string) string {
	var b strings.Builder
	for _, r := range table {
		b.WriteString(r[0] + "  " + r[1] + "\n")
		for i := 2; i < len(r) && i < len(header); i++ {
			if r[i] == "-" {
				continue
			}
			b.WriteString("  " + ui.Pad(st.Dim(strings.ToLower(header[i])), 10) + ui.Hang(r[i], st.Cols()-12, 12) + "\n")
		}
	}
	return b.String()
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func doctorCell(d *snapshot.Doctor) string {
	if d == nil {
		return "-"
	}
	at := d.Time.Local().Format("15:04")
	switch {
	case d.Failures > 0 && d.Warnings > 0:
		return fmt.Sprintf("%s, %s (%s)", plural(d.Failures, "failure", "failures"), plural(d.Warnings, "warning", "warnings"), at)
	case d.Failures > 0:
		return fmt.Sprintf("%s (%s)", plural(d.Failures, "failure", "failures"), at)
	case d.Warnings > 0:
		return fmt.Sprintf("%s (%s)", plural(d.Warnings, "warning", "warnings"), at)
	}
	return "ok (" + at + ")"
}

func sessionsCell(s *snapshot.Sessions) string {
	if s == nil {
		return "-"
	}
	var out []string
	switch {
	case !s.Tmux:
		out = append(out, "tmux down")
	case len(s.Names) == 0:
		out = append(out, "tmux")
	default:
		names := append([]string(nil), s.Names...)
		sort.Strings(names)
		first := names[0]
		for _, n := range names {
			if n == "main" {
				first = n
			}
		}
		c := first
		if len(names) > 1 {
			c += fmt.Sprintf("+%d", len(names)-1)
		}
		out = append(out, c)
	}
	switch s.RemoteControl {
	case "":
	case "running":
		out = append(out, "RC up")
	default:
		if strings.HasPrefix(s.RemoteControl, "waiting") {
			out = append(out, "RC needs sign-in")
		} else {
			out = append(out, "RC down")
		}
	}
	return strings.Join(out, ", ")
}

func updatesCell(u *snapshot.Updates) string {
	if u == nil {
		return "-"
	}
	var out []string
	if u.Security > 0 {
		out = append(out, fmt.Sprintf("%d security", u.Security))
	}
	if u.RebootRequired != nil {
		out = append(out, "reboot")
	}
	if len(out) == 0 {
		return "0"
	}
	return strings.Join(out, ", ")
}

func diskCell(ds []snapshot.Disk) string {
	if len(ds) == 0 {
		return "-"
	}
	max := ds[0]
	for _, d := range ds {
		if d.Percent > max.Percent {
			max = d
		}
	}
	if max.Mount != "/" {
		return fmt.Sprintf("%d%% %s", max.Percent, max.Mount)
	}
	return fmt.Sprintf("%d%%", max.Percent)
}

func backupCell(b *snapshot.Backup, now time.Time) string {
	switch {
	case b == nil:
		return "-"
	case b.LastFailed:
		return "failed"
	case b.LastSuccess == nil:
		return "never"
	}
	return ago(now.Sub(*b.LastSuccess))
}

func ago(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h ago", int(d.Hours()))
	}
	return fmt.Sprintf("%d d ago", int(d.Hours()/24))
}
