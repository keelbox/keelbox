package provider

import (
	"errors"
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"
)

// What every cloud provider shares: the labels that mark what keelbox
// created, and the user data a new server boots with.

// Label keys keelbox puts on every cloud resource it creates (labels on
// Hetzner, tags on AWS). A provider's Find and Destroy refuse resources
// without them, so keelbox never deletes what it did not make.
const (
	LabelManaged = "keelbox"
	LabelBox     = "keelbox-box"
	// LabelParked marks the snapshot keelbox park keeps of a box's disk
	// (docs/adr/0071-resize-park-resume.md). The parked state lives in the
	// cloud: any computer with the box's config finds the snapshot by it.
	LabelParked = "keelbox-parked"
)

// ManagedLabels are the labels for a box's resources: the user's extra
// labels, then keelbox's own, which win.
func ManagedLabels(box string, extra map[string]string) map[string]string {
	l := map[string]string{}
	for k, v := range extra {
		l[k] = v
	}
	l[LabelManaged] = "true"
	l[LabelBox] = box
	return l
}

// Managed reports whether labels mark a resource keelbox created for box.
func Managed(labels map[string]string, box string) bool {
	return labels[LabelManaged] == "true" && labels[LabelBox] == box
}

// NoServerFix ends the error for a box whose cloud has no server of its
// name with the two ways on: create the server, or stop managing the box
// from this computer.
func NoServerFix(box string) string {
	return "create it: keelbox up " + box + ", or remove the box from keelbox: keelbox forget " + box
}

// NoServerError is a provider's find answering that the cloud has no
// server of the box's name.
type NoServerError struct{ Msg string }

func (e *NoServerError) Error() string { return e.Msg }

// NoServer reports whether err says the cloud has no server of the box's
// name, as opposed to one keelbox did not create or a failed lookup.
func NoServer(err error) bool {
	var e *NoServerError
	return errors.As(err, &e)
}

// NotOrderedError is a create that failed before it asked the cloud for a
// server: nothing was created for the box, and where it is created may
// still change.
type NotOrderedError struct{ Err error }

func (e *NotOrderedError) Error() string { return e.Err.Error() }
func (e *NotOrderedError) Unwrap() error { return e.Err }

// NotOrdered reports whether err is such a failure.
func NotOrdered(err error) bool {
	var e *NotOrderedError
	return errors.As(err, &e)
}

// ParkedLabels are the labels for a box's parked snapshot: the box's own
// and the parked marker.
func ParkedLabels(box string, extra map[string]string) map[string]string {
	l := ManagedLabels(box, extra)
	l[LabelParked] = "true"
	return l
}

// Parked reports whether labels mark the parked snapshot of box.
func Parked(labels map[string]string, box string) bool {
	return Managed(labels, box) && labels[LabelParked] == "true"
}

// ResumeUserData is the cloud-init document for the server keelbox resume
// creates from a parked box's snapshot. The disk already holds the box, so
// it only tells cloud-init, which runs again on a new server, to leave
// what is there: the SSH host keys this laptop has pinned, the hostname,
// and the accounts. An image's own cloud-init configuration may name a
// default account to create ("ubuntu" with passwordless sudo, on some
// clouds), which a box may never have had; the empty users list makes
// cloud-init create none. It adds no key and holds nothing secret.
func ResumeUserData(box string) string {
	return "#cloud-config\n# keelbox box " + box + ", resumed from its parked snapshot: the disk is the box as it was parked.\n" +
		"ssh_deletekeys: false\npreserve_hostname: true\nusers: []\n"
}

// UserData is the cloud-init document for a server keelbox creates. It
// installs a host key generated on the laptop, so the very first
// connection is checked against a key keelbox already holds instead of
// trusted on first use. A cloud's metadata service serves user data to
// every process on the server, so this key is single-use: keelbox rotates
// it over the first trusted connection (SSH.RotateHostKeys). Nothing else
// here is secret; keelbox and the config arrive over SSH.
func UserData(box, hostname string, hk HostKeyPair) (string, error) {
	doc := struct {
		Hostname         string            `yaml:"hostname"`
		PreserveHostname bool              `yaml:"preserve_hostname"`
		PackageUpdate    bool              `yaml:"package_update"`
		SSHDeleteKeys    bool              `yaml:"ssh_deletekeys"`
		SSHGenKeyTypes   []string          `yaml:"ssh_genkeytypes"`
		SSHKeys          map[string]string `yaml:"ssh_keys"`
	}{
		Hostname: hostname, SSHDeleteKeys: true, SSHGenKeyTypes: []string{},
		SSHKeys: map[string]string{"ed25519_private": hk.Private, "ed25519_public": strings.TrimSpace(hk.Public)},
	}
	body, err := yaml.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("render cloud-init: %w", err)
	}
	return "#cloud-config\n# keelbox box " + box + ". keelbox and its config arrive over SSH after first boot.\n" + string(body), nil
}
