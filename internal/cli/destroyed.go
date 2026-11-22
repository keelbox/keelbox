package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/prompt"
)

// keelbox destroy deletes a cloud box's server and keeps its config, so
// that keelbox up can create the box again. This computer notes that the
// server is gone, beside the config, so that keelbox ls, keelbox status
// --all and the question "Which box?" can say so without asking the cloud.
// Like the parked note it is never the authority: a command that finds a
// server deletes it.

// destroyedMark is this computer's note that keelbox destroy deleted a
// box's server.
type destroyedMark struct {
	At time.Time `json:"at"`
}

func destroyedMarkPath(box string) string { return filepath.Join(boxesDir(), box+".destroyed.json") }

func loadDestroyedMark(box string) (destroyedMark, bool) {
	data, err := os.ReadFile(destroyedMarkPath(box))
	if err != nil {
		return destroyedMark{}, false
	}
	var m destroyedMark
	if json.Unmarshal(data, &m) != nil || m.At.IsZero() {
		return destroyedMark{}, false
	}
	return m, true
}

func saveDestroyedMark(box string, m destroyedMark) error {
	if err := os.MkdirAll(boxesDir(), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(destroyedMarkPath(box), append(data, '\n'), 0o600)
}

func forgetDestroyedMark(box string) { _ = os.Remove(destroyedMarkPath(box)) }

// destroyedStatus is a destroyed box's status in keelbox ls and keelbox
// status --all: when, and the two ways on.
func destroyedStatus(box string, at time.Time) string {
	return "server destroyed " + day(at) + ": keelbox up " + box + " or keelbox forget " + box
}

// stdoutIsTerminal reports whether a person reads out as it is written;
// tests replace it.
var stdoutIsTerminal = func(out io.Writer) bool {
	f, ok := out.(*os.File)
	return ok && prompt.IsTerminal(f)
}

// noteDestroyed is the end of a keelbox destroy that deleted the server of
// a box in the registry: it notes here that the server is gone and, on a
// terminal, says that the config is kept and what to do with it.
func noteDestroyed(cfg *config.Config, o options, stdout, stderr io.Writer) {
	box := cfg.Box
	if boxRef(cfg, o.configFile) != box || !isRegistered(box) {
		return // a config file of the user's own: the registry has nothing to note
	}
	if err := saveDestroyedMark(box, destroyedMark{At: time.Now().UTC()}); err != nil {
		notef(stderr, "warning: could not note here that the server is destroyed: %v", err)
	}
	if stdoutIsTerminal(stdout) {
		fmt.Fprintf(stdout, "keelbox: %s's config is kept: keelbox up %s creates the server again, keelbox forget %s removes the box from keelbox.\n", box, box, box)
	}
}
