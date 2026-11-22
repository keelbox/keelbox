package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"time"

	"github.com/keelbox/keelbox/internal/previews"
	"github.com/keelbox/keelbox/internal/procnet"
	"github.com/keelbox/keelbox/internal/ui"
)

// exposeWait is how long expose waits for the root service to publish;
// exposeFirstCertWait when the box has no certificate yet, which Tailscale
// can take a minute to issue.
var exposeWait, exposeFirstCertWait = 30 * time.Second, 2 * time.Minute

// listeners are the box's TCP sockets as the caller sees them now; a test
// replaces it.
var listeners = procnet.All

// cmdExpose edits the agent's own previews file and waits for the root
// service to publish it. It runs on the box as the agent and needs no sudo:
// the service reads the file as the agent and validates every entry.
func cmdExpose(ctx context.Context, o options, pos []string, stdout, stderr io.Writer) int {
	fail := func(format string, args ...any) int {
		failf(stderr, format, args...)
		return 1
	}
	data, err := os.ReadFile(previews.StatusPath)
	if err != nil {
		return fail("previews are not set up on this machine (%s is missing). keelbox expose runs on a box, as the agent, "+
			"once keelbox apply has installed previews (previews.enabled). From a laptop: keelbox ssh, then keelbox expose PORT", previews.StatusPath)
	}
	st, err := previews.ParseStatus(data)
	if err != nil {
		return fail("read %s: %v", previews.StatusPath, err)
	}
	me, err := user.Current()
	if err != nil {
		return fail("%v", err)
	}
	if me.Username != st.Agent {
		return fail("previews belong to the agent account %s; run keelbox expose as %s (sudo -u %s -i keelbox expose ...)", st.Agent, st.Agent, st.Agent)
	}
	file := filepath.Join(me.HomeDir, previews.AgentFile)
	cur, err := readPreviews(file)
	if err != nil {
		return fail("%v", err)
	}

	switch {
	case o.list:
		if len(pos) > 0 {
			return fail("expose --list takes no port")
		}
		printPreviews(stdout, cur, st)
		return 0
	case o.remove != "":
		if len(pos) > 0 {
			return fail("expose --remove takes a name, not a port")
		}
		next, found := previews.Remove(cur, o.remove)
		if !found {
			return fail("no preview named %s (keelbox expose --list)", o.remove)
		}
		if _, err := publish(ctx, file, next, exposeWait); err != nil {
			return fail("%v", err)
		}
		fmt.Fprintf(stdout, "Stopped publishing %s.\n", o.remove)
		return 0
	}

	if len(pos) != 1 {
		return fail("usage: keelbox expose <port> [--name NAME]; keelbox expose --list; keelbox expose --remove NAME")
	}
	port, err := strconv.Atoi(pos[0])
	if err != nil || port < 1 || port > 65535 {
		return fail("%q is not a port number", pos[0])
	}
	// The status can be up to 10 minutes old: a box that has just joined
	// the tailnet goes ahead, and the service's own check below decides.
	if !st.Tailnet.Connected && !liveTailnet(ctx) {
		return fail("cannot publish a preview: %s.", previews.NotOnTailnet)
	}
	name := o.name
	if name == "" {
		name = strconv.Itoa(port)
	}
	next, p, err := previews.Assign(cur, name, port, st.Ports[0], st.Ports[1], st.Skip...)
	if err != nil {
		return fail("%v", err)
	}
	wait := exposeWait
	// A status from before the box joined the tailnet says nothing of
	// certificates either; only one that saw certificates off rules it out.
	if !st.Certificate && (st.Tailnet.HTTPS || !st.Tailnet.Connected) {
		wait = exposeFirstCertWait
		fmt.Fprintln(stdout, "Fetching the box's HTTPS certificate from Tailscale (first time only; it can take up to two minutes)...")
	}
	after, err := publish(ctx, file, next, wait)
	if err != nil {
		return fail("%v", err)
	}
	if !after.Tailnet.Connected {
		if _, err := writePreviews(file, cur); err != nil {
			return fail("%v", err)
		}
		return fail("cannot publish a preview: %s.", previews.NotOnTailnet)
	}
	if after.Error != "" {
		return fail("%s is recorded but not served: %s.", name, after.Error)
	}
	for _, pub := range after.Previews {
		if pub.Name != p.Name {
			continue
		}
		fmt.Fprintf(stdout, "%s: %s -> 127.0.0.1:%d\n", pub.Name, pub.URL, pub.Port)
		fmt.Fprintln(stdout, "Reachable only from devices on your tailnet that its ACLs let reach this box.")
		if !procnet.LoopbackListener(listeners(), port) {
			fmt.Fprintf(stdout, "Nothing listens on port %d yet: start the dev server on 127.0.0.1:%d.\n", port, port)
		}
		return 0
	}
	for _, prob := range after.Problems {
		notef(stderr, "%s", prob)
	}
	return fail("%s was not published; see the problems above", name)
}

