package hetzner

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/provider"
)

// Resize, park and resume (docs/adr/0071-resize-park-resume.md). A parked
// box is a snapshot of its server's disk, labelled like the server plus
// LabelParked, and no server. Every call here that changes or deletes a
// server or a snapshot first checks keelbox's labels for the box.

// LabelParked marks a box's parked snapshot.
const LabelParked = provider.LabelParked

// How long the waits below last, and how often they look; tests shorten
// them.
var (
	pollEvery = 2 * time.Second
	// stopTimeout is how long a server gets to shut down by itself before
	// its power is cut. systemd gives each unit 90 seconds to stop.
	stopTimeout = 5 * time.Minute
	// snapshotTimeout bounds one snapshot. An 80 GB disk with a few GB used
	// takes under a minute; a full one several.
	snapshotTimeout = 45 * time.Minute
)

func sleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

func managedServer(s *Server, box string) error {
	if !provider.Managed(s.Labels, box) {
		return fmt.Errorf("refusing to change server %d: not labelled %s=true and %s=%s", s.ID, LabelManaged, LabelBox, box)
	}
	return nil
}

// waitStatus polls the server until it reports status, or the time is up.
func waitStatus(ctx context.Context, cl *Client, id int64, status string, timeout time.Duration) (*Server, error) {
	deadline := time.Now().Add(timeout)
	for {
		s, err := cl.Server(ctx, id)
		if err != nil {
			return nil, err
		}
		if s.Status == status {
			return s, nil
		}
		if time.Now().After(deadline) {
			return s, fmt.Errorf("server %d is %s, not %s, after %s", id, s.Status, status, timeout.Round(time.Second))
		}
		if err := sleep(ctx, pollEvery); err != nil {
			return s, err
		}
	}
}

// Stop shuts the box's server down and returns once Hetzner reports it
// off. It asks the operating system first, so services stop in order and
// the disk is written out; a server still running after stopTimeout has
// its power cut, and logf says so. A server already off is left as it is.
func Stop(ctx context.Context, cl *Client, box string, s *Server, logf func(string, ...any)) error {
	if err := managedServer(s, box); err != nil {
		return err
	}
	cur, err := cl.Server(ctx, s.ID)
	if err != nil {
		return err
	}
	if cur.Status == "off" {
		return nil
	}
	act, err := cl.Shutdown(ctx, s.ID)
	if err != nil {
		return err
	}
	if err := cl.WaitAction(ctx, act.ID, pollEvery); err != nil {
		return err
	}
	if _, err := waitStatus(ctx, cl, s.ID, "off", stopTimeout); err == nil {
		return nil
	} else if ctx.Err() != nil {
		return err
	}
	logf("server %d did not shut down within %s; switching it off", s.ID, stopTimeout.Round(time.Second))
	act, err = cl.PowerOff(ctx, s.ID)
	if err != nil {
		return err
	}
	if err := cl.WaitAction(ctx, act.ID, pollEvery); err != nil {
		return err
	}
	_, err = waitStatus(ctx, cl, s.ID, "off", time.Minute)
	return err
}

// Start powers the box's server on and returns it once it runs and has an
// address. A server already running is returned as it is.
func Start(ctx context.Context, cl *Client, box string, s *Server) (*Server, error) {
	if err := managedServer(s, box); err != nil {
		return nil, err
	}
	cur, err := cl.Server(ctx, s.ID)
	if err != nil {
		return nil, err
	}
	if cur.Status != "running" {
		act, err := cl.PowerOn(ctx, s.ID)
		if err != nil {
			return nil, err
		}
		if err := cl.WaitAction(ctx, act.ID, pollEvery); err != nil {
			return nil, err
		}
	}
	return waitRunning(ctx, cl, s.ID)
}

