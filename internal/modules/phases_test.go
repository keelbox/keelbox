package modules

import (
	"testing"

	"github.com/keelbox/keelbox/internal/ui"
)

// Every module is in one of the phases an apply shows. A module left out
// is drawn as a phase of its own, under its internal name.
func TestEveryModuleIsInAPhase(t *testing.T) {
	in := map[string]bool{}
	for _, p := range ui.Phases {
		for _, m := range p.Modules {
			in[m] = true
		}
	}
	for _, m := range All() {
		if !in[m.Name()] {
			t.Errorf("module %q is in no phase of ui.Phases", m.Name())
		}
	}
}
