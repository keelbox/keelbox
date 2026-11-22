package hetzner

import (
	"context"
	"crypto/md5" //nolint:gosec // G501: Hetzner names keys by their MD5 fingerprint; nothing is secured by it
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// What keelbox up needs from the API before a box exists: check a token,
// list what can be ordered where, and put the user's SSH key in the
// project (docs/adr/0025-keelbox-up.md).

// ConsoleURL is where a user creates a project API token: pick the project,
// then Security, API tokens. Hetzner Console moved here from
// console.hetzner.cloud in June 2025, which redirects.
const ConsoleURL = "https://console.hetzner.com/projects"

// TokenCheck is what a token was found to allow.
type TokenCheck struct {
	// Servers in the project, and how many keelbox did not create.
	Servers, Unmanaged int
}

// ErrTokenRefused is a token the API does not accept (401).
var ErrTokenRefused = errors.New("the Hetzner API refused the token (HTTP 401): it was mistyped, deleted, or belongs to no project")

// ErrTokenReadOnly is a token without write access (403 on a write).
var ErrTokenReadOnly = errors.New("the token is read-only: keelbox creates servers, keys and firewalls, so create one with Read & Write permissions")

// CheckToken confirms a token works and can write. The write check sends
// an empty SSH key, which the API rejects as invalid input when the token
// may write and as forbidden when it may only read; nothing is created.
func (c *Client) CheckToken(ctx context.Context) (TokenCheck, error) {
	servers, err := c.Servers(ctx, "")
	var ae *APIError
	if errors.As(err, &ae) && ae.Status == 401 {
		return TokenCheck{}, ErrTokenRefused
	}
	if err != nil {
		return TokenCheck{}, err
	}
	tc := TokenCheck{Servers: len(servers)}
	for _, s := range servers {
		if s.Labels[LabelManaged] != "true" {
			tc.Unmanaged++
		}
	}
	err = c.do(ctx, "POST", "/ssh_keys", map[string]any{}, nil)
	switch {
	case errors.As(err, &ae) && ae.Status == 403:
		return tc, ErrTokenReadOnly
	case errors.As(err, &ae) && ae.Status == 401:
		return tc, ErrTokenRefused
	case errors.As(err, &ae) && (ae.Status == 400 || ae.Status == 422):
		return tc, nil // invalid_input: the token may write
	case err == nil:
		return tc, errors.New("the API accepted an empty SSH key; refusing to go on")
	}
	return tc, err
}

// MD5Fingerprint is a public key's fingerprint as Hetzner shows and
// filters it: colon-separated MD5 hex of the key blob.
func MD5Fingerprint(publicKey string) (string, error) {
	f := strings.Fields(publicKey)
	if len(f) < 2 {
		return "", errors.New("not an OpenSSH public key line")
	}
	blob, err := base64.StdEncoding.DecodeString(f[1])
	if err != nil {
		return "", fmt.Errorf("not an OpenSSH public key line: %w", err)
	}
	sum := md5.Sum(blob) //nolint:gosec // G401: an identifier, as Hetzner computes it
	hex := make([]string, len(sum))
	for i, b := range sum {
		hex[i] = fmt.Sprintf("%02x", b)
	}
	return strings.Join(hex, ":"), nil
}

// SSHKeyByFingerprint returns the project key with this MD5 fingerprint,
// or nil.
func (c *Client) SSHKeyByFingerprint(ctx context.Context, fingerprint string) (*SSHKey, error) {
	var r struct {
		SSHKeys []SSHKey `json:"ssh_keys"`
	}
	if err := c.do(ctx, "GET", "/ssh_keys?fingerprint="+url.QueryEscape(fingerprint), nil, &r); err != nil {
		return nil, err
	}
	if len(r.SSHKeys) == 0 {
		return nil, nil
	}
	return &r.SSHKeys[0], nil
}

// EnsureSSHKey puts a public key in the project and returns its project
// key. A key already there with the same fingerprint is reused whatever
// its name (Hetzner refuses a second copy); otherwise it is uploaded as
// name, or name-2, name-3 when another key has that name.
func EnsureSSHKey(ctx context.Context, cl *Client, name, publicKey string) (*SSHKey, bool, error) {
	fp, err := MD5Fingerprint(publicKey)
	if err != nil {
		return nil, false, err
	}
	if k, err := cl.SSHKeyByFingerprint(ctx, fp); err != nil || k != nil {
		return k, false, err
	}
	candidate := name
	for i := 2; i < 10; i++ {
		k, err := cl.SSHKeyByName(ctx, candidate)
		if err != nil {
			return nil, false, err
		}
		if k == nil {
			break
		}
		candidate = name + "-" + strconv.Itoa(i)
	}
	k, err := cl.CreateSSHKey(ctx, candidate, strings.TrimSpace(publicKey), map[string]string{LabelManaged: "true"})
	if err != nil {
		return nil, false, err
	}
	return k, true, nil
}

// Offer is one server type that can be ordered in a location, with its
// prices there.
type Offer struct {
	Type            ServerType
	Hourly, Monthly float64 // EUR, gross
}

// Describe is a one-line summary: cores, memory, disk and price.
func (o Offer) Describe() string {
	cpu := "shared"
	if o.Type.CPUType == "dedicated" {
		cpu = "dedicated"
	}
	if o.Arm() {
		cpu += ", Arm64"
	}
	d := fmt.Sprintf("%d vCPU (%s), %g GB, %d GB disk, EUR %.2f/month (EUR %.4f/h)",
		o.Type.Cores, cpu, o.Type.Memory, o.Type.Disk, o.Monthly, o.Hourly)
	if o.Small() {
		d += "; small for agents"
	}
	return d
}

// Arm reports whether the type is an Arm64 one. keelbox sets such a box up
// as it does an x86 one (ADR 0087); what differs is the user's own software,
// whose container images and binaries need an arm64 build.
func (o Offer) Arm() bool { return archFor(o.Type) == "arm" }

// Small is a type below 2 vCPU or 4 GB, where a dev server, tests and a
// headless browser together run out of memory; doctor warns on such a box
// (modules.SmallHost).
func (o Offer) Small() bool { return o.Type.Cores < 2 || o.Type.Memory < 4 }

// Offers lists the server types that can be ordered in a location now, x86
// and Arm64, cheapest first. Types with a deprecation announced there are
// left out.
func Offers(types []ServerType, location string) []Offer {
	var out []Offer
	for _, t := range types {
		if t.Deprecated || !t.AvailableIn(location) || t.deprecatedIn(location) {
			continue
		}
		out = append(out, offerIn(t, location))
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Monthly != out[j].Monthly {
			return out[i].Monthly < out[j].Monthly
		}
		// At one price, x86 first: it is the default's architecture.
		if out[i].Arm() != out[j].Arm() {
			return !out[i].Arm()
		}
		return out[i].Type.Name < out[j].Type.Name
	})
	return out
}

func (t ServerType) deprecatedIn(location string) bool {
	for _, l := range t.Locations {
		if l.Name == location && l.Deprecation != nil {
			return true
		}
	}
	return false
}

// DefaultOffer is the cheapest x86 offer with at least 2 vCPUs and 4 GB, the
// size docs/guides/where-to-run.md gives as enough for one or two agents.
// x86 because it is what a project's own images and binaries most often
// are: Arm64 is a choice the user makes. With no such x86 offer it is the
// cheapest Arm64 one of that size, and else the cheapest offer.
func DefaultOffer(offers []Offer) int {
	arm := -1
	for i, o := range offers {
		if o.Type.Cores < 2 || o.Type.Memory < 4 {
			continue
		}
		if !o.Arm() {
			return i
		}
		if arm < 0 {
			arm = i
		}
	}
	if arm >= 0 {
		return arm
	}
	return 0
}
