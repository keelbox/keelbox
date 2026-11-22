// Package hetzner is a minimal Hetzner Cloud API client: just the calls
// keelbox needs to create, find, resize, park, resume and destroy a box.
// No hcloud CLI needed.
package hetzner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// DefaultEndpoint is the public API.
const DefaultEndpoint = "https://api.hetzner.cloud/v1"

// Client talks to the API. The token is held only in memory and is never
// included in errors or output.
type Client struct {
	Endpoint string
	token    string
	HTTP     *http.Client
}

// New returns a client for a token.
func New(token string) *Client {
	return &Client{Endpoint: DefaultEndpoint, token: strings.TrimSpace(token), HTTP: &http.Client{Timeout: 60 * time.Second}}
}

// TokenFromFile reads a token file, refusing one other users can read.
func TokenFromFile(path string) (string, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("hetzner token: %w (create it with mode 600, or set HCLOUD_TOKEN)", err)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("hetzner token file %s is readable by others (mode %o); chmod 600 it", path, fi.Mode().Perm())
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	t := strings.TrimSpace(string(b))
	if t == "" {
		return "", fmt.Errorf("hetzner token file %s is empty; write the token to it with (umask 077; cat > %s), or remove target.hetzner.token_file to use the credential store", path, path)
	}
	return t, nil
}

// APIError is an error response from the API.
type APIError struct {
	Status  int
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("hetzner api: %d %s: %s", e.Status, e.Code, e.Message)
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Endpoint+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "keelbox")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			return fmt.Errorf("hetzner api %s %s: %w", method, path, ue.Err)
		}
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Error APIError `json:"error"`
		}
		_ = json.Unmarshal(data, &e)
		e.Error.Status = resp.StatusCode
		return &e.Error
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

// Price is one location's price for a server type.
type Price struct {
	Location     string `json:"location"`
	PriceHourly  Amount `json:"price_hourly"`
	PriceMonthly Amount `json:"price_monthly"`
}

// Amount is a price in EUR, as decimal strings.
type Amount struct {
	Net   string `json:"net"`
	Gross string `json:"gross"`
}

// ServerType is a server plan.
type ServerType struct {
	ID           int64   `json:"id"`
	Name         string  `json:"name"`
	Description  string  `json:"description"`
	Cores        int     `json:"cores"`
	Memory       float64 `json:"memory"`
	Disk         int     `json:"disk"`
	CPUType      string  `json:"cpu_type"`
	Architecture string  `json:"architecture"`
	Deprecated   bool    `json:"deprecated"`
	Prices       []Price `json:"prices"`
	// Locations says where the type can be ordered now. Hetzner keeps
	// prices for locations it has stopped selling a type in.
	Locations []TypeLocation `json:"locations"`
}

// TypeLocation is one location's availability for a server type.
type TypeLocation struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
	// Deprecation is set once Hetzner has announced the type's end there.
	Deprecation *struct {
		UnavailableAfter string `json:"unavailable_after"`
	} `json:"deprecation"`
}

// AvailableIn reports whether the type can be ordered in location. A
// response without per-location availability falls back to the price list.
func (t ServerType) AvailableIn(location string) bool {
	if _, ok := t.HourlyGross(location); !ok {
		return false
	}
	if len(t.Locations) == 0 {
		return true
	}
	for _, l := range t.Locations {
		if l.Name == location {
			return l.Available
		}
	}
	return false
}

// OrderableIn lists the locations the type can be ordered in.
func (t ServerType) OrderableIn() []string {
	var out []string
	for _, p := range t.Prices {
		if t.AvailableIn(p.Location) {
			out = append(out, p.Location)
		}
	}
	return out
}

// Location is a datacentre location.
type Location struct {
	Name        string `json:"name"`
	City        string `json:"city"`
	Description string `json:"description"`
	NetworkZone string `json:"network_zone"`
	Country     string `json:"country"`
}

