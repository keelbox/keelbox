package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/prompt"
	"github.com/keelbox/keelbox/internal/provider/gcp"
)

// keelbox up for a new Google Cloud server
// (docs/adr/0068-google-cloud-through-gcloud.md). There is no credential
// to ask for: up checks that gcloud is installed and signed in, then asks
// for the project, the zone and the machine type. gcloud has no price
// list, so up shows no price and names Google's pricing page.

// gcpNeeds is the Google Cloud line of the "Which cloud?" question:
// whether the gcloud keelbox runs is installed here. Whether it is signed
// in is asked of Google, after the choice.
func gcpNeeds(context.Context, options) (string, bool) {
	if gcpInstalled() {
		return "uses your gcloud sign-in", true
	}
	return "needs gcloud", false
}

// askGCP checks gcloud's sign-in and project, then gets the zone and the
// machine type.
func (w *wizard) askGCP(ctx context.Context, a *answers) error {
	s := &a.starter
	s.Provider = config.ProviderGCP
	w.step("Google Cloud sign-in")
	again := w.rerun(s.Box)
	if !gcpInstalled() {
		// Only the user can install it: up stops here, before any change.
		return &needsYouError{gcp.ErrNoCLI.Error() + ". Then: " + again}
	}
	set, err := gcpClient("").Settings(ctx)
	if err != nil {
		return err
	}
	project := set.Project
	switch {
	case w.o.set["gcp-project"]:
		if err := config.ValidGCPProject(w.o.gcpProject); err != nil {
			return fmt.Errorf("--gcp-project %q: %w", w.o.gcpProject, err)
		}
		project = w.o.gcpProject
	case w.p.Interactive():
		w.hint("keelbox runs your gcloud and never sees its credentials. It creates the server in the project you name (gcloud projects list shows yours).")
		valid := func(v string) error {
			if v == "" {
				return errors.New("a project id is needed")
			}
			return config.ValidGCPProject(v)
		}
		if project, err = w.p.AskValid("Google Cloud project", set.Project, valid); err != nil {
			return err
		}
	case project == "":
		return w.needFlag("the Google Cloud project (gcloud has none set)", "--gcp-project", w.example("keelbox up "+s.Box+" --provider gcp --gcp-project my-project --location europe-west3-a --yes", map[string]string{"gcp-project": "my-project"}))
	}
	// The project is written to the config: the box stays in it whatever
	// gcloud is pointed at later.
	s.Project = project
	w.remember("gcp-project", project)
	cl := gcpClient(project)
	acct, err := cl.Account(ctx)
	if errors.Is(err, gcp.ErrNotSignedIn) || errors.Is(err, gcp.ErrAPIDisabled) {
		return &needsYouError{err.Error() + ". Then: " + again}
	}
	if err != nil {
		return err
	}
	w.ok("signed in to Google Cloud: " + acct.Describe())
	a.notes = append(a.notes, "this gcloud sign-in can do whatever its account is allowed in the project; keelbox changes only the VM labelled keelbox=true and the firewall rules and address named after the box. "+
		"A project used only for boxes keeps everything else out of reach")
	zones, err := cl.Zones(ctx)
	if err != nil {
		return err
	}
	w.step("Server")
	if err := w.askGCPZone(a, zones, set.Zone); err != nil {
		return err
	}
	w.remember("location", s.Zone)
	if named, err := cl.InstancesNamed(ctx, s.Box); err != nil {
		return err
	} else if len(named) > 0 {
		return fmt.Errorf("a VM named %s already exists in %s; pick another name: keelbox up %s-2", s.Box, named[0].ZoneName(), s.Box)
	}
	types, err := cl.MachineTypes(ctx, s.Zone)
	if err != nil {
		return err
	}
	if err := w.askGCPType(a, types); err != nil {
		return err
	}
	w.remember("size", s.MachineType)
	return nil
}

// askGCPZone gets the zone: --location, else the zone gcloud is
// configured for, offered as the default. Google has well over a hundred
// zones, so the question takes a name instead of listing them.
func (w *wizard) askGCPZone(a *answers, zones []gcp.Zone, configured string) error {
	s := &a.starter
	if len(zones) == 0 {
		return errors.New("gcloud lists no zone that is up")
	}
	valid := func(z string) error {
		if gcp.HasZone(zones, z) {
			return nil
		}
		var near []string
		for _, have := range zones {
			if strings.HasPrefix(have.Name, z) || gcp.RegionOf(have.Name) == gcp.RegionOf(z) {
				near = append(near, have.Name)
			}
		}
		if len(near) > 0 && len(near) <= 8 {
			return fmt.Errorf("%q is not a zone that is up; near it: %s", z, strings.Join(near, ", "))
		}
		return fmt.Errorf("%q is not a zone that is up (gcloud compute zones list shows them, for example europe-west3-a)", z)
	}
	if w.o.location != "" {
		if err := valid(w.o.location); err != nil {
			return fmt.Errorf("--location %w", err)
		}
		s.Zone = w.o.location
		return nil
	}
	if !gcp.HasZone(zones, configured) {
		configured = ""
	}
	// The zone decides where the user's work lives: without a terminal it
	// is gcloud's own, or it must be named.
	if !w.p.Interactive() {
		if configured == "" {
			return w.needFlag("the Google Cloud zone", "--location", w.example("keelbox up "+s.Box+" --provider gcp --location europe-west3-a --yes", map[string]string{"location": "europe-west3-a"}))
		}
		s.Zone = configured
		return nil
	}
	w.hint("A zone is a data centre in a region, such as europe-west3-a (Frankfurt) or us-central1-a (Iowa); gcloud compute zones list shows all.")
	z, err := w.p.AskValid("Zone", configured, valid)
	if err != nil {
		return err
	}
	s.Zone = z
	return nil
}

func (w *wizard) askGCPType(a *answers, types []gcp.MachineType) error {
	s := &a.starter
	offers := gcp.Offers(types)
	var some []string
	for _, o := range offers {
		some = append(some, o.Type.Name)
	}
	if w.o.size != "" {
		// --size takes any machine type of the zone; Create checks that it
		// can run a box.
		for _, t := range types {
			if t.Name == w.o.size {
				s.MachineType = t.Name
				return nil
			}
		}
		return fmt.Errorf("--size %q is not a machine type in %s; for example: %s (gcloud compute machine-types list --zones %s shows all)", w.o.size, s.Zone, strings.Join(some, ", "), s.Zone)
	}
	if len(offers) == 0 {
		return fmt.Errorf("%s offers none of the E2 machine types keelbox lists; name one: keelbox up %s --size TYPE (gcloud compute machine-types list --zones %s)", s.Zone, s.Box, s.Zone)
	}
	def := gcp.DefaultOffer(offers)
	if !w.p.Interactive() {
		s.MachineType = offers[def].Type.Name
		return nil
	}
	var choices []prompt.Choice
	for _, o := range offers {
		choices = append(choices, prompt.Choice{Key: o.Type.Name, Label: o.Type.Name, Detail: o.Describe()})
	}
	recommend(choices, def)
	w.hint(fmt.Sprintf("keelbox cannot show Google Cloud's prices: gcloud has no price list. They are at %s. Any other machine type: keelbox up %s --size TYPE. %s", gcp.PricingURL, s.Box, sizeHint))
	i, err := w.p.Choose("Which machine type?", choices, def)
	if err != nil {
		return err
	}
	s.MachineType = offers[i].Type.Name
	return nil
}
