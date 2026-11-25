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
	"github.com/keelbox/keelbox/internal/provider"
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
	// quota is set when the project is at one of the cloud's limits: no
	// other location or size changes that.
	quota bool
	// otherLocations are where the same size can be ordered now, and
	// otherSizes the other sizes that can be ordered at location, the
	// nearest to size first.
	otherLocations, otherSizes []string
	// locations are every location where some size can be ordered now;
	// sizesAt lists the sizes at one, with the one to offer first (size,
	// or the nearest to it). They are up's questions after a refusal, nil
	// where the cloud lists none.
	locations []string
	sizesAt   func(location string) (choices []prompt.Choice, def int)
	// price is a size's price at a location, "" where it is not sold or
	// the cloud lists no prices: the alternatives say what each costs.
	price func(size, location string) string
}

// priceAt is the price of the size asked for at location.
func (r *cloudRefusal) priceAt(location string) string { return r.price(r.size, location) }

// priced lists names, each with its price where price gives one.
func priced(names []string, price func(name string) string) string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = n
		if p := price(n); p != "" {
			out[i] += " at " + p
		}
	}
	return strings.Join(out, ", ")
}

// pricedLocations and pricedSizes are the other locations and sizes, each
// with what the box would cost there.
func (r *cloudRefusal) pricedLocations() string {
	return priced(r.otherLocations, func(l string) string { return r.price(r.size, l) })
}

