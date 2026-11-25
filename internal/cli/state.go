package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/keelbox/keelbox/internal/config"
)

// upState is how far keelbox up has taken a box, kept beside its config in
// boxes/<box>.up.json so a second keelbox up continues where the first
// stopped. It holds no secrets.
type upState struct {
	Via   string `json:"via"`   // how the box is reached: hetzner, aws, digitalocean, gcp, ssh, cloud-init or local
	Stage string `json:"stage"` // the last stage completed
	// Address is the server's public address, once known.
	Address string `json:"address,omitempty"`
	// Bootstrap is the account the first apply connects as.
	Bootstrap string `json:"bootstrap_user,omitempty"`
	// SudoPassword is set when the bootstrap account's sudo asks for a
	// password: its applies run under sudo on the terminal (ADR 0049).
	SudoPassword bool `json:"sudo_password,omitempty"`
	// HostKey is the public half of the single-use host key in a
	// cloud-config, pinned before the first connection.
	HostKey string `json:"host_key,omitempty"`
	// Pending holds a cloud-config box's answers until its server's
	// address is known and its config can be written.
	Pending *config.Starter `json:"pending,omitempty"`
	// Applying is set once the first apply has started, so a box stopped
	// part-way is told from one not started.
	Applying bool `json:"applying,omitempty"`
	// Left are the sign-ins skipped or not finished at the sign-in step,
	// by name (tailscale, claude-code, codex, gh); keelbox login removes
	// the ones it signs in.
	Left []string `json:"left,omitempty"`
	// Adopt is set when the owner accepted, at the go-ahead, what the
	// first apply would cut off or replace on a machine in use: the first
	// apply then runs with --adopt (ADR 0038).
	Adopt bool `json:"adopt,omitempty"`
	// OfferRemoteControl is set when up asks about Claude Code Remote
	// Control after the Claude Code sign-in rather than with the other
	// choices (ADR 0061); RemoteControlNext once the owner said yes and the
	// last apply is to start it.
	OfferRemoteControl bool `json:"offer_remote_control,omitempty"`
	RemoteControlNext  bool `json:"remote_control_next,omitempty"`
	// Answers are the answers the box was configured with, as the flags
	// that give them (no secrets: a token is never one), kept until its
	// cloud server exists: a box the cloud would not create a server for
	// moves to another cloud with them (docs/adr/0095-hetzner-refusals.md).
	Answers map[string]string `json:"answers,omitempty"`
	Updated time.Time         `json:"updated"`
}

// Ways a box is reached.
const (
	viaHetzner   = "hetzner"
	viaAWS       = "aws"
	viaDO        = "digitalocean"
	viaGCP       = "gcp"
	viaSSH       = "ssh"
	viaCloudInit = "cloud-init"
	viaLocal     = "local"
)

// Stages, in order.
const (
	stageConfigured = "configured" // config written
	stageWaiting    = "waiting"    // cloud-config printed; the user creates the server
	stageServer     = "server"     // server exists and answers SSH
	stageApplied    = "applied"    // first apply done, admin proven
	stageSignedIn   = "signed-in"  // keelbox login finished
	stageReady      = "ready"      // applied again after sign-in
)

var stageOrder = []string{stageConfigured, stageWaiting, stageServer, stageApplied, stageSignedIn, stageReady}

func stageIndex(s string) int {
	for i, x := range stageOrder {
		if x == s {
			return i
		}
	}
	return -1
}

// done reports whether stage s has been completed.
func (st upState) done(s string) bool { return stageIndex(st.Stage) >= stageIndex(s) }

