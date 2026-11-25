package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/prompt"
	"github.com/keelbox/keelbox/internal/provider/hetzner"
	"github.com/keelbox/keelbox/internal/ui"
)

// When a cloud will not create the box's server, or change it, for a
// reason of its own (Hetzner limiting new servers for an account or a
// location, a type out of stock), keelbox says so with the cloud's own
// answer and offers the ways on: another location or size, another cloud,
// or the same again later (docs/adr/0095-hetzner-refusals.md).

// cloudRefusal is such a refusal. Nothing of the attempt is left but what
// err says.
type cloudRefusal struct {
	err error
	// location and size are what was asked for; locationKey and sizeKey
	// the config keys that hold them.
	location, size, locationKey, sizeKey string
	// otherLocations are where the same size can be ordered now, and
	// otherSizes the other sizes that can be ordered at location.
	otherLocations, otherSizes []string
	// locations are every location where some size can be ordered now, and
	// sizesAt lists the sizes at one, with the one to offer first: up's
	// questions after a refusal. Nil where the cloud lists none.
	locations []string
	sizesAt   func(location string) (choices []prompt.Choice, def int)
}

func (r *cloudRefusal) Error() string { return r.err.Error() }
func (r *cloudRefusal) Unwrap() error { return r.err }

// hetznerRefusal is a Hetzner refusal as a cloudRefusal; any other error is
// returned as it is.
func hetznerRefusal(err error) error {
	var re *hetzner.RefusedError
	if !errors.As(err, &re) {
		return err
	}
	types := re.Types
	r := &cloudRefusal{err: err, location: re.Location, size: re.ServerType,
		locationKey: "target.hetzner.location", sizeKey: "target.hetzner.server_type",
		otherLocations: re.OtherLocations, otherSizes: re.OtherTypes}
	if re.Action != "create" || len(types) == 0 {
		return r
	}
	r.locations = hetzner.OfferLocations(types)
	r.sizesAt = func(location string) ([]prompt.Choice, int) {
		offers := hetzner.Offers(types, location)
		var choices []prompt.Choice
		def := hetzner.DefaultOffer(offers)
		for i, o := range offers {
			if o.Type.Name == re.ServerType {
				def = i
			}
			choices = append(choices, prompt.Choice{Key: o.Type.Name, Label: o.Type.Name, Detail: o.Describe()})
		}
		return choices, def
	}
	return r
}

// some names a few of the names: "ash", "ash (or nbg1)", "a (or b, c)".
func some(names []string) string {
	if len(names) > 3 {
		names = names[:3]
	}
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return names[0] + " (or " + strings.Join(names[1:], ", ") + ")"
}

// otherClouds names the clouds keelbox creates servers on but this one.
func otherClouds(current string) []cloudEntry {
	var out []cloudEntry
	for _, e := range cloudEntries() {
		if e.via != current {
			out = append(out, e)
		}
	}
	return out
}

// configFix is the end of a refusal for a command that reads the box's
// config, path: the keys to change to try elsewhere, also (a sentence, or
// empty), then retry, the command that runs it again.
func (r *cloudRefusal) configFix(path, retry, also string) string {
	if also != "" {
		also += ". "
	}
	var keys []string
	if len(r.otherLocations) > 0 {
		keys = append(keys, r.locationKey+" to "+some(r.otherLocations))
	}
	if len(r.otherSizes) > 0 {
		keys = append(keys, r.sizeKey+" to "+some(r.otherSizes))
	}
	if len(keys) == 0 {
		return "Nothing else can be ordered there now. " + also + "Try again later: " + retry
	}
	return fmt.Sprintf("To try elsewhere, set %s in %s, then: %s. %sOr try again later: %s", strings.Join(keys, ", or "), tildePath(path), retry, also, retry)
}

// upFix is the end of a refusal when keelbox up created the box's config
// and has no terminal to ask on: a command for each way on. yes is added
// where up would ask for a go-ahead again.
func (r *cloudRefusal) upFix(box, provider, extra string) string {
	up := "keelbox up " + box
	var ways []string
	if len(r.otherLocations) > 0 {
		ways = append(ways, "another location: "+up+" --location "+some(r.otherLocations)+extra)
	}
	if len(r.otherSizes) > 0 {
		ways = append(ways, "another size: "+up+" --size "+some(r.otherSizes)+extra)
	}
	var names []string
	for _, e := range otherClouds(provider)[1:] {
		names = append(names, e.via)
	}
	ways = append(ways, "another cloud, with your other answers kept: "+up+" --provider "+otherClouds(provider)[0].via+" --yes"+extra+" (or "+strings.Join(names, ", ")+")")
	return "Try " + strings.Join(ways, "; ") + "; or the same again later: " + up + extra
}

