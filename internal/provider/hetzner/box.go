package hetzner

import (
	"context"
	"fmt"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/provider"
)

// Label keys keelbox puts on every server it creates. Destroy refuses
// servers without LabelManaged, so keelbox never deletes what it did not make.
const (
	LabelManaged = provider.LabelManaged
	LabelBox     = provider.LabelBox
)

// Labels are the server labels for a box.
func Labels(c *config.Config) map[string]string {
	return provider.ManagedLabels(c.Box, c.Target.Hetzner.Labels)
}

// HostKey is an SSH host key pair generated on the laptop, in OpenSSH format.
type HostKey = provider.HostKeyPair

func archFor(t ServerType) string {
	if t.Architecture == "" {
		return "x86"
	}
	return t.Architecture
}

// Create makes the server for a box and waits until it is running with an
// address. It fails if a server of that name already exists.
func Create(ctx context.Context, cl *Client, c *config.Config, hk HostKey, logf func(string, ...any)) (*Server, error) {
	h := c.Target.Hetzner
	if s, err := cl.ServerByName(ctx, c.Box); err != nil {
		return nil, err
	} else if s != nil {
		return nil, fmt.Errorf("a server named %s already exists (id %d); converge it: keelbox apply %s, or delete it first: keelbox destroy %s", c.Box, s.ID, c.Box, c.Box)
	}
	// Until CreateServer, no server is ordered: each failure says so, and
	// where the box is created may still change.
	notOrdered := func(err error) error { return &provider.NotOrderedError{Err: err} }
	notOrderedf := func(format string, a ...any) error { return notOrdered(fmt.Errorf(format, a...)) }
	types, err := cl.ServerTypes(ctx)
	if err != nil {
		return nil, notOrdered(err)
	}
	var st *ServerType
	for i := range types {
		if types[i].Name == h.ServerType {
			st = &types[i]
		}
	}
	if st == nil {
		return nil, notOrderedf("target.hetzner.server_type %q does not exist; set one keelbox up lists with prices, for example server_type: cx23", h.ServerType)
	}
	if !st.AvailableIn(h.Location) {
		return nil, (&RefusedError{Action: "create", Box: c.Box, Location: h.Location, ServerType: h.ServerType, Types: types, Left: "Nothing was created"}).alternatives(nil)
	}
	img, err := cl.SystemImage(ctx, h.Image, archFor(*st))
	if err != nil {
		return nil, notOrdered(err)
	}
	if img == nil {
		return nil, notOrderedf("target.hetzner.image %q is not available for %s servers; set image: ubuntu-24.04 or ubuntu-26.04", h.Image, archFor(*st))
	}
	keyIDs, err := sshKeyIDs(ctx, cl, c)
	if err != nil {
		return nil, notOrdered(err)
	}
	if len(keyIDs) == 0 {
		return nil, notOrderedf("target.hetzner.ssh_keys: name at least one project SSH key, for the first connection as root; keelbox up %s uploads yours and sets it", c.Box)
	}
	userData, err := provider.UserData(c.Box, c.Host.Hostname, hk)
	if err != nil {
		return nil, notOrdered(err)
	}
	// The firewall exists before the server, so the server never boots
	// without it. A new one admits SSH; apply closes it once the tailnet
	// is proven.
	had, err := FindFirewall(ctx, cl, c.Box)
	if err != nil {
		return nil, notOrderedf("cloud firewall: %w", err)
	}
	fw, changes, err := EnsureFirewall(ctx, cl, c, 0, nil)
	if err != nil {
		return nil, notOrderedf("cloud firewall: %w", err)
	}
	for _, ch := range changes {
		logf("%s", ch)
	}
	price, _ := st.HourlyGross(h.Location)
	logf("creating %s: %s in %s, %s (EUR %.4f/h gross)", c.Box, st.Name, h.Location, img.Name, price)
	srv, act, err := cl.CreateServer(ctx, CreateServerRequest{
		Name: c.Box, ServerType: st.Name, Image: img.Name, Location: h.Location,
		SSHKeys: keyIDs, Labels: Labels(c), UserData: userData,
		Firewalls: []ServerFirewall{{Firewall: fw.ID}},
	})
	if ae, ok := refusal(err); ok {
		return nil, (&RefusedError{API: ae, Action: "create", Box: c.Box, Location: h.Location, ServerType: st.Name, Types: types,
			Left: removeUnusedFirewall(ctx, cl, c.Box, had == nil, logf)}).alternatives(nil)
	}
	if err != nil {
		return nil, err
	}
	if err := cl.WaitAction(ctx, act.ID, 2*time.Second); err != nil {
		return srv, err
	}
	if s, err := waitRunning(ctx, cl, srv.ID); err != nil {
		return srv, err
	} else {
		return s, nil
	}
}

