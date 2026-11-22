package digitalocean

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/provider"
)

// Resize, park and resume (docs/adr/0071-resize-park-resume.md). A parked
// box is a snapshot of its Droplet's disk and no Droplet. A Droplet
// snapshot is an image, and an image carries tags, so the labels of a
// parked snapshot (provider.ParkedLabels) are tags as on the Droplet:
// "keelbox", "keelbox-box-<box>" and "keelbox-parked". Every call here
// that changes or deletes a Droplet or a snapshot first checks those.

// TagParked is the tag on a box's parked snapshot.
const TagParked = provider.LabelParked

// ParkedTags are the tags for a box's parked snapshot: the Droplet's, and
// the parked marker.
func ParkedTags(c *config.Config) []string {
	tags := Tags(c)
	for _, t := range tags {
		if t == TagParked {
			return tags
		}
	}
	return append(tags, TagParked)
}

// SnapshotName is the name keelbox gives a box's parked snapshot. The name
// only says what the snapshot is, in the control panel; keelbox knows the
// snapshot by its tags.
func SnapshotName(box string) string { return "keelbox-parked-" + box }

// SnapshotUSDPerGB is DigitalOcean's published price for a Droplet
// snapshot, in US dollars per GB a month before tax, billed on the size
// the snapshot reports (docs.digitalocean.com/products/snapshots/details/pricing,
// read in November 2026). The API has no price for snapshots.
const SnapshotUSDPerGB = 0.06

// How long the waits below last; tests shorten them. poll is how often
// they look.
var (
	// stopTimeout is how long a Droplet gets to shut down by itself before
	// its power is cut. systemd gives each unit 90 seconds to stop.
	stopTimeout = 5 * time.Minute
	// actionTimeout bounds a power-on, a power-off and a resize. A resize
	// takes about a minute; a power-off sent after a shutdown the system
	// ignored waits until DigitalOcean gives that shutdown up.
	actionTimeout = 20 * time.Minute
	// snapshotTimeout bounds one snapshot. DigitalOcean says up to a minute
	// for each GB the disk holds; 2 GB take half a minute.
	snapshotTimeout = 2 * time.Hour
	// settleTimeout is how long a snapshot whose action is done gets to be
	// reported available.
	settleTimeout = 5 * time.Minute
	// firewallTimeout is how long DigitalOcean gets to report the firewall
	// applied to a new Droplet. It takes seconds from the Droplet being
	// active.
	firewallTimeout = 5 * time.Minute
)

// HasTag reports whether the image carries the tag.
func (i *Image) HasTag(tag string) bool { return contains(i.Tags, tag) }

// Parked reports whether the image is the parked snapshot of box: a
// snapshot tagged keelbox, keelbox-box-<box> and keelbox-parked.
func (i *Image) Parked(box string) bool {
	return i.Type == "snapshot" && i.HasTag(TagManaged) && i.HasTag(BoxTag(box)) && i.HasTag(TagParked)
}

// Created is when the image was made: for a snapshot, when it was taken.
// The zero time when DigitalOcean does not say.
func (i *Image) Created() time.Time {
	t, err := time.Parse(time.RFC3339, i.CreatedAt)
	if err != nil {
		return time.Time{}
	}
	return t
}

// UserImages lists the account's own images: snapshots, backups and
// custom images.
func (c *Client) UserImages(ctx context.Context) ([]Image, error) {
	var imgs []Image
	return imgs, c.compute(ctx, &imgs, "image", "list-user")
}

// scoped adds the fix to the API's refusal of a call only resize, park
// and resume make: a token with custom scopes that lacks theirs.
func (c *Client) scoped(err error) error {
	var se *ScopeError
	if !errors.As(err, &se) {
		return err
	}
	// The same refusal, with the scopes these three commands add.
	return &ScopeError{Err: se.Err, Fix: fmt.Sprintf("keelbox resize, park and resume also need the scopes %s. %s",
		ParkScopes, c.signIn("Sign in again with every scope keelbox needs", Scopes))}
}