// A box's state has one word, or a few, and every surface uses the same:
// keelbox ls, the start screen, the question "Which box?", keelbox status,
// keelbox status --all and keelbox doctor. Each is followed, where there is
// one, by the command that moves the box on.
//
//	ready                     set up; keelbox up has nothing left to do
//	ready, sign-ins left      set up, with sign-ins skipped: keelbox login BOX
//	not created               a cloud box whose server keelbox up has not created
//	waiting for the server    a cloud-config was printed; the user creates the server
//	not applied               configured, or the server exists; the first apply has not run
//	stopped part-way          the first apply started and did not finish
//	needs sign-in             applied; the sign-ins are left
//	signed in, apply pending  signed in; the closing apply is left
//	parked                    no server; its disk is a snapshot: keelbox resume BOX
//	server destroyed          keelbox destroy deleted the server: keelbox up BOX, or keelbox forget BOX
//	config error              the config does not load: keelbox plan BOX
//
// The first eight are how far keelbox up got (upState.describe); every one
// but "ready" has "keelbox up BOX" as its command. What a box that was
// asked says about itself has two more words, in keelbox status --all:
//
//	unreachable               ssh did not reach it; the reason follows
//	stale                     its status snapshot is over ten minutes old
//
// A box's health is counted in doctor's words everywhere: "ok", "2
// warnings", "1 failure".
const (
	stateReady        = "ready"
	stateSignInsLeft  = "ready, sign-ins left"
	stateNotCreated   = "not created"
	waitingForServer  = "waiting for the server"
	stateNotApplied   = "not applied"
	statePartWay      = "stopped part-way"
	stateNeedsSignIn  = "needs sign-in"
	stateApplyPending = "signed in, apply pending"
	stateParked       = "parked"
	stateDestroyed    = "server destroyed"
	stateBroken       = "config error"
	stateUnreachable  = "unreachable"
	stateStale        = "stale"
)

// notSetUpYet is what every command but keelbox up answers for such a box.
func notSetUpYet(box string) error {
	return fmt.Errorf("%s is not set up yet (%s): keelbox up %s", box, waitingForServer, box)
}

// describe is the state for keelbox ls, with the command that continues.
func (st upState) describe(box string) string {
	switch st.Stage {
	case stageConfigured, stageServer:
		switch {
		case st.Applying:
			return statePartWay + ": keelbox up " + box
		case isCloud(st.Via) && st.Stage == stageConfigured:
			return stateNotCreated + ": keelbox up " + box
		}
		return stateNotApplied + ": keelbox up " + box
	case stageWaiting:
		return waitingForServer + ": keelbox up " + box
	case stageApplied:
		return stateNeedsSignIn + ": keelbox up " + box
	case stageSignedIn:
		return stateApplyPending + ": keelbox up " + box
	case stageReady:
		if len(st.Left) > 0 {
			return stateSignInsLeft + " (" + strings.Join(st.Left, ", ") + "): keelbox login " + box
		}
		return stateReady
	}
	return st.Stage
}

// what is the state without the command that continues it, for a sentence
// that already names that command.
func (st upState) what(box string) string {
	s := st.describe(box)
	if i := strings.Index(s, ": keelbox "); i >= 0 {
		return s[:i]
	}
	return s
}

// noServer reports that up has no server for the box and has changed no
// machine: no address is known and the first apply never started. It is
// never true of a cloud box: this record cannot say what a create that was
// interrupted, or one run by hand, left in the cloud.
func (st upState) noServer() bool {
	return !isCloud(st.Via) && st.Address == "" && !st.Applying && !st.done(stageServer)
}

func upStatePath(box string) string { return filepath.Join(boxesDir(), box+".up.json") }

func loadUpState(cfg *config.Config) (upState, bool) {
	data, err := os.ReadFile(upStatePath(cfg.Box))
	if err != nil {
		return upState{}, false
	}
	var st upState
	if json.Unmarshal(data, &st) != nil || stageIndex(st.Stage) < 0 {
		return upState{}, false
	}
	return st, true
}

func saveUpState(box string, st upState) error {
	st.Updated = time.Now().UTC()
	if err := os.MkdirAll(boxesDir(), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(upStatePath(box), append(data, '\n'), 0o600)
}

func forgetUpState(box string) { _ = os.Remove(upStatePath(box)) }
