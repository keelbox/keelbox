package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/config"
)

// keeperCloud is a cloud that needs a file on the box before its disk
// boots on another server.
type keeperCloud struct{ *hetznerCloud }

func (keeperCloud) hostKeysFile() (string, string) {
	return "/etc/keep-the-keys", "Without it a new server makes new SSH host keys, which this computer would refuse"
}

// resize and park ask the box for the file before anything changes, and
// stop with the apply that puts it there when it is missing or the box
// does not answer.
func TestResizeAndParkNeedTheHostKeysFileOnTheBox(t *testing.T) {
	old := boxHasFile
	t.Cleanup(func() { boxHasFile = old })
	for _, cmd := range []string{"park", "resize"} {
		w := newParkWorld(t)
		var asked []string
		has, askErr := false, error(nil)
		boxHasFile = func(_ context.Context, _ *config.Config, a addr, path string) (bool, error) {
			asked = append(asked, a.host+" "+path)
			return has, askErr
		}
		o := options{size: "cpx22", yes: true}
		code, out := w.doWith(t, keeperCloud{w.cl}, cmd, o, nil)
		want := "keelbox: nothing changed: acme has no /etc/keep-the-keys. Without it a new server makes new SSH host keys, which this computer would refuse. " +
			"Put it there: keelbox apply acme; then: keelbox " + cmd + " acme\n"
		if code != 1 || out != want || len(w.writes()) != 0 || len(asked) != 1 || asked[0] != "203.0.113.7 /etc/keep-the-keys" {
			t.Errorf("%s without the file: exit %d, %v, asked %v\n%s", cmd, code, w.writes(), asked, out)
		}
		askErr = errors.New("ssh admin@203.0.113.7: exit status 255")
		code, out = w.doWith(t, keeperCloud{w.cl}, cmd, o, nil)
		if code != 1 || len(w.writes()) != 0 || !strings.HasPrefix(out, "keelbox: nothing changed: acme could not be asked whether it has /etc/keep-the-keys (ssh admin@203.0.113.7: exit status 255). Without it") ||
			!strings.HasSuffix(out, "Put it there: keelbox apply acme; then: keelbox "+cmd+" acme\n") {
			t.Errorf("%s with a box that does not answer: exit %d\n%s", cmd, code, out)
		}
		// With the file the command goes ahead.
		has, askErr = true, nil
		if code, out := w.doWith(t, keeperCloud{w.cl}, cmd, o, nil); code != 0 || len(w.writes()) == 0 {
			t.Errorf("%s with the file: exit %d\n%s", cmd, code, out)
		}
		// A cloud that needs no file is not asked.
		w = newParkWorld(t)
		asked = nil
		if code, out := w.do(t, cmd, o, nil); code != 0 || len(asked) != 0 {
			t.Errorf("%s on a cloud that needs no file: exit %d, asked %v\n%s", cmd, code, asked, out)
		}
	}
}
