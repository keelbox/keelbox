package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// upDraft is what keelbox up has been told before it writes anything: the
// answers so far, by the flag that gives each. It is kept in
// ~/.config/keelbox/up-draft.json (mode 600, no secrets: a Hetzner token
// goes to the credential store, never here) so a check that fails, or
// Ctrl-C at a question, loses nothing: the next keelbox up offers to carry
// on with them. It goes once the box's config is written, or when the user
// says no at the summary.
type upDraft struct {
	Box     string            `json:"box,omitempty"`
	Answers map[string]string `json:"answers"`
	Updated time.Time         `json:"updated"`
}

func draftPath() string { return filepath.Join(configDir(), "up-draft.json") }

func loadDraft() (upDraft, bool) {
	data, err := os.ReadFile(draftPath())
	if err != nil {
		return upDraft{}, false
	}
	var d upDraft
	if json.Unmarshal(data, &d) != nil {
		return upDraft{}, false
	}
	if d.Answers["provider"] == viaLocal && localUnsupported() != nil {
		// Kept by a keelbox that offered this machine on a Mac: carrying
		// on with it would only be refused again, so the question is asked.
		delete(d.Answers, "provider")
	}
	if len(d.Answers) == 0 {
		return upDraft{}, false
	}
	return d, true
}

func (d *upDraft) save() error {
	d.Updated = time.Now().UTC()
	if err := os.MkdirAll(configDir(), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(draftPath(), append(data, '\n'), 0o600)
}

func forgetDraft() { _ = os.Remove(draftPath()) }

// describe says what the draft sets up, for the question that offers it.
func (d upDraft) describe() string {
	a := d.Answers
	var s string
	switch a["provider"] {
	case viaSSH:
		s = "an existing server"
		if h := a["host"]; h != "" {
			s += " at " + h
			if u := a["ssh-user"]; u != "" {
				s += ", as " + u
			}
		}
	case viaLocal:
		s = "this machine"
	case viaHetzner:
		s = "a new Hetzner server"
		if l := a["location"]; l != "" {
			s += " in " + l
		}
	case viaAWS:
		s = "a new AWS server"
		if l := a["location"]; l != "" {
			s += " in " + l
		}
	case viaDO:
		s = "a new DigitalOcean server"
		if l := a["location"]; l != "" {
			s += " in " + l
		}
	case viaGCP:
		s = "a new Google Cloud server"
		if l := a["location"]; l != "" {
			s += " in " + l
		}
	case viaCloudInit:
		s = "a server from a cloud-config"
	default:
		s = "a box"
	}
	if d.Box != "" {
		s = d.Box + ", " + s
	}
	return s
}

// remember keeps an answer in the draft, as the flag that would give it.
func (w *wizard) remember(flag, value string) {
	if w.draft == nil {
		return
	}
	w.draft.Answers[flag] = value
	if err := w.draft.save(); err != nil {
		fmt.Fprintf(w.errOut, "keelbox up: could not keep your answers in %s: %v\n", tildePath(draftPath()), err)
	}
}

// rememberBox keeps the box name in the draft.
func (w *wizard) rememberBox(name string) {
	if w.draft == nil {
		return
	}
	w.draft.Box = name
	w.remember("name", name)
}

// useDraft takes the draft's answers as if they were flags, except where a
// flag on the command line says otherwise.
func (w *wizard) useDraft(d upDraft) {
	saved := w.o
	fs := flagSet(&w.o, io.Discard) // defining the flags resets their fields
	w.o = saved
	for k, v := range d.Answers {
		if w.o.set[k] || fs.Lookup(k) == nil {
			continue
		}
		if fs.Set(k, v) == nil {
			w.o.set[k] = true
			if w.drafted == nil {
				w.drafted = map[string]bool{}
			}
			w.drafted[k] = true
		}
	}
	if d.Answers == nil {
		d.Answers = map[string]string{}
	}
	w.draft = &d
}

// draftAnswer is an answer kept in the draft that no flag gives.
func (w *wizard) draftAnswer(key string) (string, bool) {
	if w.draft == nil {
		return "", false
	}
	v, ok := w.draft.Answers[key]
	return v, ok
}