// deleteImage deletes an image, then takes the tags off it. One that is
// gone already is not an error: the API answers 404 for an id it does not
// know and 422 for an image deleted a moment ago.
//
// DigitalOcean goes on counting a deleted image among a tag's resources
// until the tag is taken off it, which it accepts for an image that is
// gone. The tags come off after the delete and not before, so that a
// delete that fails leaves a snapshot keelbox still finds. A tag that
// stays on is not an error: it costs nothing and only keeps the tag from
// being deleted with the box.
func (c *Client) deleteImage(ctx context.Context, id int64, tags []string) error {
	err := c.compute(ctx, nil, "image", "delete", strconv.FormatInt(id, 10), "--force")
	gone := notFound(err) || (IsStatus(err, 422) && strings.Contains(err.Error(), "already deleted"))
	if err != nil && !gone {
		return c.scoped(err)
	}
	for _, t := range tags {
		_ = c.compute(ctx, nil, "tag", "remove", t, "--resource", "do:image:"+strconv.FormatInt(id, 10))
	}
	return nil
}

// tagImage puts a tag, which must exist, on an image.
func (c *Client) tagImage(ctx context.Context, tag string, id int64) error {
	return c.scoped(c.compute(ctx, nil, "tag", "apply", tag, "--resource", "do:image:"+strconv.FormatInt(id, 10)))
}

// Action is something DigitalOcean does to a Droplet over time: a
// shutdown, a resize, a snapshot.
type Action struct {
	ID     int64  `json:"id"`
	Status string `json:"status"` // in-progress, completed or errored
	Type   string `json:"type"`
}

// dropletAction starts an action on a Droplet and returns it.
func (c *Client) dropletAction(ctx context.Context, id int64, verb string, args ...string) (*Action, error) {
	var acts []Action
	if err := c.compute(ctx, &acts, "droplet-action", verb, append([]string{strconv.FormatInt(id, 10)}, args...)...); err != nil {
		return nil, c.scoped(err)
	}
	if len(acts) != 1 {
		return nil, fmt.Errorf("doctl compute droplet-action %s printed %d actions, not one", verb, len(acts))
	}
	return &acts[0], nil
}

// waitAction polls an action of a Droplet until it is completed. An action
// DigitalOcean reports as errored, or one still running after timeout, is
// an error.
func (c *Client) waitAction(ctx context.Context, droplet int64, a *Action, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	cur := *a
	for i := 0; ; i++ {
		switch cur.Status {
		case "completed":
			return nil
		case "errored":
			return fmt.Errorf("DigitalOcean reports that the %s of Droplet %d failed (action %d)", cur.Type, droplet, cur.ID)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the %s of Droplet %d is still %s after %s (action %d)", cur.Type, droplet, cur.Status, timeout.Round(time.Second), cur.ID)
		}
		// The answer to the call that started it always says in-progress:
		// the first look comes at once.
		if i > 0 {
			if err := wait(ctx, poll); err != nil {
				return err
			}
		} else if err := ctx.Err(); err != nil {
			return err
		}
		var acts []Action
		if err := c.compute(ctx, &acts, "droplet-action", "get", strconv.FormatInt(droplet, 10), "--action-id", strconv.FormatInt(cur.ID, 10)); err != nil {
			return err
		}
		if len(acts) == 1 {
			cur = acts[0]
		}
	}
}

// current is the box's Droplet as DigitalOcean reports it now, after the
// check that keelbox created it for the box.
func (c *Client) current(ctx context.Context, d *Droplet, box string) (*Droplet, error) {
	if !d.Managed(box) {
		return nil, fmt.Errorf("refusing to change Droplet %d: not named %s and tagged %s and %s", d.ID, box, TagManaged, BoxTag(box))
	}
	cur, err := c.Droplet(ctx, d.ID)
	if err != nil {
		return nil, err
	}
	if cur == nil {
		return nil, fmt.Errorf("Droplet %d is gone", d.ID)
	}
	if !cur.Managed(box) {
		return nil, fmt.Errorf("refusing to change Droplet %d: it is no longer named %s and tagged %s and %s", d.ID, box, TagManaged, BoxTag(box))
	}
	return cur, nil
}