func (r *cloudRefusal) pricedSizes() string {
	return priced(r.otherSizes, func(s string) string { return r.price(s, r.location) })
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
	r := &cloudRefusal{err: err, location: re.Location, size: re.ServerType, quota: re.Quota(),
		locationKey: "target.hetzner.location", sizeKey: "target.hetzner.server_type",
		otherLocations: re.OtherLocations, otherSizes: re.OtherTypes}
	r.price = func(size, location string) string {
		for _, o := range hetzner.Offers(types, location) {
			if o.Type.Name == size {
				return fmt.Sprintf("EUR %.2f/month", o.Monthly)
			}
		}
		return ""
	}
	if re.Action != "create" || len(types) == 0 {
		return r
	}
	like, _ := hetzner.TypeNamed(types, re.ServerType)
	r.locations = hetzner.OfferLocations(types)
	r.sizesAt = func(location string) ([]prompt.Choice, int) {
		offers := hetzner.Offers(types, location)
		def := hetzner.NearestOffer(offers, like)
		var choices []prompt.Choice
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

// exampleLocation is a location each cloud takes without a terminal, for
// the commands a refusal prints: up asks for none of these there.
var exampleLocation = map[string]string{
	config.ProviderAWS: "eu-central-1", config.ProviderDigitalOcean: "fra1", config.ProviderGCP: "europe-west3-a",
}

// moveCommands are the commands that move box to each other cloud
// without a terminal. None has --yes: each stops at the go-ahead, which
// prints what the box costs there and the command with --yes.
func moveCommands(box, provider, extra string) string {
	var cmds []string
	for _, e := range otherClouds(provider) {
		cmds = append(cmds, otherCloudCommand(box, e.via)+extra)
	}
	return orList(cmds)
}

// otherCloudCommand is keelbox up for box on the cloud via, as it runs
// without a terminal up to the go-ahead.
func otherCloudCommand(box, via string) string {
	c := "keelbox up " + box + " --provider " + via
	if l := exampleLocation[via]; l != "" {
		c += " --location " + l
	}
	return c
}

// configFix is the end of a refusal for a command that reads the box's
// config, path: the keys to change to try elsewhere, also (a sentence, or
// empty), then retry, the command that runs it again.
func (r *cloudRefusal) configFix(path, retry, also string) string {
	if also != "" {
		also += ". "
	}
	if r.quota {
		return also + "Once Hetzner has raised the limit, or a server is deleted: " + retry
	}
	var known, keys []string
	if len(r.otherLocations) > 0 {
		known = append(known, fmt.Sprintf("Other locations for %s now: %s.", r.size, r.pricedLocations()))
		keys = append(keys, r.locationKey+": "+r.otherLocations[0])
	}
	if len(r.otherSizes) > 0 {
		known = append(known, fmt.Sprintf("Other sizes in %s now: %s.", r.location, r.pricedSizes()))
		keys = append(keys, r.sizeKey+": "+r.otherSizes[0])
	}
	if len(keys) == 0 {
		return "Nothing else can be ordered there now. " + also + "Try again later: " + retry
	}
	return fmt.Sprintf("%s To try one, set %s in %s, then: %s. %sOr try again later: %s",
		strings.Join(known, " "), strings.Join(keys, " or "), tildePath(path), retry, also, retry)
}

// upFix is the end of a refusal when keelbox up created the box's config
// and has no terminal to ask on: a command for each way on, each as it
// runs. extra is added to each, as up was given it. At a new price, up
// asks for the go-ahead again (newPrice): the commands for another
// location or size say their price and carry --yes, which gives it;
// another cloud has no --yes and stops at that cloud's go-ahead.
func (r *cloudRefusal) upFix(box, provider, extra string) string {
	up := "keelbox up " + box
	move := "another cloud, with your other answers kept: " + moveCommands(box, provider, extra)
	if r.quota {
		return "The limit holds in every location. Try " + move + "; or, once Hetzner has raised the limit or a server is deleted: " + up + extra
	}
	var ways []string
	if len(r.otherLocations) > 0 {
		ways = append(ways, fmt.Sprintf("another location (%s): %s --location %s%s --yes", r.pricedLocations(), up, r.otherLocations[0], extra))
	}
	if len(r.otherSizes) > 0 {
		ways = append(ways, fmt.Sprintf("another size in %s (%s): %s --size %s%s --yes", r.location, r.pricedSizes(), up, r.otherSizes[0], extra))
	}
	ways = append(ways, move)
	return "Try " + strings.Join(ways, "; ") + "; or the same again later: " + up + extra
}

// notCreated reports whether keelbox up wrote the cloud box's config and
// has created no server for it yet, nor started to: where it is created
// may still change.
func notCreated(cfg *config.Config) bool {
	st, ok := loadUpState(cfg)
	return ok && isCloud(st.Via) && isCloud(cfg.Target.Provider) && st.Stage == stageConfigured && !st.Applying && st.Address == ""
}

// markOrdering marks a cloud box keelbox up configured, and has created
// no server for yet, as having its server ordered (on) or as not (off):
// keelbox create does, for a box up configured. It reports whether the
// box is such a box, and fails when the mark cannot be written.
func markOrdering(cfg *config.Config, on bool) (bool, error) {
	st, ok := loadUpState(cfg)
	if !ok || !isCloud(st.Via) || st.Stage != stageConfigured || st.Address != "" {
		return false, nil
	}
	if st.Applying != on {
		st.Applying = on
		if err := saveUpState(cfg.Box, st); err != nil {
			return true, err
		}
	}
	return true, nil
}

// recordServer notes in up's progress that the box up configured has its
// server at ip, as up does when it creates it: keelbox up carries on from
// there, and the answers kept for creating it elsewhere go.
func recordServer(cfg *config.Config, ip string) error {
	st, ok := loadUpState(cfg)
	if !ok {
		return nil
	}
	st.Address, st.Bootstrap, st.Stage, st.Applying, st.Answers = ip, bootstrapUser(st.Via), stageServer, true, nil
	return saveUpState(cfg.Box, st)
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
// nothing, and ignoredFlags says so. --provider naming the box's own cloud
// changes nothing either, and is taken without a word. Each is checked
// against Hetzner's lists before the config changes, and so is the price:
// one other than the go-ahead showed is asked about again (--yes without a
// terminal). It reports false when the user declines it.
func (w *wizard) placeAgain(ctx context.Context, cfg *config.Config) (bool, error) {
	if w.o.set["provider"] && w.o.provider == cfg.Target.Provider {
		delete(w.o.set, "provider")
	}
	if cfg.Target.Provider != config.ProviderHetzner || !w.o.set["location"] && !w.o.set["size"] {
		return true, nil
	}
	box, path, h := cfg.Box, boxConfigPath(cfg.Box), cfg.Target.Hetzner
	// Held to the end of the run: a keelbox create meanwhile would order
	// where the config no longer says.
	if err := w.lock(box); err != nil {
		return false, err
	}
	failed := func(err error) error {
		return fmt.Errorf("nothing changed; %v. Try again: %s", err, w.upCommand(nil))
	}
	cl, err := hetznerClient(ctx, cfg, w.o, w.p, w.out)
	if err != nil {
		return false, failed(err)
	}
	types, err := cl.ServerTypes(ctx)
	if err != nil {
		return false, failed(err)
	}
	var locNames []string
	if w.o.set["location"] {
		locs, err := cl.Locations(ctx)
		if err != nil {
			return false, failed(err)
		}
		for _, l := range locs {
			locNames = append(locNames, l.Name)
		}
	}
	loc, size, err := w.checkPlace(types, locNames, h)
	if err != nil {
		return false, err
	}
	var keys []string
	for _, f := range []struct{ flag, key, value string }{
		{"location", "target.hetzner.location", w.o.location},
		{"size", "target.hetzner.server_type", w.o.size},
	} {
		if w.o.set[f.flag] {
			keys = append(keys, f.key+": "+f.value)
		}
	}
	if err := w.noServerYet(ctx, cfg, w.upCommand(nil), "set "+strings.Join(keys, " and ")+" in "+tildePath(path)+", then: keelbox up "+box); err != nil {
		return false, err
	}
	if ok, err := w.newPrice(types, h.ServerType, h.Location, size, loc); !ok || err != nil {
		return false, err
	}
	for _, f := range []struct{ flag, key, value string }{
		{"location", "target.hetzner.location", w.o.location},
		{"size", "target.hetzner.server_type", w.o.size},
	} {
		if !w.o.set[f.flag] {
			continue
		}
		if err := w.setPlace(cfg, path, f.key, f.value); err != nil {
			return false, fmt.Errorf("--%s %q: %v; set %s in %s yourself, then: keelbox up %s", f.flag, f.value, err, f.key, tildePath(path), box)
		}
		delete(w.o.set, f.flag) // taken: not one of the flags that change nothing
	}
	return true, nil
}

// checkPlace checks --location against locNames, Hetzner's locations,
// and --size against its server types, and returns where the box would
// be created and as what. With either wrong, the error names each wrong
// flag and ends with an example that works as printed: the flags given,
// each wrong one replaced by one that can be ordered, never the place or
// size the box has now (it may be the one refused), or left out where
// there is none.
func (w *wizard) checkPlace(types []hetzner.ServerType, locNames []string, h config.Hetzner) (loc, size string, err error) {
	loc, size = h.Location, h.ServerType
	_, sizeKnown := hetzner.TypeNamed(types, w.o.size)
	badLoc := w.o.set["location"] && !contains(locNames, w.o.location)
	badSize := w.o.set["size"] && !sizeKnown
	if w.o.set["location"] && !badLoc {
		loc = w.o.location
	}
	if w.o.set["size"] && !badSize {
		size = w.o.size
	}
	if !badLoc && !badSize {
		return loc, size, nil
	}
	change := map[string]string{}
	var why []string
	if badLoc {
		// Where the size can be ordered first, then any other location.
		cands := hetzner.OrderableElsewhere(types, size, h.Location)
		for _, n := range locNames {
			if n != h.Location {
				cands = append(cands, n)
			}
		}
		change["location"] = firstNonEmpty(cands...)
		if change["location"] != "" {
			loc = change["location"]
		}
		why = append(why, fmt.Sprintf("--location %q is not a Hetzner location; use one of: %s", w.o.location, strings.Join(locNames, ", ")))
	}
	if badSize {
		var names, others []string
		for _, o := range hetzner.Offers(types, loc) {
			names = append(names, o.Type.Name)
			if o.Type.Name != h.ServerType {
				others = append(others, o.Type.Name)
			}
		}
		ex := firstNonEmpty(append(hetzner.NewRefusedError(nil, w.box, loc, h.ServerType, types, "").OtherTypes, others...)...)
		if ex == "" {
			// Nothing else there: the nearest type in another location
			// that has one.
			like, _ := hetzner.TypeNamed(types, h.ServerType)
			for _, l := range hetzner.OfferLocations(types) {
				if offers := hetzner.Offers(types, l); l != loc && len(offers) > 0 {
					change["location"], ex = l, hetzner.NearestOffers(offers, like)[0].Type.Name
					break
				}
			}
		}
		change["size"] = ex
		if len(names) > 0 {
			why = append(why, fmt.Sprintf("--size %q is not a Hetzner server type; use one that can be ordered in %s now: %s", w.o.size, loc, strings.Join(names, ", ")))
		} else {
			why = append(why, fmt.Sprintf("--size %q is not a Hetzner server type, and none can be ordered in %s now", w.o.size, loc))
		}
	}
	msg := strings.Join(why, "; ")
	if change["size"] == "" && badSize || change["location"] == "" && badLoc {
		// No example to give for a flag: the rest, or the same later.
		return "", "", fmt.Errorf("%s. Hetzner lists nothing else to order now; try again later: %s", msg, w.upCommand(change))
	}
	return "", "", fmt.Errorf("%s, for example: %s", msg, w.upCommand(change))
}

// newPrice says what the box costs as size in loc, against wasSize in
// wasLoc, which the go-ahead showed. It goes by what Create orders: any
// type Hetzner lists as available there, deprecated or not. The same
// price carries on; another, or one Hetzner's list does not give, is
// asked about on a terminal and needs --yes without one. A type not
// available there carries on: Create refuses it before any order. It
// reports false when the user declines.
func (w *wizard) newPrice(types []hetzner.ServerType, wasSize, wasLoc, size, loc string) (bool, error) {
	t, _ := hetzner.TypeNamed(types, size)
	now, orderable, priced := t.MonthlyIn(loc)
	if !orderable {
		return true, nil
	}
	old, _ := hetzner.TypeNamed(types, wasSize)
	before, _, shown := old.MonthlyIn(wasLoc)
	cost, says := "no price in Hetzner's list", "has no price in Hetzner's list"
	if priced {
		cost = fmt.Sprintf("about EUR %.2f a month", now)
		says = "costs " + cost
		if shown && fmt.Sprintf("%.2f", before) == fmt.Sprintf("%.2f", now) {
			w.logf("%s in %s %s, as before", size, loc, says)
			return true, nil
		}
	}
	was := ""
	if shown {
		was = fmt.Sprintf(", against EUR %.2f for %s in %s", before, wasSize, wasLoc)
	}
	w.logf("%s in %s %s%s, billed by Hetzner until keelbox destroy %s", size, loc, says, was, w.box)
	if w.o.yes {
		return true, nil
	}
	if !w.p.Interactive() {
		return false, w.needFlag("the go-ahead for "+size+" in "+loc+" ("+cost+")", "--yes", w.upCommand(map[string]string{"yes": "true"}))
	}
	ok, err := w.p.Confirm(fmt.Sprintf("Create the server as %s in %s (%s until you destroy it)?", size, loc, cost), true)
	if err != nil || ok {
		return ok, err
	}
	w.logf("nothing changed; %s stays %s in %s, with no server. To create it there later: %s", w.box, wasSize, wasLoc, w.upCommand(map[string]string{"yes": "true"}))
	return false, nil
}

// noServerYet checks, before up moves a box to another cloud or changes
// where its server is created, that the cloud has no server keelbox
// created for it. Up's progress says it has none, but a create run by
// hand, or one stopped part-way, may have made one: moving the box would
// leave it billed and unmanaged. Should there be one, up records it, so
// that keelbox up carries on with it there. again is the command that
// runs up again as asked, elsewhere what to run after deleting the server
// to go ahead anyway.
func (w *wizard) noServerYet(ctx context.Context, cfg *config.Config, again, elsewhere string) error {
	box, at := cfg.Box, cfg.Target.Provider
	cl, err := openCloud(ctx, cfg, w.o, noPrompt(), io.Discard)
	if err == nil {
		var srv *cloudServer
		srv, err = cl.find(ctx)
		switch {
		case err == nil:
			_ = recordServer(cfg, srv.ip) // the error below stops the change either way
			return fmt.Errorf("%s has a server on %s after all, %s, which keelbox created; a box has one server, so nothing changed. Set it up there: keelbox up %s. "+
				"Or delete it first: keelbox destroy %s, then %s", box, at, srv.about, box, box, elsewhere)
		case provider.NoServer(err):
			return nil
		}
	}
	return fmt.Errorf("nothing changed: keelbox could not check %s for a server of %s (%v). Once it can: %s", at, box, err, again)
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
	var others []string
	for _, l := range r.locations {
		if l != r.location {
			others = append(others, l)
		}
	}
	var names []string
	for _, e := range otherClouds(cfg.Target.Provider) {
		names = append(names, e.name)
	}
	cloud := prompt.Choice{Key: "cloud", Label: "Another cloud", Detail: orList(names) + "; your other answers are kept"}
	stop := prompt.Choice{Key: "stop", Label: "Stop here", Detail: "nothing was created; keelbox up " + box + " tries the same again"}
	var choices []prompt.Choice
	if r.quota {
		stop.Detail = "nothing was created; once Hetzner has raised the limit or a server is deleted, keelbox up " + box + " tries again"
		choices = []prompt.Choice{cloud, stop}
	} else {
		if len(others) > 0 && r.sizesAt != nil {
			d := r.size + " is not sold elsewhere now; you pick another size there"
			if len(r.otherLocations) > 0 {
				d = r.size + " can be ordered in " + strings.Join(r.otherLocations, ", ")
			}
			choices = append(choices, prompt.Choice{Key: "location", Label: "Another location", Detail: d})
		}
		if len(r.otherSizes) > 0 && r.sizesAt != nil {
			choices = append(choices, prompt.Choice{Key: "size", Label: "Another size in " + r.location, Detail: strings.Join(r.otherSizes, ", ") + " can be ordered there"})
		}
		choices = append(choices, cloud, stop)
	}
	i, err := w.p.Choose("How should keelbox up go on?", choices, 0)
	if err != nil {
		return err
	}
	set := func(key, value string) error {
		if err := w.setPlace(cfg, path, key, value); err != nil {
			return fmt.Errorf("%v; set %s: %s in %s yourself, then: keelbox up %s", err, key, value, tildePath(path), box)
		}
		return nil
	}
	switch choices[i].Key {
	case "location":
		// Each location with what the box costs there, against what it
		// was to cost: the go-ahead was for that price.
		was := r.priceAt(r.location)
		lc := make([]prompt.Choice, len(others))
		for j, l := range others {
			lc[j] = prompt.Choice{Key: l, Label: l, Detail: r.size + " is not sold there; you pick another size"}
			if p := r.priceAt(l); p != "" {
				lc[j].Detail = r.size + " at " + p
				if was != "" {
					lc[j].Detail += " (" + was + " in " + r.location + ")"
				}
			}
		}
		j, err := w.p.Choose("Which location?", lc, 0)
		if err != nil {
			return err
		}
		loc, size := others[j], r.size
		if r.priceAt(loc) == "" {
			sc, def := r.sizesAt(loc)
			w.hint(r.size + " cannot be ordered in " + loc + " now, so the box needs another size there.")
			k, err := w.p.Choose("Which size in "+loc+"?", sc, def)
			if err != nil {
				return err
			}
			size = sc[k].Key
		}
		if err := set(r.locationKey, loc); err != nil {
			return err
		}
		return set(r.sizeKey, size)
	case "size":
		sc, _ := r.sizesAt(r.location)
		var keep []prompt.Choice
		def := 0
		for _, c := range sc {
			if c.Key == r.size {
				continue
			}
			if c.Key == r.otherSizes[0] {
				def = len(keep)
			}
			keep = append(keep, c)
		}
		k, err := w.p.Choose("Which size in "+r.location+"?", keep, def)
		if err != nil {
			return err
		}
		return set(r.sizeKey, keep[k].Key)
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
	}
	return errStopHere
}

// moveCloud configures a box whose server was not created on another
// cloud, the one --provider names: the answers that hold for any cloud
// are kept, the cloud's own are asked (or taken from the flags), and so is
// the go-ahead, unless --yes. The config and up's progress are written
// anew. Until then the box stays where it was, and what up prints to
// continue moves it.
func (w *wizard) moveCloud(ctx context.Context, name string, cfg *config.Config) (int, error) {
	st, _ := loadUpState(cfg)
	d := upDraft{Box: name, Answers: map[string]string{}}
	for k, v := range st.Answers {
		if !cloudAnswers[k] {
			d.Answers[k] = v
		}
	}
	w.useDraft(d) // the command line's flags win
	to := w.o.provider
	// What carries the move on is this command with the flags it was
	// given; the kept answers come from the box's progress again.
	w.moving, w.next = cfg.Target.Provider, w.upCommand(nil)
	// Held to the end of the run: a keelbox create meanwhile would order
	// on the cloud the box is leaving.
	if err := w.lock(name); err != nil {
		return 2, err
	}
	if err := w.noServerYet(ctx, cfg, w.next, "keelbox forget "+name+", then: "+w.next); err != nil {
		return 2, err
	}
	w.logf("moving %s from %s to %s; nothing was created on %s", name, cfg.Target.Provider, to, cfg.Target.Provider)
	next, nst, proceed, err := w.configure(ctx, name)
	if err != nil || !proceed {
		// The answers stay in the box's progress, not in a draft: the
		// command up prints, with --provider, carries on with them.
		forgetDraft()
		w.draft = nil
	}
	var needs *needsYouError
	switch {
	case errors.Is(err, prompt.ErrInterrupted) || errors.Is(err, prompt.ErrNoAnswer):
		return 2, err // cmdUp says how to continue: w.next
	case errors.As(err, &needs):
		return exitNeedsYou, &needsYouError{w.moveNext(needs.msg)}
	case err != nil:
		return 2, errors.New(w.moveNext(err.Error()))
	case !proceed:
		return 0, nil // declinedGoAhead said how to move it later
	}
	w.moving, w.next = "", ""
	w.fresh = true
	return w.carryOn(ctx, next, nst)
}

// moveNext is msg, an error during a move, ending with the command that
// carries the move on, unless it names one already: every example up
// prints during a move is the move's own command (wizard.example).
func (w *wizard) moveNext(msg string) string {
	if strings.Contains(msg, "keelbox up "+w.box+" --provider "+w.o.provider) {
		return msg
	}
	return msg + ". Then move it: " + w.next
}

// carryOn is resume, and, when the user chose another cloud after a
// refusal, the move there. The flags for the cloud refused (its location,
// size and sign-in) are dropped first: they do not hold for the next.
func (w *wizard) carryOn(ctx context.Context, cfg *config.Config, st upState) (int, error) {
	code, err := w.resume(ctx, cfg, st)
	var mv *movingCloud
	if !errors.As(err, &mv) {
		return code, err
	}
	saved := w.o
	fs := flagSet(&w.o, io.Discard) // defining the flags resets their fields
	w.o = saved
	if w.o.set == nil {
		w.o.set = map[string]bool{}
	}
	for k := range cloudAnswers {
		if k != "provider" {
			_ = fs.Set(k, "")
			delete(w.o.set, k)
			delete(w.drafted, k)
		}
	}
	// --yes was the go-ahead for the cloud refused: another cloud has
	// another price, and up asks again.
	_ = fs.Set("yes", "false")
	delete(w.o.set, "yes")
	w.o.provider = mv.via
	w.o.set["provider"] = true
	return w.moveCloud(ctx, cfg.Box, cfg)
}