// waitRunning returns the server once it runs and has an address.
func waitRunning(ctx context.Context, cl *Client, id int64) (*Server, error) {
	var last *Server
	for i := 0; i < 60; i++ {
		s, err := cl.Server(ctx, id)
		if err != nil {
			return last, err
		}
		if s.Status == "running" && s.PublicNet.IPv4.IP != "" {
			return s, nil
		}
		last = s
		if err := sleep(ctx, pollEvery); err != nil {
			return last, err
		}
	}
	return last, fmt.Errorf("server %d did not reach running state", id)
}

// ResizeOffers lists the server types s can change to and back from,
// cheapest first, its own type included: the same architecture, a disk at
// least as large as the server's disk now (Resize never grows the disk, so
// that is the disk the server had when it was created), and orderable in
// the server's location. location is used when the server does not say
// where it is.
func ResizeOffers(types []ServerType, s *Server, location string) []Offer {
	if l := s.LocationName(); l != "" {
		location = l
	}
	arch := archFor(s.ServerType)
	var out []Offer
	for _, t := range types {
		current := t.Name == s.ServerType.Name
		if archFor(t) != arch || t.Disk < s.DiskGB() {
			continue
		}
		if !current && (t.Deprecated || !t.AvailableIn(location) || t.deprecatedIn(location)) {
			continue
		}
		out = append(out, offerIn(t, location))
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Monthly != out[j].Monthly {
			return out[i].Monthly < out[j].Monthly
		}
		return out[i].Type.Name < out[j].Type.Name
	})
	return out
}

func offerIn(t ServerType, location string) Offer {
	o := Offer{Type: t}
	for _, p := range t.Prices {
		if p.Location == location {
			o.Hourly, _ = strconv.ParseFloat(p.PriceHourly.Gross, 64)
			o.Monthly, _ = strconv.ParseFloat(p.PriceMonthly.Gross, 64)
		}
	}
	return o
}

// Resize changes the type of the box's server, which must be off, and
// returns once Hetzner has done it. The disk keeps its size
// (upgrade_disk: false), so a later change to a type with a smaller disk
// stays possible. When it fails the server is still its old type.
func Resize(ctx context.Context, cl *Client, box string, s *Server, serverType string) error {
	if err := managedServer(s, box); err != nil {
		return err
	}
	act, err := cl.ChangeType(ctx, s.ID, serverType, false)
	if err != nil {
		return err
	}
	return cl.WaitAction(ctx, act.ID, pollEvery)
}

func parkedSelector(box string) string {
	return LabelManaged + "=true," + LabelBox + "=" + box + "," + LabelParked + "=true"
}

// Snapshot saves the disk of the box's server, which must be off, as the
// box's parked snapshot, and returns it only once Hetzner reports it
// available: complete, and usable to create a server from. When that does
// not happen, the unfinished snapshot is deleted and the error returned.
func Snapshot(ctx context.Context, cl *Client, c *config.Config, s *Server) (*Image, error) {
	if err := managedServer(s, c.Box); err != nil {
		return nil, err
	}
	cur, err := cl.Server(ctx, s.ID)
	if err != nil {
		return nil, err
	}
	if cur.Status != "off" {
		return nil, fmt.Errorf("server %d is %s; a snapshot is taken only of a server that is off", s.ID, cur.Status)
	}
	img, act, err := cl.CreateSnapshot(ctx, s.ID, "keelbox: parked box "+c.Box, provider.ParkedLabels(c.Box, c.Target.Hetzner.Labels))
	if err != nil {
		return nil, err
	}
	// From here an unfinished snapshot exists: on any failure it is deleted,
	// also when ctx was cancelled.
	fail := func(err error) (*Image, error) {
		if derr := cl.DeleteImage(context.WithoutCancel(ctx), img.ID); derr != nil && !apiErr(derr, "", 404) {
			return nil, fmt.Errorf("%w (and the unfinished snapshot %d could not be deleted: %v; delete it in the Hetzner Console, Snapshots)", err, img.ID, derr)
		}
		return nil, err
	}
	if err := cl.WaitAction(ctx, act.ID, pollEvery); err != nil {
		return fail(fmt.Errorf("snapshot %d: %w", img.ID, err))
	}
	deadline := time.Now().Add(snapshotTimeout)
	for {
		got, err := cl.Image(ctx, img.ID)
		if err != nil {
			return fail(fmt.Errorf("snapshot %d: %w", img.ID, err))
		}
		if got.Status == "available" {
			if got.Type != "snapshot" || !provider.Parked(got.Labels, c.Box) {
				return fail(fmt.Errorf("snapshot %d is not the parked snapshot keelbox asked for (type %s)", got.ID, got.Type))
			}
			return got, nil
		}
		if time.Now().After(deadline) {
			return fail(fmt.Errorf("snapshot %d is still %s after %s", img.ID, got.Status, snapshotTimeout.Round(time.Second)))
		}
		if err := sleep(ctx, pollEvery); err != nil {
			return fail(err)
		}
	}
}