// Image is an OS image, or a snapshot of a server's disk.
type Image struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	Architecture string `json:"architecture"`
	Deprecated   string `json:"deprecated"`
	// The rest is set on snapshots. Status is creating, available or
	// unavailable; ImageSize is what the snapshot stores and is billed
	// for, in GB; DiskSize is the smallest disk a server made from it needs.
	Type        string            `json:"type"`
	Status      string            `json:"status"`
	Description string            `json:"description"`
	ImageSize   float64           `json:"image_size"`
	DiskSize    float64           `json:"disk_size"`
	Created     time.Time         `json:"created"`
	Labels      map[string]string `json:"labels"`
}

// SSHKey is a project SSH key.
type SSHKey struct {
	ID          int64             `json:"id"`
	Name        string            `json:"name"`
	Fingerprint string            `json:"fingerprint"`
	PublicKey   string            `json:"public_key"`
	Labels      map[string]string `json:"labels"`
}

// Server is a cloud server.
type Server struct {
	ID        int64             `json:"id"`
	Name      string            `json:"name"`
	Status    string            `json:"status"`
	Labels    map[string]string `json:"labels"`
	Created   time.Time         `json:"created"`
	PublicNet struct {
		IPv4 struct {
			IP string `json:"ip"`
		} `json:"ipv4"`
		IPv6 struct {
			IP string `json:"ip"`
		} `json:"ipv6"`
	} `json:"public_net"`
	ServerType ServerType `json:"server_type"`
	Image      *Image     `json:"image"`
	// PrimaryDiskSize is the disk's size in GB. It stays what it was when
	// the server type changes without upgrade_disk.
	PrimaryDiskSize int `json:"primary_disk_size"`
	// Location is where the server runs. Older API responses carry it
	// only inside Datacenter.
	Location   *Location `json:"location"`
	Datacenter *struct {
		Location Location `json:"location"`
	} `json:"datacenter"`
}

// LocationName is the name of the server's location, or "".
func (s *Server) LocationName() string {
	switch {
	case s.Location != nil && s.Location.Name != "":
		return s.Location.Name
	case s.Datacenter != nil:
		return s.Datacenter.Location.Name
	}
	return ""
}

// DiskGB is the size of the server's disk: its own, or its type's when the
// API does not say.
func (s *Server) DiskGB() int {
	if s.PrimaryDiskSize > 0 {
		return s.PrimaryDiskSize
	}
	return s.ServerType.Disk
}

