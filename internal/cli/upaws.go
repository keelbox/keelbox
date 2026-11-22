package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/prompt"
	"github.com/keelbox/keelbox/internal/provider/aws"
)

// keelbox up for a new AWS server (docs/adr/0065-aws-through-the-aws-cli.md).
// There is no credential to ask for: up checks that the AWS CLI is
// installed and signed in, then lists the regions and the Lightsail
// bundles with their prices from the account itself.

// awsNeeds is the AWS line of the "Which cloud?" question: whether the
// AWS CLI keelbox runs is installed here. Whether it is signed in is asked
// of AWS, after the choice.
func awsNeeds(context.Context, options) (string, bool) {
	if awsInstalled() {
		return "uses your AWS CLI sign-in", true
	}
	return "needs the AWS CLI", false
}

// askAWS checks the AWS CLI's sign-in, then gets the region and the bundle.
func (w *wizard) askAWS(ctx context.Context, a *answers) error {
	s := &a.starter
	s.Provider, s.Blueprint = config.ProviderAWS, config.DefaultAWSBlueprint
	w.step("AWS sign-in")
	again := "keelbox up " + s.Box
	if !awsInstalled() {
		// Only the user can install it: up stops here, before any change.
		return &needsYouError{aws.ErrNoCLI.Error() + ". Then: " + again}
	}
	switch {
	case w.o.set["profile"]:
		if err := config.ValidAWSProfile(w.o.profile); err != nil {
			return fmt.Errorf("--profile %q: %w", w.o.profile, err)
		}
		s.Profile = w.o.profile
	case w.p.Interactive():
		w.hint("keelbox runs your AWS CLI and never sees its credentials. It uses the sign-in of the profile you name: " +
			"access keys, single sign-on (SSO) or aws login.")
		p, err := w.p.AskOr("AWS CLI profile", "your default", "your default", config.ValidAWSProfile)
		if err != nil {
			return err
		}
		s.Profile = p
	}
	w.remember("profile", s.Profile)
	cl := awsClient(s.Profile, "")
	id, err := cl.Identity(ctx)
	if errors.Is(err, aws.ErrNotSignedIn) {
		return &needsYouError{err.Error() + ". Then: " + again}
	}
	if err != nil {
		return err
	}
	w.ok(fmt.Sprintf("signed in to AWS account %s as %s", id.Account, id.Arn))
	a.notes = append(a.notes, "this AWS sign-in can do whatever its profile is allowed in the account; keelbox changes only Lightsail resources tagged keelbox=true. "+
		"A profile allowed only Lightsail keeps the rest of the account out of reach")
	regions, err := cl.Regions(ctx)
	if err != nil {
		return err
	}
	w.step("Server")
	if err := w.askAWSRegion(ctx, a, cl, regions); err != nil {
		return err
	}
	w.remember("location", s.Region)
	cl.Region = s.Region
	if in, err := cl.Instance(ctx, s.Box); err != nil {
		return err
	} else if in != nil {
		return fmt.Errorf("a Lightsail instance named %s already exists in %s; pick another name: keelbox up %s-2", s.Box, s.Region, s.Box)
	}
	bundles, err := cl.Bundles(ctx)
	if err != nil {
		return err
	}
	if err := w.askAWSBundle(a, aws.Offers(bundles)); err != nil {
		return err
	}
	w.remember("size", s.Bundle)
	return nil
}

func (w *wizard) askAWSRegion(ctx context.Context, a *answers, cl *aws.Client, regions []aws.Region) error {
	s := &a.starter
	if len(regions) == 0 {
		return errors.New("the AWS CLI lists no Lightsail regions")
	}
	var names []string
	for _, r := range regions {
		names = append(names, r.Name)
	}
	if w.o.location != "" {
		if !contains(names, w.o.location) {
			return fmt.Errorf("--location %q is not a region that offers Lightsail; use one of: %s", w.o.location, strings.Join(names, ", "))
		}
		s.Region = w.o.location
		return nil
	}
	// The region the AWS CLI is configured for, when it offers Lightsail.
	def, configured := 0, cl.ConfiguredRegion(ctx)
	for i, n := range names {
		if n == configured {
			def = i
		}
	}
	if !w.p.Interactive() {
		if names[def] != configured {
			return w.needFlag("the AWS region", "--location", "keelbox up "+s.Box+" --provider aws --location eu-central-1 --yes")
		}
		s.Region = configured
		return nil
	}
	var choices []prompt.Choice
	for _, r := range regions {
		choices = append(choices, prompt.Choice{Key: r.Name, Label: r.Name, Detail: r.DisplayName})
	}
	i, err := w.p.Choose("Which region?", choices, def)
	if err != nil {
		return err
	}
	s.Region = names[i]
	return nil
}

func (w *wizard) askAWSBundle(a *answers, offers []aws.Offer) error {
	s := &a.starter
	if len(offers) == 0 {
		return fmt.Errorf("no Linux bundle with a public IPv4 address can be ordered in %s now; pick another region: keelbox up %s --location us-east-1", s.Region, s.Box)
	}
	pick := func(i int) {
		o := offers[i]
		a.awsOffer, s.Bundle = &o, o.Bundle.BundleID
	}
	if w.o.size != "" {
		var names []string
		for i, o := range offers {
			if o.Bundle.BundleID == w.o.size {
				pick(i)
				return nil
			}
			names = append(names, o.Bundle.BundleID)
		}
		return fmt.Errorf("--size %q is not a Linux bundle with a public IPv4 address in %s; these are: %s", w.o.size, s.Region, strings.Join(names, ", "))
	}
	def := aws.DefaultOffer(offers)
	if !w.p.Interactive() {
		pick(def)
		return nil
	}
	var choices []prompt.Choice
	for _, o := range offers {
		choices = append(choices, prompt.Choice{Key: o.Bundle.BundleID, Label: o.Bundle.BundleID, Detail: o.Describe()})
	}
	recommend(choices, def)
	w.hint(fmt.Sprintf("Estimated Lightsail bundle prices from AWS for %s, in US dollars before tax. AWS bills by the hour, up to the monthly price, until the server is deleted. %s", s.Region, sizeHint))
	i, err := w.p.Choose("Which size?", choices, def)
	if err != nil {
		return err
	}
	pick(i)
	return nil
}