// waitStatus polls the Droplet until it reports status, or the time is up.
func (c *Client) waitStatus(ctx context.Context, id int64, status string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		cur, err := c.Droplet(ctx, id)
		if err != nil {
			return err
		}
		if cur == nil {
			return fmt.Errorf("Droplet %d is gone", id)
		}
		if cur.Status == status {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("Droplet %d is %s, not %s, after %s", id, cur.Status, status, timeout.Round(time.Second))
		}
		if err := wait(ctx, poll); err != nil {
			return err
		}
	}
}

// Stop shuts the box's Droplet down and returns once DigitalOcean reports
// it off. It asks the operating system first, so services stop in order
// and the disk is written out; a Droplet still running after stopTimeout
// has its power cut, and logf says so. A Droplet already off is left as it
// is. A Droplet that is off is still billed.
func Stop(ctx context.Context, cl *Client, box string, d *Droplet, logf func(string, ...any)) error {
	cur, err := cl.current(ctx, d, box)
	if err != nil {
		return err
	}
	if cur.Status == "off" {
		return nil
	}
	// The action itself is not waited for: it ends when the Droplet is off,
	// and fails by itself, minutes later, when the system ignores it.
	if _, err := cl.dropletAction(ctx, d.ID, "shutdown"); err != nil {
		return err
	}
	if err := cl.waitStatus(ctx, d.ID, "off", stopTimeout); err == nil {
		return nil
	} else if ctx.Err() != nil {
		return err
	}
	logf("Droplet %d did not shut down within %s; switching it off, which DigitalOcean takes up to five minutes over", d.ID, stopTimeout.Round(time.Second))
	act, err := cl.dropletAction(ctx, d.ID, "power-off")
	if err != nil {
		return err
	}
	if err := cl.waitAction(ctx, d.ID, act, actionTimeout); err != nil {
		return err
	}
	return cl.waitStatus(ctx, d.ID, "off", time.Minute)
}

// Start powers the box's Droplet on and returns it once it is active and
// has its address, which is the one it had: a Droplet keeps its addresses
// while it is off. A Droplet already active is returned as it is.
func Start(ctx context.Context, cl *Client, box string, d *Droplet) (*Droplet, error) {
	cur, err := cl.current(ctx, d, box)
	if err != nil {
		return nil, err
	}
	if cur.Status != "active" {
		act, err := cl.dropletAction(ctx, d.ID, "power-on")
		if err != nil {
			return nil, err
		}
		if err := cl.waitAction(ctx, d.ID, act, actionTimeout); err != nil {
			return nil, err
		}
	}
	return cl.waitActive(ctx, cur)
}

// ResizeOffers lists the sizes the Droplet can change to and back from,
// cheapest first, its own size included: the sizes that can be ordered in
// its region whose disk is at least the Droplet's disk now. Resize never
// grows the disk, so that is the disk the Droplet was created with, and
// DigitalOcean refuses a size with a smaller one.
func ResizeOffers(sizes []Size, d *Droplet) []Offer {
	disk := d.Disk
	if disk == 0 {
		// A Droplet whose disk DigitalOcean does not report: its size's.
		for _, s := range sizes {
			if s.Slug == d.SizeSlug {
				disk = s.Disk
			}
		}
	}
	var out []Offer
	for _, s := range sizes {
		if s.Slug != d.SizeSlug && (!s.In(d.Region.Slug) || s.Disk < disk) {
			continue
		}
		out = append(out, Offer{Size: s})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Size.PriceMonthly != out[j].Size.PriceMonthly {
			return out[i].Size.PriceMonthly < out[j].Size.PriceMonthly
		}
		return out[i].Size.Slug < out[j].Size.Slug
	})
	return out
}