// Parked returns the box's parked snapshot: the newest available snapshot
// labelled as it, or nil when the box is not parked.
func Parked(ctx context.Context, cl *Client, box string) (*Image, error) {
	imgs, err := cl.Snapshots(ctx, parkedSelector(box))
	if err != nil {
		return nil, err
	}
	var newest *Image
	for i := range imgs {
		img := &imgs[i]
		if img.Type != "snapshot" || img.Status != "available" || !provider.Parked(img.Labels, box) {
			continue
		}
		if newest == nil || img.Created.After(newest.Created) {
			newest = img
		}
	}
	return newest, nil
}

// DeleteSnapshot deletes a parked snapshot of the box. It refuses an image
// that is not a snapshot or does not carry the box's parked labels.
func DeleteSnapshot(ctx context.Context, cl *Client, box string, img *Image) error {
	if img.Type != "snapshot" || !provider.Parked(img.Labels, box) {
		return fmt.Errorf("refusing to delete image %d: not a snapshot labelled %s=true, %s=%s and %s=true", img.ID, LabelManaged, LabelBox, box, LabelParked)
	}
	if err := cl.DeleteImage(ctx, img.ID); err != nil && !apiErr(err, "", 404) {
		return err
	}
	return nil
}

// SweepSnapshots deletes every snapshot labelled as the box's parked
// snapshot, whatever its state, and returns the ones deleted: what is left
// of a box that is being destroyed.
func SweepSnapshots(ctx context.Context, cl *Client, box string) ([]Image, error) {
	imgs, err := cl.Snapshots(ctx, parkedSelector(box))
	if err != nil {
		return nil, err
	}
	var done []Image
	for i := range imgs {
		if imgs[i].Type != "snapshot" || !provider.Parked(imgs[i].Labels, box) {
			continue
		}
		if err := DeleteSnapshot(ctx, cl, box, &imgs[i]); err != nil {
			return done, err
		}
		done = append(done, imgs[i])
	}
	return done, nil
}

// sshKeyIDs are the ids of the project keys target.hetzner.ssh_keys names.
func sshKeyIDs(ctx context.Context, cl *Client, c *config.Config) ([]int64, error) {
	var ids []int64
	for _, name := range c.Target.Hetzner.SSHKeys {
		k, err := cl.SSHKeyByName(ctx, name)
		if err != nil {
			return nil, err
		}
		if k == nil {
			return nil, fmt.Errorf("target.hetzner.ssh_keys: no key named %q in the Hetzner project; keelbox up %s uploads yours, or add it in the console (Security, SSH keys) under that name", name, c.Box)
		}
		ids = append(ids, k.ID)
	}
	return ids, nil
}

