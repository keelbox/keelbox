package hetzner

import (
	"context"
	"fmt"
	"strings"
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
	types, err := cl.ServerTypes(ctx)
	if err != nil {
		return nil, err
	}
	var st *ServerType
	for i := range types {
		if types[i].Name == h.ServerType {
			st = &types[i]
		}
	}
	if st == nil {
		return nil, fmt.Errorf("target.hetzner.server_type %q does not exist; set one keelbox up lists with prices, for example server_type: cx23", h.ServerType)
	}
	if !st.AvailableIn(h.Location) {
		return nil, fmt.Errorf("server type %s cannot be ordered in %s; set target.hetzner.location to one of: %s, or choose another server_type", h.ServerType, h.Location, strings.Join(st.OrderableIn(), ", "))
	}
	img, err := cl.SystemImage(ctx, h.Image, archFor(*st))
	if err != nil {
		return nil, err
	}
	if img == nil {
		return nil, fmt.Errorf("target.hetzner.image %q is not available for %s servers; set image: ubuntu-24.04 or ubuntu-26.04", h.Image, archFor(*st))
	}
	keyIDs, err := sshKeyIDs(ctx, cl, c)
	if err != nil {
		return nil, err
	}
	if len(keyIDs) == 0 {
		return nil, fmt.Errorf("target.hetzner.ssh_keys: name at least one project SSH key, for the first connection as root; keelbox up %s uploads yours and sets it", c.Box)
	}
	userData, err := provider.UserData(c.Box, c.Host.Hostname, hk)
	if err != nil {
		return nil, err
	}
	// The firewall exists before the server, so the server never boots
	// without it. A new one admits SSH; apply closes it once the tailnet
	// is proven.
	fw, changes, err := EnsureFirewall(ctx, cl, c, 0, nil)
	if err != nil {
		return nil, fmt.Errorf("cloud firewall: %w", err)
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

// Find returns the keelbox-managed server for a box.
func Find(ctx context.Context, cl *Client, box string) (*Server, error) {
	s, err := cl.ServerByName(ctx, box)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, fmt.Errorf("no server named %s in the Hetzner project; %s", box, provider.NoServerFix(box))
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