// Resize changes the size of the box's Droplet, which must be off, and
// returns it, still off, once DigitalOcean has done it. Only the vCPUs and
// the memory change: the disk keeps its size (no --resize-disk), so a
// later change to a size with a smaller disk stays possible. When it fails
// the Droplet is still its old size.
func Resize(ctx context.Context, cl *Client, box string, d *Droplet, size string) (*Droplet, error) {
	cur, err := cl.current(ctx, d, box)
	if err != nil {
		return nil, err
	}
	if cur.Status != "off" {
		return nil, fmt.Errorf("Droplet %d is %s; its size is changed only while it is off", d.ID, cur.Status)
	}
	act, err := cl.dropletAction(ctx, d.ID, "resize", "--size", size)
	if err != nil {
		return nil, err
	}
	if err := cl.waitAction(ctx, d.ID, act, actionTimeout); err != nil {
		return nil, err
	}
	cur, err = cl.current(ctx, d, box)
	if err != nil {
		return nil, err
	}
	if cur.SizeSlug != size {
		return nil, fmt.Errorf("DigitalOcean reports the resize of Droplet %d done, but its size is %s, not %s", d.ID, cur.SizeSlug, size)
	}
	return cur, nil
}

// startedSnapshot finds the image a snapshot action just started: the
// snapshot of that name that was not there before. DigitalOcean lists it
// as pending from the moment the action starts; the action itself does not
// name it.
func (c *Client) startedSnapshot(ctx context.Context, name string, before map[int64]bool) (*Image, error) {
	for i := 0; ; i++ {
		imgs, err := c.UserImages(ctx)
		if err != nil {
			return nil, err
		}
		for j := range imgs {
			if img := &imgs[j]; img.Type == "snapshot" && img.Name == name && !before[img.ID] {
				return img, nil
			}
		}
		if i == 20 {
			return nil, fmt.Errorf("DigitalOcean lists no new snapshot named %s", name)
		}
		if err := wait(ctx, poll); err != nil {
			return nil, err
		}
	}
}

// Snapshot saves the disk of the box's Droplet, which must be off, as the
// box's parked snapshot, and returns it only once DigitalOcean reports it
// available: complete, stored in the Droplet's region, and usable to create
// a Droplet from. When that does not happen, the unfinished snapshot is
// deleted, also when ctx was cancelled, and the error returned.
//
// DigitalOcean takes no tags in the call that starts a snapshot. The
// snapshot is listed at once, as pending, and is tagged then, before the
// wait: it carries the box's tags for all but the moment between those
// calls.
func Snapshot(ctx context.Context, cl *Client, c *config.Config, d *Droplet) (*Image, error) {
	cur, err := cl.current(ctx, d, c.Box)
	if err != nil {
		return nil, err
	}
	if cur.Status != "off" {
		return nil, fmt.Errorf("Droplet %d is %s; a snapshot is taken only of a Droplet that is off", d.ID, cur.Status)
	}
	tags := ParkedTags(c)
	for _, t := range tags {
		if err := cl.ensureTag(ctx, t); err != nil {
			return nil, err
		}
	}
	had, err := cl.UserImages(ctx)
	if err != nil {
		return nil, err
	}
	before := map[int64]bool{}
	for _, img := range had {
		before[img.ID] = true
	}
	name := SnapshotName(c.Box)
	act, err := cl.dropletAction(ctx, d.ID, "snapshot", "--snapshot-name", name)
	if err != nil {
		return nil, err
	}
	// From here an unfinished snapshot exists: it is found, and on any
	// failure deleted, also when ctx was cancelled.
	held := context.WithoutCancel(ctx)
	img, err := cl.startedSnapshot(held, name, before)
	if err != nil {
		return nil, fmt.Errorf("the snapshot of Droplet %d was started but not found: %w. If one named %s appears in the DigitalOcean control panel (Backups & Snapshots), delete it there", d.ID, err, name)
	}
	fail := func(err error) (*Image, error) {
		if derr := cl.deleteImage(held, img.ID, tags); derr != nil {
			return nil, fmt.Errorf("%w (and the unfinished snapshot %d could not be deleted: %v; delete it in the DigitalOcean control panel, Backups & Snapshots)", err, img.ID, derr)
		}
		return nil, err
	}
	for _, t := range tags {
		if err := cl.tagImage(held, t, img.ID); err != nil {
			return fail(fmt.Errorf("tag snapshot %d: %w", img.ID, err))
		}
	}
	if err := cl.waitAction(ctx, d.ID, act, snapshotTimeout); err != nil {
		return fail(fmt.Errorf("snapshot %d: %w", img.ID, err))
	}
	deadline := time.Now().Add(settleTimeout)
	for {
		got, err := cl.Image(ctx, strconv.FormatInt(img.ID, 10))
		if err != nil {
			return fail(fmt.Errorf("snapshot %d: %w", img.ID, err))
		}
		if got == nil {
			return fail(fmt.Errorf("snapshot %d is gone", img.ID))
		}
		if got.Status == "available" {
			switch {
			case !got.Parked(c.Box):
				return fail(fmt.Errorf("snapshot %d is not the parked snapshot keelbox asked for (type %s, tags %s)", got.ID, got.Type, strings.Join(got.Tags, ", ")))
			case !contains(got.Regions, cur.Region.Slug):
				return fail(fmt.Errorf("snapshot %d is not stored in %s, the region of the Droplet", got.ID, cur.Region.Slug))
			case got.Created().IsZero():
				return fail(fmt.Errorf("snapshot %d has no creation time", got.ID))
			}
			return got, nil
		}
		if time.Now().After(deadline) {
			return fail(fmt.Errorf("snapshot %d is still %s, %s after DigitalOcean reported it taken", img.ID, got.Status, settleTimeout.Round(time.Second)))
		}
		if err := wait(ctx, poll); err != nil {
			return fail(err)
		}
	}
}