// Restore creates the box's server again from its parked snapshot: the
// name, type, location, labels and project keys of the config, the box's
// Cloud Firewall attached in the create call so the server never runs
// without it, and user data that keeps what is on the disk
// (provider.ResumeUserData). It returns the server once it runs and has an
// address, and fails when a server of that name exists. A server returned
// with an error may exist.
//
// The project keys are passed because Hetzner otherwise sets a root
// password and mails it; they are the keys root already had, and sshd
// refuses root whatever its keys.
func Restore(ctx context.Context, cl *Client, c *config.Config, img *Image, logf func(string, ...any)) (*Server, error) {
	h := c.Target.Hetzner
	if img.Type != "snapshot" || !provider.Parked(img.Labels, c.Box) {
		return nil, fmt.Errorf("refusing to create %s from image %d: not its parked snapshot", c.Box, img.ID)
	}
	if s, err := cl.ServerByName(ctx, c.Box); err != nil {
		return nil, err
	} else if s != nil {
		return nil, fmt.Errorf("a server named %s already exists (id %d)", c.Box, s.ID)
	}
	types, err := cl.ServerTypes(ctx)
	if err != nil {
		return nil, err
	}
	var st *ServerType
	var fits []string
	for i := range types {
		t := &types[i]
		if t.Name == h.ServerType {
			st = t
		}
		if !t.Deprecated && archFor(*t) == img.Architecture && float64(t.Disk) >= img.DiskSize && t.AvailableIn(h.Location) && !t.deprecatedIn(h.Location) {
			fits = append(fits, t.Name)
		}
	}
	sort.Strings(fits)
	another := fmt.Sprintf("set target.hetzner.server_type to one of: %s, then run keelbox resume %s again", strings.Join(fits, ", "), c.Box)
	switch {
	case st == nil:
		return nil, fmt.Errorf("target.hetzner.server_type %q does not exist; %s", h.ServerType, another)
	case !st.AvailableIn(h.Location):
		return nil, fmt.Errorf("server type %s cannot be ordered in %s now; the snapshot is kept. Try again later, or %s", st.Name, h.Location, another)
	case float64(st.Disk) < img.DiskSize:
		return nil, fmt.Errorf("server type %s has a %d GB disk and the parked snapshot needs %g GB; %s", st.Name, st.Disk, img.DiskSize, another)
	case img.Architecture != "" && archFor(*st) != img.Architecture:
		return nil, fmt.Errorf("server type %s is %s and the parked snapshot is %s; %s", st.Name, archFor(*st), img.Architecture, another)
	}
	keyIDs, err := sshKeyIDs(ctx, cl, c)
	if err != nil {
		return nil, err
	}
	if len(keyIDs) == 0 {
		return nil, errors.New("target.hetzner.ssh_keys: name at least one project SSH key; without one Hetzner sets a root password on the new server")
	}
	// As at create: the firewall exists before the server. One that is
	// there keeps its rules, so a box that had closed public SSH stays
	// closed.
	fw, changes, err := EnsureFirewall(ctx, cl, c, 0, nil)
	if err != nil {
		return nil, fmt.Errorf("cloud firewall: %w", err)
	}
	for _, ch := range changes {
		logf("%s", ch)
	}
	price, _ := st.HourlyGross(h.Location)
	logf("creating %s from snapshot %d: %s in %s (EUR %.4f/h gross)", c.Box, img.ID, st.Name, h.Location, price)
	srv, act, err := cl.CreateServer(ctx, CreateServerRequest{
		Name: c.Box, ServerType: st.Name, Image: img.ID, Location: h.Location,
		SSHKeys: keyIDs, Labels: Labels(c), UserData: provider.ResumeUserData(c.Box),
		Firewalls: []ServerFirewall{{Firewall: fw.ID}},
	})
	if err != nil {
		return nil, err
	}
	if err := cl.WaitAction(ctx, act.ID, pollEvery); err != nil {
		return srv, err
	}
	if s, err := waitRunning(ctx, cl, srv.ID); err != nil {
		return srv, err
	} else {
		return s, nil
	}
}
