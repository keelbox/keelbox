package cli

import (
	"encoding/json"
	"io"
	"sort"
	"strings"
)

// listedBox is a box in keelbox ls --json: what this computer knows of it,
// reaching no box.
type listedBox struct {
	Box      string `json:"box"`
	Provider string `json:"provider"`
	Address  string `json:"address,omitempty"`
	Tailnet  bool   `json:"tailnet"`
	Status   string `json:"status"`
	// State is what stands in the way of using the box, such as "parked";
	// empty for a box that is set up.
	State string `json:"state,omitempty"`
}

// cmdListJSON is keelbox ls --json: the rows of keelbox ls, as a JSON array.
func cmdListJSON(stdout io.Writer) int {
	boxes := append(registeredBoxes(), pendingBoxes()...)
	sort.Strings(boxes)
	out := []listedBox{}
	for _, b := range boxes {
		r := listRow(b)
		addr, tailnet := strings.CutSuffix(r.address, " (tailnet)")
		if addr == "-" {
			addr = ""
		}
		out = append(out, listedBox{Box: r.name, Provider: r.provider, Address: addr, Tailnet: tailnet, Status: r.status, State: r.state})
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		return 1
	}
	return 0
}

// listedSession is a tmux session in keelbox sessions --json.
type listedSession struct {
	Name     string `json:"name"`
	Windows  int    `json:"windows"`
	Age      string `json:"age"`
	Attached bool   `json:"attached"`
}

func sessionsJSON(sessions []session, stdout io.Writer) int {
	out := []listedSession{}
	for _, s := range sessions {
		out = append(out, listedSession{Name: s.name, Windows: s.windows, Age: s.age, Attached: s.attached != ""})
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		return 1
	}
	return 0
}