// Parked returns the box's parked snapshot: the newest available snapshot
// tagged as it, or nil when the box is not parked.
func Parked(ctx context.Context, cl *Client, box string) (*Image, error) {
	imgs, err := cl.UserImages(ctx)
	if err != nil {
		return nil, err
	}
	var newest *Image
	for i := range imgs {
		img := &imgs[i]
		if !img.Parked(box) || img.Status != "available" || img.Created().IsZero() {
			continue
		}
		if newest == nil || img.Created().After(newest.Created()) {
			newest = img
		}
	}
	return newest, nil
}

// DeleteSnapshot deletes a parked snapshot of the box, then the tags that
// nothing carries any more. It refuses an image that is not a snapshot
// with the box's parked tags.
func DeleteSnapshot(ctx context.Context, cl *Client, box string, img *Image) error {
	if !img.Parked(box) {
		return fmt.Errorf("refusing to delete image %d: not a snapshot tagged %s, %s and %s", img.ID, TagManaged, BoxTag(box), TagParked)
	}
	if err := cl.deleteImage(ctx, img.ID, img.Tags); err != nil {
		return err
	}
	// A tag left behind costs nothing, and the next destroy removes it.
	_ = cl.dropParkedTags(ctx, box)
	return nil
}

// dropParkedTags deletes the tags a deleted parked snapshot leaves without
// a resource. The box's own tags go only when the box has no firewall
// either: a firewall applies to the box's tag and is no resource of it,
// and a parked box keeps its firewall for the Droplet resume creates.
func (c *Client) dropParkedTags(ctx context.Context, box string) error {
	names := []string{TagParked}
	if f, err := FindFirewall(ctx, c, box); err == nil && f == nil {
		names = append(names, BoxTag(box), TagManaged)
	}
	return c.dropUnusedTags(ctx, names...)
}

// SweepSnapshots deletes every snapshot tagged as the box's parked
// snapshot, whatever its state, and returns the ones deleted: what is left
// of a box that is being destroyed. While a Droplet carries the box's tag
// it deletes nothing: a snapshot beside a Droplet may be one a park is
// taking now.
func SweepSnapshots(ctx context.Context, cl *Client, box string) ([]Image, error) {
	imgs, err := cl.UserImages(ctx)
	if err != nil {
		return nil, err
	}
	var parked []Image
	for _, img := range imgs {
		if img.Parked(box) {
			parked = append(parked, img)
		}
	}
	if len(parked) == 0 {
		return nil, nil
	}
	left, err := cl.DropletsTagged(ctx, BoxTag(box))
	if err != nil {
		return nil, err
	}
	for _, d := range left {
		if d.Status != "archive" {
			return nil, fmt.Errorf("Droplet %s (id %d) still carries the tag %s, so its snapshots stay", d.Name, d.ID, BoxTag(box))
		}
	}
	var done []Image
	for i := range parked {
		if err := DeleteSnapshot(ctx, cl, box, &parked[i]); err != nil {
			return done, err
		}
		done = append(done, parked[i])
	}
	return done, nil
}