// Action is an asynchronous API action.
type Action struct {
	ID     int64  `json:"id"`
	Status string `json:"status"`
	Error  *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type pagination struct {
	Meta struct {
		Pagination struct {
			NextPage *int `json:"next_page"`
		} `json:"pagination"`
	} `json:"meta"`
}

// ServerTypes lists every server type.
func (c *Client) ServerTypes(ctx context.Context) ([]ServerType, error) {
	var out []ServerType
	for page := 1; ; {
		var r struct {
			ServerTypes []ServerType `json:"server_types"`
			pagination
		}
		if err := c.do(ctx, "GET", "/server_types?per_page=50&page="+strconv.Itoa(page), nil, &r); err != nil {
			return nil, err
		}
		out = append(out, r.ServerTypes...)
		if r.Meta.Pagination.NextPage == nil {
			return out, nil
		}
		page = *r.Meta.Pagination.NextPage
	}
}

// Locations lists locations.
func (c *Client) Locations(ctx context.Context) ([]Location, error) {
	var r struct {
		Locations []Location `json:"locations"`
	}
	return r.Locations, c.do(ctx, "GET", "/locations?per_page=50", nil, &r)
}

// SystemImage finds a system image by name and architecture (x86, arm).
func (c *Client) SystemImage(ctx context.Context, name, arch string) (*Image, error) {
	var r struct {
		Images []Image `json:"images"`
	}
	q := url.Values{"type": {"system"}, "name": {name}, "architecture": {arch}}
	if err := c.do(ctx, "GET", "/images?"+q.Encode(), nil, &r); err != nil {
		return nil, err
	}
	if len(r.Images) == 0 {
		return nil, nil
	}
	return &r.Images[0], nil
}

// SSHKeyByName returns a project key, or nil.
func (c *Client) SSHKeyByName(ctx context.Context, name string) (*SSHKey, error) {
	var r struct {
		SSHKeys []SSHKey `json:"ssh_keys"`
	}
	if err := c.do(ctx, "GET", "/ssh_keys?name="+url.QueryEscape(name), nil, &r); err != nil {
		return nil, err
	}
	if len(r.SSHKeys) == 0 {
		return nil, nil
	}
	return &r.SSHKeys[0], nil
}

// CreateSSHKey registers a public key.
func (c *Client) CreateSSHKey(ctx context.Context, name, publicKey string, labels map[string]string) (*SSHKey, error) {
	var r struct {
		SSHKey SSHKey `json:"ssh_key"`
	}
	body := map[string]any{"name": name, "public_key": publicKey, "labels": labels}
	return &r.SSHKey, c.do(ctx, "POST", "/ssh_keys", body, &r)
}

// DeleteSSHKey removes a project key. Servers created with it keep it in
// their authorized_keys.
func (c *Client) DeleteSSHKey(ctx context.Context, id int64) error {
	return c.do(ctx, "DELETE", "/ssh_keys/"+strconv.FormatInt(id, 10), nil, nil)
}

// CreateServerRequest is the subset of server options keelbox sets.
type CreateServerRequest struct {
	Name       string `json:"name"`
	ServerType string `json:"server_type"`
	// Image is a system image's name, or a snapshot's id as a number.
	Image    any               `json:"image"`
	Location string            `json:"location"`
	SSHKeys  []int64           `json:"ssh_keys"`
	Labels   map[string]string `json:"labels"`
	UserData string            `json:"user_data,omitempty"`
	// Firewalls are attached before the server first boots.
	Firewalls []ServerFirewall `json:"firewalls,omitempty"`
}

// ServerFirewall names a firewall for a new server.
type ServerFirewall struct {
	Firewall int64 `json:"firewall"`
}

// CreateServer creates a server and returns it with its create action.
func (c *Client) CreateServer(ctx context.Context, req CreateServerRequest) (*Server, *Action, error) {
	var r struct {
		Server Server `json:"server"`
		Action Action `json:"action"`
	}
	if err := c.do(ctx, "POST", "/servers", req, &r); err != nil {
		return nil, nil, err
	}
	return &r.Server, &r.Action, nil
}

// Servers lists servers matching a label selector ("" for all).
func (c *Client) Servers(ctx context.Context, labelSelector string) ([]Server, error) {
	var out []Server
	for page := 1; ; {
		q := url.Values{"per_page": {"50"}, "page": {strconv.Itoa(page)}}
		if labelSelector != "" {
			q.Set("label_selector", labelSelector)
		}
		var r struct {
			Servers []Server `json:"servers"`
			pagination
		}
		if err := c.do(ctx, "GET", "/servers?"+q.Encode(), nil, &r); err != nil {
			return nil, err
		}
		out = append(out, r.Servers...)
		if r.Meta.Pagination.NextPage == nil {
			return out, nil
		}
		page = *r.Meta.Pagination.NextPage
	}
}

// ServerByName returns a server, or nil.
func (c *Client) ServerByName(ctx context.Context, name string) (*Server, error) {
	var r struct {
		Servers []Server `json:"servers"`
	}
	if err := c.do(ctx, "GET", "/servers?name="+url.QueryEscape(name), nil, &r); err != nil {
		return nil, err
	}
	if len(r.Servers) == 0 {
		return nil, nil
	}
	return &r.Servers[0], nil
}

// Server fetches one server.
func (c *Client) Server(ctx context.Context, id int64) (*Server, error) {
	var r struct {
		Server Server `json:"server"`
	}
	return &r.Server, c.do(ctx, "GET", "/servers/"+strconv.FormatInt(id, 10), nil, &r)
}

// DeleteServer deletes a server.
func (c *Client) DeleteServer(ctx context.Context, id int64) (*Action, error) {
	var r struct {
		Action Action `json:"action"`
	}
	return &r.Action, c.do(ctx, "DELETE", "/servers/"+strconv.FormatInt(id, 10), nil, &r)
}

// serverAction posts an action on a server and returns it.
func (c *Client) serverAction(ctx context.Context, id int64, name string, body any) (*Action, error) {
	var r struct {
		Action Action `json:"action"`
	}
	if body == nil {
		body = map[string]any{}
	}
	return &r.Action, c.do(ctx, "POST", "/servers/"+strconv.FormatInt(id, 10)+"/actions/"+name, body, &r)
}

// Shutdown asks the server's operating system to shut down (an ACPI
// request). The action succeeds once the request is sent: the server is
// off when its status says so.
func (c *Client) Shutdown(ctx context.Context, id int64) (*Action, error) {
	return c.serverAction(ctx, id, "shutdown", nil)
}

// PowerOff cuts the server's power, as pulling the plug would.
func (c *Client) PowerOff(ctx context.Context, id int64) (*Action, error) {
	return c.serverAction(ctx, id, "poweroff", nil)
}

// PowerOn starts a server that is off.
func (c *Client) PowerOn(ctx context.Context, id int64) (*Action, error) {
	return c.serverAction(ctx, id, "poweron", nil)
}

// ChangeType changes the type of a server that is off. With upgradeDisk
// false the disk keeps its size, so the server can change back to a type
// with a smaller disk later.
func (c *Client) ChangeType(ctx context.Context, id int64, serverType string, upgradeDisk bool) (*Action, error) {
	return c.serverAction(ctx, id, "change_type", map[string]any{"server_type": serverType, "upgrade_disk": upgradeDisk})
}

// CreateSnapshot starts a snapshot of the server's disk and returns the
// image, still being created, with the action that creates it.
func (c *Client) CreateSnapshot(ctx context.Context, id int64, description string, labels map[string]string) (*Image, *Action, error) {
	var r struct {
		Image  Image  `json:"image"`
		Action Action `json:"action"`
	}
	body := map[string]any{"type": "snapshot", "description": description, "labels": labels}
	if err := c.do(ctx, "POST", "/servers/"+strconv.FormatInt(id, 10)+"/actions/create_image", body, &r); err != nil {
		return nil, nil, err
	}
	return &r.Image, &r.Action, nil
}

// Image fetches one image.
func (c *Client) Image(ctx context.Context, id int64) (*Image, error) {
	var r struct {
		Image Image `json:"image"`
	}
	return &r.Image, c.do(ctx, "GET", "/images/"+strconv.FormatInt(id, 10), nil, &r)
}

// Snapshots lists the project's snapshots matching a label selector.
func (c *Client) Snapshots(ctx context.Context, labelSelector string) ([]Image, error) {
	var out []Image
	for page := 1; ; {
		q := url.Values{"type": {"snapshot"}, "per_page": {"50"}, "page": {strconv.Itoa(page)}}
		if labelSelector != "" {
			q.Set("label_selector", labelSelector)
		}
		var r struct {
			Images []Image `json:"images"`
			pagination
		}
		if err := c.do(ctx, "GET", "/images?"+q.Encode(), nil, &r); err != nil {
			return nil, err
		}
		out = append(out, r.Images...)
		if r.Meta.Pagination.NextPage == nil {
			return out, nil
		}
		page = *r.Meta.Pagination.NextPage
	}
}

// DeleteImage deletes a snapshot.
func (c *Client) DeleteImage(ctx context.Context, id int64) error {
	return c.do(ctx, "DELETE", "/images/"+strconv.FormatInt(id, 10), nil, nil)
}

// SnapshotPrice is what the project pays for snapshot storage: EUR per GB
// a month, gross.
func (c *Client) SnapshotPrice(ctx context.Context) (float64, error) {
	var r struct {
		Pricing struct {
			Image struct {
				PricePerGBMonth Amount `json:"price_per_gb_month"`
			} `json:"image"`
		} `json:"pricing"`
	}
	if err := c.do(ctx, "GET", "/pricing", nil, &r); err != nil {
		return 0, err
	}
	v, err := strconv.ParseFloat(r.Pricing.Image.PricePerGBMonth.Gross, 64)
	if err != nil || v <= 0 {
		return 0, errors.New("the Hetzner API lists no price for snapshots")
	}
	return v, nil
}

// FirewallRule is one Cloud Firewall rule.
type FirewallRule struct {
	Direction      string   `json:"direction"` // in or out
	Protocol       string   `json:"protocol"`  // tcp, udp, icmp, esp, gre
	Port           string   `json:"port,omitempty"`
	SourceIPs      []string `json:"source_ips,omitempty"`
	DestinationIPs []string `json:"destination_ips,omitempty"`
	Description    string   `json:"description,omitempty"`
}

// FirewallResource is what a firewall applies to.
type FirewallResource struct {
	Type   string `json:"type"` // server
	Server *struct {
		ID int64 `json:"id"`
	} `json:"server,omitempty"`
}

// ServerResource is a FirewallResource for one server.
func ServerResource(id int64) FirewallResource {
	r := FirewallResource{Type: "server"}
	r.Server = &struct {
		ID int64 `json:"id"`
	}{ID: id}
	return r
}

// Firewall is a Cloud Firewall.
type Firewall struct {
	ID        int64              `json:"id"`
	Name      string             `json:"name"`
	Labels    map[string]string  `json:"labels"`
	Rules     []FirewallRule     `json:"rules"`
	AppliedTo []FirewallResource `json:"applied_to"`
}

// AppliedToServer reports whether the firewall is attached to a server.
func (f *Firewall) AppliedToServer(id int64) bool {
	for _, r := range f.AppliedTo {
		if r.Type == "server" && r.Server != nil && r.Server.ID == id {
			return true
		}
	}
	return false
}

// Firewalls lists firewalls matching a label selector ("" for all).
func (c *Client) Firewalls(ctx context.Context, labelSelector string) ([]Firewall, error) {
	var out []Firewall
	for page := 1; ; {
		q := url.Values{"per_page": {"50"}, "page": {strconv.Itoa(page)}}
		if labelSelector != "" {
			q.Set("label_selector", labelSelector)
		}
		var r struct {
			Firewalls []Firewall `json:"firewalls"`
			pagination
		}
		if err := c.do(ctx, "GET", "/firewalls?"+q.Encode(), nil, &r); err != nil {
			return nil, err
		}
		out = append(out, r.Firewalls...)
		if r.Meta.Pagination.NextPage == nil {
			return out, nil
		}
		page = *r.Meta.Pagination.NextPage
	}
}

// FirewallByName returns a firewall, or nil.
func (c *Client) FirewallByName(ctx context.Context, name string) (*Firewall, error) {
	var r struct {
		Firewalls []Firewall `json:"firewalls"`
	}
	if err := c.do(ctx, "GET", "/firewalls?name="+url.QueryEscape(name), nil, &r); err != nil {
		return nil, err
	}
	if len(r.Firewalls) == 0 {
		return nil, nil
	}
	return &r.Firewalls[0], nil
}

// CreateFirewall creates a firewall, applied to the given resources.
func (c *Client) CreateFirewall(ctx context.Context, name string, labels map[string]string, rules []FirewallRule, applyTo []FirewallResource) (*Firewall, []Action, error) {
	var r struct {
		Firewall Firewall `json:"firewall"`
		Actions  []Action `json:"actions"`
	}
	body := map[string]any{"name": name, "labels": labels, "rules": rules}
	if len(applyTo) > 0 {
		body["apply_to"] = applyTo
	}
	if err := c.do(ctx, "POST", "/firewalls", body, &r); err != nil {
		return nil, nil, err
	}
	return &r.Firewall, r.Actions, nil
}

// SetFirewallRules replaces a firewall's rules.
func (c *Client) SetFirewallRules(ctx context.Context, id int64, rules []FirewallRule) ([]Action, error) {
	var r struct {
		Actions []Action `json:"actions"`
	}
	if rules == nil {
		rules = []FirewallRule{}
	}
	return r.Actions, c.do(ctx, "POST", "/firewalls/"+strconv.FormatInt(id, 10)+"/actions/set_rules", map[string]any{"rules": rules}, &r)
}

// ApplyFirewall attaches a firewall to resources.
func (c *Client) ApplyFirewall(ctx context.Context, id int64, to []FirewallResource) ([]Action, error) {
	var r struct {
		Actions []Action `json:"actions"`
	}
	return r.Actions, c.do(ctx, "POST", "/firewalls/"+strconv.FormatInt(id, 10)+"/actions/apply_to_resources", map[string]any{"apply_to": to}, &r)
}

// RemoveFirewall detaches a firewall from resources.
func (c *Client) RemoveFirewall(ctx context.Context, id int64, from []FirewallResource) ([]Action, error) {
	var r struct {
		Actions []Action `json:"actions"`
	}
	return r.Actions, c.do(ctx, "POST", "/firewalls/"+strconv.FormatInt(id, 10)+"/actions/remove_from_resources", map[string]any{"remove_from": from}, &r)
}

// DeleteFirewall deletes a firewall.
func (c *Client) DeleteFirewall(ctx context.Context, id int64) error {
	return c.do(ctx, "DELETE", "/firewalls/"+strconv.FormatInt(id, 10), nil, nil)
}

// WaitActions waits for every action.
func (c *Client) WaitActions(ctx context.Context, acts []Action, poll time.Duration) error {
	for _, a := range acts {
		if err := c.WaitAction(ctx, a.ID, poll); err != nil {
			return err
		}
	}
	return nil
}

// WaitAction polls an action until it finishes.
func (c *Client) WaitAction(ctx context.Context, id int64, poll time.Duration) error {
	for {
		var r struct {
			Action Action `json:"action"`
		}
		if err := c.do(ctx, "GET", "/actions/"+strconv.FormatInt(id, 10), nil, &r); err != nil {
			return err
		}
		switch r.Action.Status {
		case "success":
			return nil
		case "error":
			if r.Action.Error != nil {
				return fmt.Errorf("hetzner action %d failed: %s: %s", id, r.Action.Error.Code, r.Action.Error.Message)
			}
			return fmt.Errorf("hetzner action %d failed", id)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
}

// HourlyGross returns a type's gross hourly price in a location.
func (t ServerType) HourlyGross(location string) (float64, bool) {
	for _, p := range t.Prices {
		if p.Location == location {
			v, err := strconv.ParseFloat(p.PriceHourly.Gross, 64)
			return v, err == nil
		}
	}
	return 0, false
}

// CheapestShared picks the cheapest non-deprecated shared-vCPU type for an
// architecture that can be ordered in one of the given locations.
func CheapestShared(types []ServerType, arch string, locations []string) (ServerType, string, float64, error) {
	var best ServerType
	bestLoc, bestPrice := "", -1.0
	for _, t := range types {
		if t.Deprecated || t.CPUType != "shared" || t.Architecture != arch {
			continue
		}
		for _, loc := range locations {
			if !t.AvailableIn(loc) {
				continue
			}
			if p, ok := t.HourlyGross(loc); ok && (bestPrice < 0 || p < bestPrice) {
				best, bestLoc, bestPrice = t, loc, p
			}
		}
	}
	if bestPrice < 0 {
		return ServerType{}, "", 0, fmt.Errorf("no shared %s server type available in %v", arch, locations)
	}
	return best, bestLoc, bestPrice, nil
}
