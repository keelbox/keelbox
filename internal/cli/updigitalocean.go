package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/prompt"
	"github.com/keelbox/keelbox/internal/provider/digitalocean"
)

// keelbox up for a new DigitalOcean server
// (docs/adr/0067-digitalocean-through-doctl.md). There is no credential
// to ask for: up checks that doctl is installed and signed in, then lists
// the regions and the Droplet sizes with their prices from the account
// itself.

// doNeeds is the DigitalOcean line of the "Which cloud?" question:
// whether the doctl keelbox runs is installed here. Whether it is signed
// in is asked of DigitalOcean, after the choice.
func doNeeds(context.Context, options) (string, bool) {
	if doInstalled() {
		return "uses your doctl sign-in", true
	}
	return "needs doctl", false
}

// askDigitalOcean checks doctl's sign-in, then gets the region and the size.
func (w *wizard) askDigitalOcean(ctx context.Context, a *answers) error {
	s := &a.starter
	s.Provider, s.Image = config.ProviderDigitalOcean, config.DefaultDigitalOceanImage
	w.step("DigitalOcean sign-in")
	again := "keelbox up " + s.Box
	if !doInstalled() {
		// Only the user can install it: up stops here, before any change.
		return &needsYouError{digitalocean.ErrNoCLI.Error() + ". Then: " + again}
	}
	switch {
	case w.o.set["doctl-context"]:
		if err := config.ValidDoctlContext(w.o.doctlContext); err != nil {
			return fmt.Errorf("--doctl-context %q: %w", w.o.doctlContext, err)
		}
		s.DoctlContext = w.o.doctlContext
	case w.p.Interactive():
		w.hint("keelbox runs your doctl and never sees its token. It uses the sign-in of the doctl context you name (doctl auth list shows yours).")
		c, err := w.p.AskOr("doctl context", "your current one", "your current one", config.ValidDoctlContext)
		if err != nil {
			return err
		}
		s.DoctlContext = c
	}
	w.remember("doctl-context", s.DoctlContext)
	cl := doClient(s.DoctlContext)
	acct, err := cl.Account(ctx)
	if errors.Is(err, digitalocean.ErrNotSignedIn) {
		return &needsYouError{err.Error() + ". Then: " + again}
	}
	if err != nil {
		return err
	}
	w.ok("signed in to DigitalOcean: " + acct.Describe())
	a.notes = append(a.notes, "this doctl sign-in can do whatever its token is allowed in the team; keelbox changes only Droplets tagged keelbox and their firewalls. "+
		"A sign-in with only the scopes keelbox needs keeps the rest of the team out of reach: "+docsURL+"/guides/credentials#digitalocean-nothing-kept")
	regions, err := cl.Regions(ctx)
	if err != nil {
		return err
	}
	w.step("Server")
	if err := w.askDORegion(a, regions); err != nil {
		return err
	}
	w.remember("location", s.Region)
	if named, err := cl.DropletsNamed(ctx, s.Box); err != nil {
		return err
	} else if len(named) > 0 {
		return fmt.Errorf("a Droplet named %s already exists (id %d); pick another name: keelbox up %s-2", s.Box, named[0].ID, s.Box)
	}
	sizes, err := cl.Sizes(ctx)
	if err != nil {
		return err
	}
	if err := w.askDOSize(a, digitalocean.Offers(sizes, s.Region)); err != nil {
		return err
	}
	w.remember("size", s.Size)
	return nil
}

func (w *wizard) askDORegion(a *answers, regions []digitalocean.Region) error {
	s := &a.starter
	if len(regions) == 0 {
		return errors.New("doctl lists no region that takes new Droplets")
	}
	var slugs []string
	for _, r := range regions {
		slugs = append(slugs, r.Slug)
	}
	if w.o.location != "" {
		if !contains(slugs, w.o.location) {
			return fmt.Errorf("--location %q is not a region that takes new Droplets; use one of: %s", w.o.location, strings.Join(slugs, ", "))
		}
		s.Region = w.o.location
		return nil
	}
	// doctl has no region of its own to default to, and the region decides
	// where the user's work lives: without a terminal it must be named.
	if !w.p.Interactive() {
		return w.needFlag("the DigitalOcean region", "--location", "keelbox up "+s.Box+" --provider digitalocean --location fra1 --yes")
	}
	var choices []prompt.Choice
	for _, r := range regions {
		choices = append(choices, prompt.Choice{Key: r.Slug, Label: r.Slug, Detail: r.Name})
	}
	i, err := w.p.Choose("Which region?", choices, 0)
	if err != nil {
		return err
	}
	s.Region = slugs[i]
	return nil
}

func (w *wizard) askDOSize(a *answers, offers []digitalocean.Offer) error {
	s := &a.starter
	if len(offers) == 0 {
		return fmt.Errorf("no Droplet size can be ordered in %s now; pick another region: keelbox up %s --location nyc3", s.Region, s.Box)
	}
	pick := func(i int) {
		o := offers[i]
		a.doOffer, s.Size = &o, o.Size.Slug
	}
	if w.o.size != "" {
		var some []string
		for i, o := range offers {
			if o.Size.Slug == w.o.size {
				pick(i)
				return nil
			}
			if len(some) < 8 {
				some = append(some, o.Size.Slug)
			}
		}
		return fmt.Errorf("--size %q is not a Droplet size that can be ordered in %s; the cheapest are: %s (doctl compute size list shows all)", w.o.size, s.Region, strings.Join(some, ", "))
	}
	def := digitalocean.DefaultOffer(offers)
	if !w.p.Interactive() {
		pick(def)
		return nil
	}
	// DigitalOcean sells well over a hundred sizes; the menu shows the
	// shared-CPU ones up to a size no agent box needs, and --size takes
	// any other.
	shown, shownDef := offers, def
	if short, d := doMenu(offers, def); len(short) > 0 {
		shown, shownDef = short, d
	}
	var choices []prompt.Choice
	for _, o := range shown {
		choices = append(choices, prompt.Choice{Key: o.Size.Slug, Label: o.Size.Slug, Detail: o.Describe()})
	}
	hint := fmt.Sprintf("Estimated Droplet prices from DigitalOcean for %s, in US dollars before tax. DigitalOcean bills until the server is deleted, also while it is powered off.", s.Region)
	if len(shown) < len(offers) {
		hint += fmt.Sprintf(" Showing %d of %d sizes; any other: keelbox up %s --size SLUG (doctl compute size list).", len(shown), len(offers), s.Box)
	}
	recommend(choices, shownDef)
	w.hint(hint + " " + sizeHint)
	i, err := w.p.Choose("Which size?", choices, shownDef)
	if err != nil {
		return err
	}
	for j, o := range offers {
		if o.Size.Slug == shown[i].Size.Slug {
			pick(j)
		}
	}
	return nil
}

// doMenuMax is the most sizes the size question lists.
const doMenuMax = 12

// doMenu shortens a long list of offers to the cheapest ones around the
// default, keeping the default in it, and returns the default's place in
// the short list. A list that already fits is returned as nil.
func doMenu(offers []digitalocean.Offer, def int) ([]digitalocean.Offer, int) {
	if len(offers) <= doMenuMax {
		return nil, 0
	}
	start := 0
	if def >= doMenuMax-4 {
		start = def - (doMenuMax - 5)
	}
	end := start + doMenuMax
	if end > len(offers) {
		end = len(offers)
	}
	return offers[start:end], def - start
}