// Release deletes the box's Droplet and returns once it is gone. Its
// addresses go with it. The firewall, the tags and any snapshot stay:
// they are what resume creates the next Droplet from and behind.
func Release(ctx context.Context, cl *Client, d *Droplet, box string) error {
	_, err := cl.deleteDroplet(ctx, d, box, "run keelbox park "+box+" again")
	return err
}

// DigitalOcean has no way to create a Droplet behind a firewall. It
// applies one to a new Droplet only once the Droplet is active, seconds
// later, and for a firewall that names a tag it first has to find the
// Droplet by the tag, which can take another half minute: until then
// nothing of the firewall is in front of the Droplet. So a
// Droplet created from a snapshot, which boots with everything on its disk
// listening, is named in the firewall by id at once, before it has booted
// (nameInFirewall): that leaves only the first of those waits, and makes
// the firewall list the Droplet as pending until it is applied, which
// firewallApplied waits for.

// nameInFirewall adds the Droplet to the firewall by id.
func (c *Client) nameInFirewall(ctx context.Context, f *Firewall, d *Droplet) error {
	if err := c.compute(ctx, nil, "firewall", "add-droplets", f.ID, "--droplet-ids", strconv.FormatInt(d.ID, 10)); err != nil {
		return fmt.Errorf("cloud firewall: %w", err)
	}
	return nil
}