// notCreated reports whether keelbox up wrote the cloud box's config and
// has created no server for it yet, nor started to: where it is created
// may still change.
func notCreated(cfg *config.Config) bool {
	st, ok := loadUpState(cfg)
	return ok && isCloud(st.Via) && isCloud(cfg.Target.Provider) && st.Stage == stageConfigured && !st.Applying && st.Address == ""
}

// cloudAnswers are the answers that hold for one cloud only: they are not
// carried to another.
var cloudAnswers = map[string]bool{
	"provider": true, "location": true, "size": true, "token-file": true, "hcloud-context": true,
	"profile": true, "doctl-context": true, "gcp-project": true,
}

// keptAnswers are this run's answers as the flags that give them: the
// draft's and the command line's. A box whose server is not created yet
// keeps them, so it can move to another cloud without asking them again.
func (w *wizard) keptAnswers() map[string]string {
	out := map[string]string{}
	if w.draft != nil {
		for k, v := range w.draft.Answers {
			out[k] = v
		}
	}
	o := w.o
	fs := flagSet(&o, io.Discard) // defining the flags resets their fields
	o = w.o
	fs.VisitAll(func(f *flag.Flag) {
		if w.o.set[f.Name] && f.Name != "yes" && f.Name != "name" {
			out[f.Name] = f.Value.String()
		}
	})
	return out
}

// placeAgain takes --location and --size for a Hetzner box whose server is
// not created yet: they change where it is created. Elsewhere they change
// nothing, and ignoredFlags says so.
func (w *wizard) placeAgain(cfg *config.Config) error {
	if cfg.Target.Provider != config.ProviderHetzner {
		return nil
	}
	path := boxConfigPath(cfg.Box)
	for _, f := range []struct{ flag, key, value string }{
		{"location", "target.hetzner.location", w.o.location},
		{"size", "target.hetzner.server_type", w.o.size},
	} {
		if !w.o.set[f.flag] {
			continue
		}
		if err := w.setPlace(cfg, path, f.key, f.value); err != nil {
			return fmt.Errorf("--%s %q: %v; set %s in %s yourself, then: keelbox up %s", f.flag, f.value, err, f.key, tildePath(path), cfg.Box)
		}
		delete(w.o.set, f.flag) // taken: not one of the flags that change nothing
	}
	return nil
}

// setPlace sets one key of where the box's server is created, and says so.
func (w *wizard) setPlace(cfg *config.Config, path, key, value string) error {
	changed, err := setConfigKey(cfg, path, key, value)
	if err == nil && changed {
		w.logf("set %s: %s in %s", key, value, tildePath(path))
	}
	return err
}

// movingCloud is the user choosing, after a refusal, to create the box on
// another cloud.
type movingCloud struct{ via string }

func (e *movingCloud) Error() string { return "moving to " + e.via }