// removeUnusedFirewall deletes the firewall Create made for the box after
// Hetzner refused to create its server: the box has no server (Create
// checked), so the firewall serves nothing, and a box that moves to
// another cloud would leave it behind. The next create makes it again. A
// firewall that was there before, or that is attached to anything, stays
// as it is; only an unattached one is worth deleting by hand. made says
// whether this Create made it. It returns what is left, as a sentence.
func removeUnusedFirewall(ctx context.Context, cl *Client, box string, made bool, logf func(string, ...any)) string {
	const key = "; your SSH key stays in the project for the next try"
	name := FirewallName(box)
	stays := func(why string) string {
		return fmt.Sprintf("No server was created. The firewall %s stays (%s), free of charge; should %s move to another cloud, delete it in the Hetzner Console (Firewalls)%s",
			name, why, box, key)
	}
	ctx = context.WithoutCancel(ctx)
	f, err := FindFirewall(ctx, cl, box)
	if f != nil {
		name = f.Name
	}
	switch {
	case err != nil:
		// Whether it is in use is not known: no advice to delete it.
		return fmt.Sprintf("No server was created. The firewall %s is left as it was: looking it up again failed (%v)%s", name, err, key)
	case f == nil:
		return "No server was created" + key
	case len(f.AppliedTo) > 0:
		return fmt.Sprintf("No server was created. The firewall %s stays as it is: something is attached to it%s", name, key)
	case !made:
		return stays("it was there before this create")
	case !managed(f, box):
		return stays("it is not labelled for " + box)
	}
	if err := cl.DeleteFirewall(ctx, f.ID); err != nil && !apiErr(err, "", 404) {
		return stays("deleting it failed: " + err.Error())
	}
	logf("deleted firewall %s (id %d) again: it was for the server Hetzner refused", f.Name, f.ID)
	return "No server was created, and keelbox deleted the firewall it had made for it" + key
}

// Find returns the keelbox-managed server for a box.
func Find(ctx context.Context, cl *Client, box string) (*Server, error) {
	s, err := cl.ServerByName(ctx, box)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, &provider.NoServerError{Msg: fmt.Sprintf("no server named %s in the Hetzner project; %s", box, provider.NoServerFix(box))}
	}
	if s.Labels[LabelManaged] != "true" || s.Labels[LabelBox] != box {
		return nil, fmt.Errorf("server %s (id %d) was not created by keelbox (missing %s labels); not touching it. Use another box name, or manage it as an existing server: keelbox up %s --provider ssh --host ITS-IP", box, s.ID, LabelManaged, box)
	}
	return s, nil
}

// Destroy deletes a keelbox-managed server and waits for the deletion.
func Destroy(ctx context.Context, cl *Client, s *Server) error {
	if s.Labels[LabelManaged] != "true" {
		return fmt.Errorf("refusing to delete server %d: not labelled %s=true", s.ID, LabelManaged)
	}
	act, err := cl.DeleteServer(ctx, s.ID)
	if err != nil {
		return err
	}
	return cl.WaitAction(ctx, act.ID, 2*time.Second)
}