func readPreviews(file string) (previews.File, error) {
	data, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return previews.File{Version: 1}, nil
	}
	if err != nil {
		return previews.File{}, err
	}
	var f previews.File
	ps, problems := previews.ParseFile(data, 1, 65535)
	if len(problems) > 0 && ps == nil {
		return f, fmt.Errorf("%s: %s; fix or remove the file", file, problems[0])
	}
	f.Version, f.Previews = 1, ps
	return f, nil
}

// publish writes the agent's file atomically and waits until the root
// service reports it has processed exactly this content.
func publish(ctx context.Context, file string, f previews.File, wait time.Duration) (previews.Status, error) {
	data, err := writePreviews(file, f)
	if err != nil {
		return previews.Status{}, err
	}
	want := previews.Hash(data)
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(previews.StatusPath); err == nil {
			if st, err := previews.ParseStatus(b); err == nil && st.Source == want {
				return st, nil
			}
		}
		select {
		case <-ctx.Done():
			return previews.Status{}, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return previews.Status{}, fmt.Errorf("the change is saved in %s, but the previews service did not pick it up within %s (systemctl status %s)",
		file, wait, previews.PathUnit)
}

// writePreviews replaces the agent's file atomically.
func writePreviews(file string, f previews.File) ([]byte, error) {
	data := f.Marshal()
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(file), ".previews-*.json")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	return data, os.Rename(tmp.Name(), file)
}

// liveTailnet asks tailscaled directly, which any account may.
func liveTailnet(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "tailscale", "status", "--json").Output()
	return err == nil && previews.ParseTailscaleStatus(string(out)).Connected
}

func printPreviews(out io.Writer, f previews.File, st previews.Status) {
	if st.Error != "" {
		fmt.Fprintf(out, "Not serving previews: %s.\n\n", st.Error)
	}
	if len(f.Previews) == 0 {
		fmt.Fprintln(out, "No previews. Publish one with keelbox expose PORT.")
		return
	}
	socks := listeners()
	published := map[string]previews.Published{}
	for _, p := range st.Previews {
		published[p.Name] = p
	}
	var table [][]string
	for _, p := range f.Previews {
		pub, ok := published[p.Name]
		state, url := "not served", "-"
		switch {
		// Whether something listens is read now, from the socket tables
		// (IPv4 and IPv6, so a dev server on [::1] counts), not from the
		// service's last run, which may predate the dev server.
		case ok && pub.Port == p.Port && procnet.LoopbackListener(socks, p.Port):
			state, url = "listening", pub.URL
		case ok && pub.Port == p.Port:
			state, url = "nothing on the port", pub.URL
		case st.Error == "":
			state = "pending"
		}
		table = append(table, []string{p.Name, strconv.Itoa(p.Port), url, state})
	}
	fmt.Fprint(out, ui.Style{}.Table([]string{"NAME", "PORT", "URL", "STATE"}, table))
}