// afterRefusal is up's answer to a cloud refusing the box's server. On a
// terminal it says what the cloud said and asks how to go on: another
// location or size changes the config and returns nil, to try again;
// another cloud returns movingCloud, stop errStopHere. Without one it
// returns the refusal with a command for each way on.
func (w *wizard) afterRefusal(ctx context.Context, cfg *config.Config, r *cloudRefusal) error {
	box, path := cfg.Box, boxConfigPath(cfg.Box)
	if !w.p.Interactive() {
		extra := ""
		if w.o.skipSignIn {
			extra = " --skip-sign-in"
		}
		return fmt.Errorf("%w. %s", r, r.upFix(box, cfg.Target.Provider, extra))
	}
	trouble(w.errOut, ui.Trouble{Severity: ui.NeedsYou, Text: r.Error(), Prefix: "keelbox up: "})
	// Nothing changes while up asks: Ctrl-C ends the question.
	w.changing = false
	setInterruptNote("")
	defer func() { w.changing = true; setInterruptNote(stepNote) }()
	var choices []prompt.Choice
	var ways []string
	var others []string
	for _, l := range r.locations {
		if l != r.location {
			others = append(others, l)
		}
	}
	if len(others) > 0 && r.sizesAt != nil {
		d := "where sizes can be ordered now: " + strings.Join(others, ", ")
		if len(r.otherLocations) > 0 {
			d = r.size + " can be ordered in " + strings.Join(r.otherLocations, ", ")
		}
		choices, ways = append(choices, prompt.Choice{Key: "location", Label: "Another location", Detail: d}), append(ways, "location")
	}
	if len(r.otherSizes) > 0 && r.sizesAt != nil {
		choices = append(choices, prompt.Choice{Key: "size", Label: "Another size in " + r.location, Detail: some(r.otherSizes) + " can be ordered there"})
		ways = append(ways, "size")
	}
	var names []string
	for _, e := range otherClouds(cfg.Target.Provider) {
		names = append(names, e.name)
	}
	choices = append(choices,
		prompt.Choice{Key: "cloud", Label: "Another cloud", Detail: orList(names) + "; your other answers are kept"},
		prompt.Choice{Key: "stop", Label: "Stop here", Detail: "nothing was created; keelbox up " + box + " tries the same again"})
	ways = append(ways, "cloud", "stop")
	i, err := w.p.Choose("How should keelbox up go on?", choices, 0)
	if err != nil {
		return err
	}
	switch ways[i] {
	case "location":
		lc := make([]prompt.Choice, len(others))
		for j, l := range others {
			lc[j] = prompt.Choice{Key: l, Label: l}
			if contains(r.otherLocations, l) {
				lc[j].Detail = r.size + " can be ordered there"
			}
		}
		j := 0
		if len(lc) > 1 {
			var err error
			if j, err = w.p.Choose("Which location?", lc, 0); err != nil {
				return err
			}
		}
		loc, size := others[j], r.size
		if !contains(r.otherLocations, loc) {
			sc, def := r.sizesAt(loc)
			w.hint(r.size + " cannot be ordered in " + loc + " now, so the box needs another size there.")
			k, err := w.p.Choose("Which size in "+loc+"?", sc, def)
			if err != nil {
				return err
			}
			size = sc[k].Key
		}
		if err := w.setPlace(cfg, path, r.locationKey, loc); err != nil {
			return fmt.Errorf("%v; set %s: %s in %s yourself, then: keelbox up %s", err, r.locationKey, loc, tildePath(path), box)
		}
		if err := w.setPlace(cfg, path, r.sizeKey, size); err != nil {
			return fmt.Errorf("%v; set %s: %s in %s yourself, then: keelbox up %s", err, r.sizeKey, size, tildePath(path), box)
		}
	case "size":
		sc, _ := r.sizesAt(r.location)
		var keep []prompt.Choice
		for _, c := range sc {
			if c.Key != r.size {
				keep = append(keep, c)
			}
		}
		k, err := w.p.Choose("Which size?", keep, 0)
		if err != nil {
			return err
		}
		if err := w.setPlace(cfg, path, r.sizeKey, keep[k].Key); err != nil {
			return fmt.Errorf("%v; set %s: %s in %s yourself, then: keelbox up %s", err, r.sizeKey, keep[k].Key, tildePath(path), box)
		}
	case "cloud":
		entries := otherClouds(cfg.Target.Provider)
		cc := make([]prompt.Choice, len(entries))
		def := -1
		for j, e := range entries {
			detail, ready := e.needs(ctx, w.o)
			cc[j] = prompt.Choice{Key: e.via, Label: e.name, Detail: detail}
			if ready && def < 0 {
				def = j
			}
		}
		j, err := w.p.Choose("Which cloud?", cc, max(def, 0))
		if err != nil {
			return err
		}
		return &movingCloud{entries[j].via}
	default:
		return errStopHere
	}
	return nil
}

// moveCloud configures a box whose server was not created on another
// cloud, the one --provider names: the answers that hold for any cloud
// are kept, the cloud's own are asked (or taken from the flags), and the
// go-ahead is asked again. The config and up's progress are written anew.
func (w *wizard) moveCloud(ctx context.Context, name string, cfg *config.Config) (int, error) {
	st, _ := loadUpState(cfg)
	d := upDraft{Box: name, Answers: map[string]string{}}
	for k, v := range st.Answers {
		if !cloudAnswers[k] {
			d.Answers[k] = v
		}
	}
	w.useDraft(d) // the command line's flags win
	w.logf("moving %s from %s to %s; nothing was created on %s", name, cfg.Target.Provider, w.o.provider, cfg.Target.Provider)
	next, nst, proceed, err := w.configure(ctx, name)
	if err != nil {
		// The answers stay in the box's progress, not the draft: the
		// error's own command, with --provider, carries on with them.
		forgetDraft()
		w.draft = nil
	}
	var needs *needsYouError
	switch {
	case errors.As(err, &needs):
		return exitNeedsYou, err
	case err != nil:
		return 2, err
	case !proceed:
		// The user said no: the box stays where it was configured.
		forgetDraft()
		w.draft = nil
		w.hint(fmt.Sprintf("%s stays on %s. To move it later: keelbox up %s --provider %s", name, cfg.Target.Provider, name, w.o.provider))
		return 0, nil
	}
	w.fresh = true
	return w.carryOn(ctx, next, nst)
}

// carryOn is resume, and, when the user chose another cloud after a
// refusal, the move there.
func (w *wizard) carryOn(ctx context.Context, cfg *config.Config, st upState) (int, error) {
	code, err := w.resume(ctx, cfg, st)
	var mv *movingCloud
	if errors.As(err, &mv) {
		w.o.provider = mv.via
		if w.o.set == nil {
			w.o.set = map[string]bool{}
		}
		w.o.set["provider"] = true
		return w.moveCloud(ctx, cfg.Box, cfg)
	}
	return code, err
}
