package cli

import (
	"bytes"
	"context"
	"testing"
)

// After a session that ended with exit status 0, keelbox attach says on a
// terminal that the box keeps running and how to come back. It goes by the
// exit status alone, and adds nothing after any other end.
func TestAttachSaysHowToComeBack(t *testing.T) {
	cfg := setUpBox(t)
	stub := stubSSH(t)
	attach := func(o options) (int, string, string) {
		var out, errOut bytes.Buffer
		code := cmdAttach(context.Background(), cfg, o, &out, &errOut)
		return code, out.String(), errOut.String()
	}

	onTerminal(t, true)
	stub.write("sessions-end", "0", 0o644)
	if code, out, errs := attach(options{}); code != 0 || out != "keelbox: lab keeps running whatever was left on it. Come back: keelbox attach lab\n" || errs != "" {
		t.Errorf("after a session: exit %d\n%s%s", code, out, errs)
	}
	// The command that comes back names the same session and config.
	if code, out, _ := attach(options{session: "review", configFile: "boxes/lab.yaml"}); code != 0 || out != "keelbox: lab keeps running whatever was left on it. Come back: keelbox attach boxes/lab.yaml --session review\n" {
		t.Errorf("after the session review: exit %d\n%s", code, out)
	}
	// A session that ended any other way: its exit status, and no line.
	stub.write("sessions-end", "3", 0o644)
	if code, out, errs := attach(options{}); code != 3 || out != "" || errs != "" {
		t.Errorf("a session that ended with 3: exit %d\n%s%s", code, out, errs)
	}
	// Not a terminal: no line.
	onTerminal(t, false)
	stub.write("sessions-end", "0", 0o644)
	if code, out, errs := attach(options{}); code != 0 || out != "" || errs != "" {
		t.Errorf("without a terminal: exit %d\n%s%s", code, out, errs)
	}
}