// firewallApplied returns once DigitalOcean reports the firewall applied
// to the Droplet it names: listed, and no longer pending.
func (c *Client) firewallApplied(ctx context.Context, f *Firewall, d *Droplet) error {
	deadline := time.Now().Add(firewallTimeout)
	for {
		var fws []Firewall
		if err := c.compute(ctx, &fws, "firewall", "get", f.ID); err != nil {
			return fmt.Errorf("cloud firewall: %w", err)
		}
		if len(fws) == 1 {
			cur, listed, pending := fws[0], false, false
			for _, n := range cur.DropletIDs {
				listed = listed || n == d.ID
			}
			for _, p := range cur.Pending {
				pending = pending || p.DropletID == d.ID
			}
			switch {
			case cur.Status == "failed":
				return fmt.Errorf("DigitalOcean reports that applying the firewall %s to Droplet %d failed", f.Name, d.ID)
			case listed && !pending && cur.Status == "succeeded":
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("DigitalOcean does not report the firewall %s applied to Droplet %d after %s", f.Name, d.ID, firewallTimeout.Round(time.Second))
		}
		if err := wait(ctx, poll); err != nil {
			return err
		}
	}
}

// resumeUserData is the user data of a Droplet created from a parked
// snapshot: provider.ResumeUserData, and the two settings the Droplet was
// created with (provider.AdminUserData). DigitalOcean's own cloud-init
// configuration for every Droplet turns SSH passwords and root's keys on,
// and cloud-init runs again on a new Droplet; with these it writes what
// the disk already says.
func resumeUserData(box string) string {
	return provider.ResumeUserData(box) + "ssh_pwauth: false\ndisable_root: true\n"
}

// Restore creates the box's Droplet again from its parked snapshot: the
// name, size, region and tags of the config, and user data that keeps what
// is on the disk (resumeUserData). The box's firewall exists before the
// Droplet, applies to its tag and names it by id as soon as it is created;
// Restore returns the Droplet once it is active, has an address, and
// DigitalOcean reports the firewall applied to it (firewallApplied). Between
// the Droplet's boot and that report, seconds, the firewall on the disk
// and sshd's settings are all that is in front of the box. Restore fails
// when a Droplet of that name exists. A Droplet returned with an error may
// exist.
//
// As at create, no key is added to the account, and one that is there and
// is one of the admin's is named, because DigitalOcean otherwise sets a
// root password for the new Droplet. Root's keys are written as they were
// at create (disable_root), and sshd refuses root whatever its keys.
func Restore(ctx context.Context, cl *Client, c *config.Config, img *Image, logf func(string, ...any)) (*Droplet, error) {
	d := c.Target.DigitalOcean
	if !img.Parked(c.Box) {
		return nil, fmt.Errorf("refusing to create %s from image %d: not its parked snapshot", c.Box, img.ID)
	}
	named, err := cl.DropletsNamed(ctx, c.Box)
	if err != nil {
		return nil, err
	}
	if len(named) > 0 {
		return nil, fmt.Errorf("a Droplet named %s already exists (id %d)", c.Box, named[0].ID)
	}
	if !contains(img.Regions, d.Region) {
		return nil, fmt.Errorf("the parked snapshot %d is stored in %s and target.digitalocean.region is %s; set region to %s, then run keelbox resume %s again",
			img.ID, strings.Join(img.Regions, ", "), d.Region, strings.Join(img.Regions, " or "), c.Box)
	}
	sizes, err := cl.Sizes(ctx)
	if err != nil {
		return nil, err
	}
	var size *Size
	var fits []string
	for _, o := range Offers(sizes, d.Region) {
		if o.Size.Slug == d.Size {
			s := o.Size
			size = &s
		}
		if o.Size.Disk >= img.MinDiskSize && len(fits) < 8 {
			fits = append(fits, o.Size.Slug)
		}
	}
	another := fmt.Sprintf("set target.digitalocean.size to one with a disk of %d GB or more, for example: %s, then run keelbox resume %s again", img.MinDiskSize, strings.Join(fits, ", "), c.Box)
	switch {
	case size == nil:
		return nil, fmt.Errorf("target.digitalocean.size %q cannot be ordered in %s now; the snapshot is kept. Try again later, or %s", d.Size, d.Region, another)
	case size.Disk < img.MinDiskSize:
		return nil, fmt.Errorf("size %s has a %d GB disk and the parked snapshot needs %d GB; %s", size.Slug, size.Disk, img.MinDiskSize, another)
	}
	keys, err := c.Users.Admin.AllKeys()
	if err != nil {
		return nil, err
	}
	// As at create: the firewall exists before the Droplet. One that is
	// there keeps its rules, so a box that had closed public SSH stays
	// closed.
	fw, changes, err := EnsureFirewall(ctx, cl, c.Box, nil)
	if err != nil {
		return nil, fmt.Errorf("cloud firewall: %w", err)
	}
	for _, ch := range changes {
		logf("%s", ch)
	}
	path, err := writeUserData(resumeUserData(c.Box))
	if err != nil {
		return nil, fmt.Errorf("write the user data file: %w", err)
	}
	defer func() { _ = os.Remove(path) }()

	logf("creating %s from snapshot %d: %s in %s (about USD %.2f/month before tax)", c.Box, img.ID, size.Slug, d.Region, size.PriceMonthly)
	args := []string{c.Box, "--region", d.Region, "--size", size.Slug, "--image", strconv.FormatInt(img.ID, 10),
		"--tag-names", strings.Join(Tags(c), ","), "--user-data-file", path}
	if id := cl.accountKey(ctx, keys); id != 0 {
		args = append(args, "--ssh-keys", strconv.FormatInt(id, 10))
	}
	var made []Droplet
	if err := cl.compute(ctx, &made, "droplet", "create", args...); err != nil {
		return nil, err
	}
	if len(made) != 1 {
		return &Droplet{Name: c.Box}, fmt.Errorf("doctl compute droplet create printed %d Droplets, not one", len(made))
	}
	if err := cl.nameInFirewall(ctx, fw, &made[0]); err != nil {
		return &made[0], err
	}
	dr, err := cl.waitActive(ctx, &made[0])
	if err != nil {
		return dr, err
	}
	if err := cl.firewallApplied(ctx, fw, dr); err != nil {
		return dr, fmt.Errorf("Droplet %d runs, but %w. Until it is, only the box's own firewall is in front of it; look at it: doctl compute firewall get %s%s",
			dr.ID, err, fw.ID, cl.contextFlag())
	}
	return dr, nil
}
