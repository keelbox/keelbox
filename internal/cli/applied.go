package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/keelbox/keelbox/internal/config"
)

// appliedNote is the keelbox this computer last applied to a box with,
// kept beside the box's config in boxes/<box>.applied.json. It is what
// lets self-update say which boxes are behind without reaching any. It is
// this computer's view: an apply from another computer is not in it.
type appliedNote struct {
	Keelbox string    `json:"keelbox"`
	At      time.Time `json:"at"`
}

func appliedNotePath(box string) string { return filepath.Join(boxesDir(), box+".applied.json") }

func loadAppliedNote(box string) (appliedNote, bool) {
	data, err := os.ReadFile(appliedNotePath(box))
	if err != nil {
		return appliedNote{}, false
	}
	var n appliedNote
	if json.Unmarshal(data, &n) != nil || n.Keelbox == "" {
		return appliedNote{}, false
	}
	return n, true
}

// saveAppliedNote records an apply that passed, for a box of this
// computer's registry.
func saveAppliedNote(box, keelbox string, now time.Time) {
	if _, err := os.Stat(boxConfigPath(box)); err != nil {
		return // a config by path, outside the registry: nothing lists it
	}
	data, err := json.Marshal(appliedNote{Keelbox: keelbox, At: now.UTC()})
	if err != nil {
		return
	}
	_ = os.WriteFile(appliedNotePath(box), append(data, '\n'), 0o600)
}

// behindBox is a box whose last apply from this computer was with another
// keelbox than the one here now.
type behindBox struct {
	name string
	// with is what it was applied with, or why that is not known.
	with string
	// next is the command that brings it level.
	next string
}

// boxesBehind lists the boxes of this computer's registry that keelbox
// have was not applied to: applied with another version, or with none this
// computer recorded, which is every box until its first apply with a
// keelbox that keeps the note. A destroyed box has no server to update, a
// box that is this machine is told of by self-update itself, and a parked
// one is applied by the resume.
func boxesBehind(have string) []behindBox {
	var out []behindBox
	for _, name := range registeredBoxes() {
		row := listRow(name)
		if row.provider == config.ProviderLocal || row.state == stateDestroyed || row.state == stateBroken {
			continue
		}
		n, ok := loadAppliedNote(name)
		if ok && n.Keelbox == have {
			continue
		}
		b := behindBox{name: name, with: "not recorded here", next: "keelbox update " + name}
		if ok {
			b.with = n.Keelbox
		}
		if row.state == stateParked {
			b.with += ", parked"
			b.next = "keelbox resume " + name
		}
		out = append(out, b)
	}
	return out
}

// printBoxesBehind says which boxes have not had this keelbox applied, as
// far as this computer knows, and the command for each.
func printBoxesBehind(stdout io.Writer, have string) {
	behind := boxesBehind(have)
	if len(behind) == 0 {
		return
	}
	fmt.Fprintf(stdout, "\nNot yet applied with %s, as far as this computer knows:\n", have)
	nameW, withW := 0, 0
	for _, b := range behind {
		nameW, withW = max(nameW, len(b.name)), max(withW, len(b.with))
	}
	for _, b := range behind {
		fmt.Fprintln(stdout, strings.TrimRight(fmt.Sprintf("  %-*s  %-*s  %s", nameW, b.name, withW, b.with, b.next), " "))
	}
}
