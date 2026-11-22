// Package dotest is a fake doctl for tests: an in-memory DigitalOcean
// account behind the same arguments and JSON the doctl command takes and
// prints. As doctl does with --output json, it reports an error as
// {"errors":[{"detail":...,"title":...,"reason":...,"status":...}]} on
// standard output and exits 1.
package dotest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Droplet is a Droplet in the fake account.
type Droplet struct {
	ID                        int64
	Name, Region, Size, Image string
	IP                        string
	Tags                      []string
	// SSHKeys are the account key ids it was created with.
	SSHKeys []string
	// UserData is what the --user-data-file held, and UserDataMode that
	// file's mode.
	UserData     string
	UserDataMode os.FileMode
	// Status is "off" for a Droplet that is shut down; empty is active.
	Status string
	// Disk is its disk in GB: the disk of the size it was created with,
	// which a resize leaves as it is.
	Disk int
	// FromSnapshot is the id of the snapshot it was created from, or 0.
	FromSnapshot int64
	// polls counts "droplet get" calls since creation.
	polls int
}

// Snapshot is a Droplet snapshot in the fake account: an image of the
// account's own.
type Snapshot struct {
	ID        int64
	Name      string
	DropletID int64
	Region    string
	// MinDisk is the disk, in GB, of the Droplet it was taken of.
	MinDisk int
	SizeGB  float64
	Tags    []string
	// Status is "pending" while it is taken, then "available".
	Status  string
	Created string
}

// action is something the fake does to a Droplet over time.
type action struct {
	id, droplet int64
	kind        string
	status      string
	polls       int
	// done is what completing it changes.
	done func()
}

// Firewall is a Cloud Firewall in the fake account. A rule is
// "protocol:ports:address,address".
type Firewall struct {
	ID, Name          string
	Inbound, Outbound []string
	Tags              []string
	DropletIDs        []int64
	// pending counts, for a Droplet just named, the "firewall get" calls
	// that still list it as pending.
	pending map[int64]int
}

// CLI is the fake. Fields may be changed between calls.
type CLI struct {
	mu sync.Mutex
	// SignedIn is whether the context has a token; Context, when set, is
	// the only --context the fake knows. Refused makes the API answer 401,
	// and Forbidden names commands ("account get") the token may not run.
	SignedIn  bool
	Context   string
	Refused   bool
	Forbidden map[string]bool
	// Regions, Sizes and Images are the catalogue, as doctl prints it.
	Regions []map[string]any
	Sizes   []map[string]any
	Images  []map[string]any
	// Keys are the account's SSH keys: id to public key line.
	Keys      map[int64]string
	Droplets  map[int64]*Droplet
	Firewalls map[string]*Firewall
	// Tags are the tags that exist.
	Tags map[string]bool
	// Snapshots are the account's Droplet snapshots.
	Snapshots map[int64]*Snapshot
	// NewPolls is how many "droplet get" calls a new Droplet answers
	// "new", without an address, before it is active.
	NewPolls int
	// ActionPolls is how many "droplet-action get" calls a resize or a
	// snapshot answers "in-progress" before it ends.
	ActionPolls int
	// Errored names the kinds of action ("resize", "snapshot") that end
	// "errored" and change nothing; a failed snapshot stays pending.
	Errored map[string]bool
	// Stubborn is a Droplet whose operating system ignores a shutdown: only
	// a power-off turns it off.
	Stubborn bool
	// FirewallPolls is how many "firewall get" calls list a Droplet just
	// added to a firewall as pending before the firewall is applied to it.
	FirewallPolls int
	// Hook, when set, is called with each call before it is answered.
	Hook func(call string)
	// Fail makes the named command ("compute firewall create") fail with
	// a server error.
	Fail map[string]string
	// Secrets must never appear in an argument; Leaked records any that did.
	Secrets []string
	Leaked  []string
	calls   []string
	next    int64
	actions map[int64]*action
	// deleted are the snapshots that were deleted, with the tags still on
	// them: as DigitalOcean does, a tag counts a deleted image among its
	// resources until the tag is taken off it.
	deleted map[int64][]string
}

// New is a signed-in account with a realistic catalogue and no Droplets.
func New() *CLI {
	both := []string{"fra1", "nyc3"}
	size := func(slug string, mem, vcpus, disk int, tb, monthly float64, regions []string, available bool) map[string]any {
		return map[string]any{"slug": slug, "memory": mem, "vcpus": vcpus, "disk": disk, "transfer": tb, "price_monthly": monthly,
			"price_hourly": monthly / 672, "regions": regions, "available": available, "description": "Basic"}
	}
	image := func(id int, slug, dist, name string, regions []string) map[string]any {
		return map[string]any{"id": id, "slug": slug, "distribution": dist, "name": name, "public": true, "regions": regions, "type": "base", "status": "available"}
	}
	return &CLI{
		SignedIn: true,
		Regions: []map[string]any{
			{"slug": "nyc3", "name": "New York 3", "available": true, "sizes": []string{"s-2vcpu-4gb"}},
			{"slug": "fra1", "name": "Frankfurt 1", "available": true, "sizes": []string{"s-2vcpu-4gb"}},
			{"slug": "nyc2", "name": "New York 2", "available": false, "sizes": []string{}},
		},
		Sizes: []map[string]any{
			size("s-1vcpu-1gb", 1024, 1, 25, 1, 6, both, true),
			size("s-2vcpu-2gb", 2048, 2, 60, 3, 18, both, true),
			size("s-2vcpu-4gb", 4096, 2, 80, 4, 24, both, true),
			size("s-4vcpu-8gb", 8192, 4, 160, 5, 48, both, true),
			size("c-2", 4096, 2, 25, 4, 42, []string{"nyc3"}, true),
			size("s-2vcpu-4gb-old", 4096, 2, 80, 4, 20, both, false),
		},
		Images: []map[string]any{
			image(101, "ubuntu-24-04-x64", "Ubuntu", "24.04 (LTS) x64", both),
			image(102, "ubuntu-26-04-x64", "Ubuntu", "26.04 (LTS) x64", both),
			image(103, "ubuntu-22-04-x64", "Ubuntu", "22.04 (LTS) x64", []string{"nyc3"}),
			image(104, "debian-13-x64", "Debian", "13 x64", both),
		},
		Keys: map[int64]string{}, Droplets: map[int64]*Droplet{}, Firewalls: map[string]*Firewall{}, Tags: map[string]bool{},
		Snapshots: map[int64]*Snapshot{}, Errored: map[string]bool{}, actions: map[int64]*action{}, deleted: map[int64][]string{},
		Fail: map[string]string{}, Forbidden: map[string]bool{}, next: 4241,
	}
}

// Calls lists every call as "command arg...", without the global flags.
func (c *CLI) Calls() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.calls...)
}

// Called reports whether a command ("compute droplet delete") ran.
func (c *CLI) Called(cmd string) bool {
	for _, l := range c.Calls() {
		if l == cmd || strings.HasPrefix(l, cmd+" ") {
			return true
		}
	}
	return false
}

// AddDroplet puts a Droplet in the account and returns it.
func (c *CLI) AddDroplet(name string, tags ...string) *Droplet {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.add(&Droplet{Name: name, Region: "fra1", Size: "s-2vcpu-4gb", Image: "ubuntu-24-04-x64", Tags: tags})
}

func (c *CLI) add(d *Droplet) *Droplet {
	c.next++
	d.ID = c.next
	d.IP = "203.0.113." + strconv.FormatInt(d.ID-4235, 10)
	if d.Disk == 0 {
		d.Disk = c.disk(d.Size)
	}
	for _, t := range d.Tags {
		c.Tags[t] = true
	}
	c.Droplets[d.ID] = d
	return d
}

// disk is the disk of a size in the catalogue, in GB, or 0.
func (c *CLI) disk(size string) int {
	for _, s := range c.Sizes {
		if s["slug"] == size {
			n, _ := s["disk"].(int)
			return n
		}
	}
	return 0
}

// AddSnapshot puts an available Droplet snapshot in the account and
// returns it.
func (c *CLI) AddSnapshot(name string, tags ...string) *Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.next++
	s := &Snapshot{ID: c.next, Name: name, Region: "fra1", MinDisk: 80, SizeGB: 2.5, Tags: tags, Status: "available", Created: c.stamp()}
	for _, t := range tags {
		c.Tags[t] = true
	}
	c.Snapshots[s.ID] = s
	return s
}

// stamp is a creation time later than every one before it.
func (c *CLI) stamp() string {
	return time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC).Add(time.Duration(c.next) * time.Second).Format(time.RFC3339)
}

func fail(status int, title, msg string) ([]byte, []byte, int, error) {
	e := map[string]any{"detail": msg}
	if status != 0 {
		e = map[string]any{"detail": fmt.Sprintf("GET https://api.digitalocean.com/v2/: %d %s", status, msg), "title": title, "reason": msg, "status": status}
	}
	b, _ := json.Marshal(map[string]any{"errors": []any{e}})
	return append(b, '\n'), nil, 1, nil
}

func has(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// rules parses doctl's rule argument into "protocol:ports:addresses".
func rules(arg string) []string {
	var out []string
	for _, r := range strings.Fields(arg) {
		var proto, ports string
		var addrs []string
		for _, kv := range strings.Split(r, ",") {
			k, v, _ := strings.Cut(kv, ":")
			switch k {
			case "protocol":
				proto = v
			case "ports":
				ports = v
			case "address":
				addrs = append(addrs, v)
			}
		}
		out = append(out, proto+":"+ports+":"+strings.Join(addrs, ","))
	}
	return out
}

func ruleView(list []string, peers string) []map[string]any {
	out := []map[string]any{}
	for _, r := range list {
		f := strings.SplitN(r, ":", 3)
		ports := f[1]
		if ports == "" || ports == "all" {
			ports = "0" // as the API returns every port, and ICMP
		}
		out = append(out, map[string]any{"protocol": f[0], "ports": ports, peers: map[string]any{"addresses": strings.Split(f[2], ",")}})
	}
	return out
}

// Run is digitalocean.Runner.
func (c *CLI) Run(_ context.Context, args []string) ([]byte, []byte, int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, a := range args {
		for _, s := range c.Secrets {
			if s != "" && strings.Contains(a, s) {
				c.Leaked = append(c.Leaked, a)
			}
		}
	}
	// Split the global flags off, then the command's own.
	var words []string
	flag := map[string]string{}
	output, ctxName := "", ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--output" && i+1 < len(args):
			output = args[i+1]
			i++
		case a == "--context" && i+1 < len(args):
			ctxName = args[i+1]
			i++
		case a == "--interactive=false":
		case a == "--force":
			flag[a] = "true"
		case strings.HasPrefix(a, "--") && i+1 < len(args):
			flag[a] = args[i+1]
			i++
		default:
			words = append(words, a)
		}
	}
	c.calls = append(c.calls, strings.Join(words, " "))
	if c.Hook != nil {
		c.Hook(strings.Join(words, " "))
	}
	if output != "json" {
		return nil, []byte("Error: the fake needs --output json\n"), 1, nil
	}
	n := 2
	if len(words) > 0 && words[0] == "compute" {
		n = 3
	}
	if len(words) < n {
		return nil, []byte("Error: unknown command\n"), 1, nil
	}
	cmd, rest := strings.Join(words[:n], " "), words[n:]
	arg := ""
	if len(rest) > 0 {
		arg = rest[0]
	}
	send := func(v any) ([]byte, []byte, int, error) {
		b, _ := json.MarshalIndent(v, "", "  ")
		return append(b, '\n'), nil, 0, nil
	}
	if !c.SignedIn || (ctxName != "" && c.Context != "" && ctxName != c.Context) {
		return fail(0, "", "Unable to initialize DigitalOcean API client: access token is required. (hint: run 'doctl auth init')") //nolint:misspell // doctl's own words
	}
	if c.Refused {
		return fail(401, "Unauthorized", "Unable to authenticate you") //nolint:misspell // the API's own word
	}
	if c.Forbidden[cmd] {
		return fail(403, "Forbidden", "You are not authorized to perform this operation")
	}
	if msg, ok := c.Fail[cmd]; ok {
		return fail(500, "Internal Server Error", msg)
	}
	missing := func() ([]byte, []byte, int, error) {
		return fail(404, "Not Found", "The resource you were accessing could not be found.")
	}
	needForce := func() ([]byte, []byte, int, error) {
		return fail(0, "", "Requires confirmation. Use the `--force` flag to continue without confirmation.")
	}
	dropletView := func(d *Droplet) map[string]any {
		status, v4 := "active", []map[string]any{
			{"ip_address": "10.114.0.2", "netmask": "255.255.240.0", "type": "private"},
			{"ip_address": d.IP, "netmask": "255.255.240.0", "gateway": "203.0.113.1", "type": "public"}}
		if d.polls <= c.NewPolls && c.NewPolls > 0 {
			status, v4 = "new", []map[string]any{}
		} else if d.Status != "" {
			status = d.Status // a Droplet keeps its addresses while it is off
		}
		return map[string]any{"id": d.ID, "name": d.Name, "status": status, "size_slug": d.Size, "disk": d.Disk, "tags": d.Tags,
			"region": map[string]any{"slug": d.Region, "name": d.Region}, "image": map[string]any{"slug": d.Image, "name": d.Image, "distribution": "Ubuntu"},
			"networks": map[string]any{"v4": v4}, "volume_ids": nil}
	}
	droplets := func(keep func(*Droplet) bool) []map[string]any {
		out := []map[string]any{}
		ids := make([]int64, 0, len(c.Droplets))
		for id := range c.Droplets {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		for _, id := range ids {
			if d := c.Droplets[id]; keep(d) {
				out = append(out, dropletView(d))
			}
		}
		return out
	}
	fwView := func(f *Firewall) map[string]any {
		ids := f.DropletIDs
		if ids == nil {
			ids = []int64{}
		}
		status, pending := "succeeded", []any{}
		for _, id := range ids {
			if f.pending[id] > 0 {
				status, pending = "waiting", append(pending, map[string]any{"droplet_id": id, "removing": false, "status": "waiting"})
			}
		}
		return map[string]any{"id": f.ID, "name": f.Name, "status": status, "inbound_rules": ruleView(f.Inbound, "sources"),
			"outbound_rules": ruleView(f.Outbound, "destinations"), "droplet_ids": ids, "tags": f.Tags, "pending_changes": pending}
	}
	fwFrom := func(f *Firewall) (*Firewall, string) {
		f.Name, f.Inbound, f.Outbound = flag["--name"], rules(flag["--inbound-rules"]), rules(flag["--outbound-rules"])
		f.Tags, f.DropletIDs = nil, nil
		for _, t := range strings.Split(flag["--tag-names"], ",") {
			if t == "" {
				continue
			}
			if !c.Tags[t] {
				return nil, "tag " + t + " does not exist"
			}
			f.Tags = append(f.Tags, t)
		}
		for _, s := range strings.Split(flag["--droplet-ids"], ",") {
			if id, err := strconv.ParseInt(s, 10, 64); err == nil {
				f.DropletIDs = append(f.DropletIDs, id)
			}
		}
		if len(f.Inbound)+len(f.Outbound) == 0 {
			return nil, "must have at least one inbound or outbound rule"
		}
		return f, ""
	}
	tagView := func(name string) map[string]any {
		n, images := 0, 0
		for _, d := range c.Droplets {
			if has(d.Tags, name) {
				n++
			}
		}
		for _, s := range c.Snapshots {
			if has(s.Tags, name) {
				images++
			}
		}
		for _, tags := range c.deleted {
			if has(tags, name) {
				images++
			}
		}
		return map[string]any{"name": name, "resources": map[string]any{"count": n + images, "droplets": map[string]any{"count": n}, "images": map[string]any{"count": images}}}
	}
	snapView := func(s *Snapshot) map[string]any {
		v := map[string]any{"id": s.ID, "name": s.Name, "type": "snapshot", "distribution": "Ubuntu", "min_disk_size": s.MinDisk,
			"created_at": s.Created, "status": s.Status}
		if len(s.Tags) > 0 {
			v["tags"] = s.Tags
		}
		if s.Status == "available" {
			// As the API does: where it is stored and what it holds are
			// known only once it is taken.
			v["regions"], v["size_gigabytes"] = []string{s.Region}, s.SizeGB
		}
		return v
	}
	actionView := func(a *action) []any {
		return []any{map[string]any{"id": a.id, "status": a.status, "type": a.kind, "resource_id": a.droplet, "resource_type": "droplet", "region_slug": "fra1"}}
	}
	// start records an action on a Droplet. One that takes time ends at
	// the poll after ActionPolls; the others are done at once.
	start := func(d *Droplet, kind string, slow bool, done func()) ([]byte, []byte, int, error) {
		c.next++
		a := &action{id: c.next, droplet: d.ID, kind: kind, status: "in-progress", done: done}
		c.actions[a.id] = a
		view := actionView(a) // the API answers before anything is done
		if !slow {
			a.status = "completed"
			done()
		}
		return send(view)
	}
	switch cmd {
	case "account get":
		return send(map[string]any{"email": "sam@example.com", "uuid": "a1b2c3", "status": "active", "droplet_limit": 10, "email_verified": true,
			"team": map[string]any{"name": "Sam's team", "uuid": "t1"}})
	case "compute region list":
		return send(c.Regions)
	case "compute size list":
		return send(c.Sizes)
	case "compute image list-distribution":
		return send(c.Images)
	case "compute image get":
		for _, i := range c.Images {
			if i["slug"] == arg || fmt.Sprint(i["id"]) == arg {
				return send([]any{i})
			}
		}
		if id, err := strconv.ParseInt(arg, 10, 64); err == nil && c.Snapshots[id] != nil {
			return send([]any{snapView(c.Snapshots[id])})
		}
		return missing()
	case "compute image list-user":
		ids := make([]int64, 0, len(c.Snapshots))
		for id := range c.Snapshots {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		out := []map[string]any{}
		for _, id := range ids {
			out = append(out, snapView(c.Snapshots[id]))
		}
		return send(out)
	case "compute image delete":
		if flag["--force"] == "" {
			return needForce()
		}
		id, _ := strconv.ParseInt(arg, 10, 64)
		if c.Snapshots[id] == nil {
			if _, gone := c.deleted[id]; gone {
				return fail(422, "Unprocessable Entity", "Can not delete an already deleted image.")
			}
			return missing()
		}
		c.deleted[id] = c.Snapshots[id].Tags
		delete(c.Snapshots, id)
		return nil, nil, 0, nil
	case "compute tag remove":
		id, err := strconv.ParseInt(strings.TrimPrefix(flag["--resource"], "do:image:"), 10, 64)
		if err != nil {
			return fail(422, "Unprocessable Entity", "the fake tags images only: "+flag["--resource"])
		}
		if !c.Tags[arg] {
			return missing()
		}
		without := func(tags []string) []string {
			var keep []string
			for _, t := range tags {
				if t != arg {
					keep = append(keep, t)
				}
			}
			return keep
		}
		if s := c.Snapshots[id]; s != nil {
			s.Tags = without(s.Tags)
		} else if tags, gone := c.deleted[id]; gone {
			c.deleted[id] = without(tags) // accepted for an image that is gone
		}
		return nil, nil, 0, nil
	case "compute tag apply":
		id, err := strconv.ParseInt(strings.TrimPrefix(flag["--resource"], "do:image:"), 10, 64)
		if err != nil {
			return fail(422, "Unprocessable Entity", "the fake tags images only: "+flag["--resource"])
		}
		s := c.Snapshots[id]
		if !c.Tags[arg] || s == nil {
			return missing()
		}
		if !has(s.Tags, arg) {
			s.Tags = append(s.Tags, arg)
		}
		return nil, nil, 0, nil
	case "compute ssh-key list":
		out := []map[string]any{}
		for id, k := range c.Keys {
			out = append(out, map[string]any{"id": id, "name": "key-" + strconv.FormatInt(id, 10), "public_key": k, "fingerprint": "aa:bb"})
		}
		return send(out)
	case "compute tag get":
		if !c.Tags[arg] {
			return missing()
		}
		return send([]any{tagView(arg)})
	case "compute tag create":
		c.Tags[arg] = true
		return send([]any{tagView(arg)})
	case "compute tag delete":
		if flag["--force"] == "" {
			return needForce()
		}
		if !c.Tags[arg] {
			return missing()
		}
		delete(c.Tags, arg)
		for _, d := range c.Droplets { // deleting a tag takes it off its resources
			var keep []string
			for _, t := range d.Tags {
				if t != arg {
					keep = append(keep, t)
				}
			}
			d.Tags = keep
		}
		for _, s := range c.Snapshots {
			var keep []string
			for _, t := range s.Tags {
				if t != arg {
					keep = append(keep, t)
				}
			}
			s.Tags = keep
		}
		for id, tags := range c.deleted {
			var keep []string
			for _, t := range tags {
				if t != arg {
					keep = append(keep, t)
				}
			}
			c.deleted[id] = keep
		}
		return nil, nil, 0, nil
	case "compute droplet-action get":
		aid, _ := strconv.ParseInt(flag["--action-id"], 10, 64)
		a := c.actions[aid]
		if id, _ := strconv.ParseInt(arg, 10, 64); a == nil || a.droplet != id {
			return missing()
		}
		if a.status == "in-progress" {
			if a.polls++; a.polls > c.ActionPolls {
				if c.Errored[a.kind] {
					a.status = "errored"
				} else {
					a.status = "completed"
					a.done()
				}
			}
		}
		return send(actionView(a))
	case "compute droplet-action shutdown", "compute droplet-action power-off", "compute droplet-action power-on",
		"compute droplet-action resize", "compute droplet-action snapshot":
		id, _ := strconv.ParseInt(arg, 10, 64)
		d := c.Droplets[id]
		if d == nil {
			return missing()
		}
		switch words[2] {
		case "shutdown":
			return start(d, "shutdown", false, func() {
				if !c.Stubborn {
					d.Status = "off"
				}
			})
		case "power-off":
			return start(d, "power_off", false, func() { d.Status = "off" })
		case "power-on":
			return start(d, "power_on", false, func() { d.Status = "" })
		case "resize":
			size := flag["--size"]
			switch disk := c.disk(size); {
			case disk == 0:
				return fail(422, "Unprocessable Entity", "This size is unavailable.")
			case disk < d.Disk:
				return fail(422, "Unprocessable Entity", "This size is not available because it has a smaller disk.")
			case d.Status != "off":
				return fail(422, "Unprocessable Entity", "Droplet must be powered off to resize.")
			}
			// Without --resize-disk the disk keeps its size.
			return start(d, "resize", true, func() { d.Size = size })
		}
		c.next++
		s := &Snapshot{ID: c.next, Name: flag["--snapshot-name"], DropletID: d.ID, Region: d.Region, MinDisk: d.Disk, SizeGB: 2.5, Status: "pending", Created: c.stamp()}
		c.Snapshots[s.ID] = s
		return start(d, "snapshot", true, func() {
			if c.Snapshots[s.ID] != nil {
				s.Status = "available"
			}
		})
	case "compute droplet list":
		tag := flag["--tag-name"]
		return send(droplets(func(d *Droplet) bool {
			return (tag == "" || has(d.Tags, tag)) && (arg == "" || d.Name == arg)
		}))
	case "compute droplet get":
		id, _ := strconv.ParseInt(arg, 10, 64)
		d := c.Droplets[id]
		if d == nil {
			return missing()
		}
		d.polls++
		return send([]any{dropletView(d)})
	case "compute droplet create":
		d := &Droplet{Name: arg, Region: flag["--region"], Size: flag["--size"], Image: flag["--image"], Tags: strings.Split(flag["--tag-names"], ",")}
		if k := flag["--ssh-keys"]; k != "" {
			d.SSHKeys = strings.Split(k, ",")
		}
		ok := false
		for _, s := range c.Sizes {
			ok = ok || s["slug"] == d.Size
		}
		if !ok {
			return fail(422, "Unprocessable Entity", "You specified an invalid size for Droplet creation.")
		}
		if id, err := strconv.ParseInt(d.Image, 10, 64); err == nil {
			s := c.Snapshots[id]
			switch {
			case s == nil || s.Status != "available":
				return fail(422, "Unprocessable Entity", "You specified an invalid image for Droplet creation.")
			case c.disk(d.Size) < s.MinDisk:
				return fail(422, "Unprocessable Entity", "Cannot create a droplet with a smaller disk than the image.")
			}
			d.FromSnapshot = id
		}
		if path := flag["--user-data-file"]; path != "" {
			if fi, err := os.Stat(path); err == nil {
				d.UserDataMode = fi.Mode().Perm()
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return fail(0, "", "open "+path+": no such file or directory")
			}
			d.UserData = string(b)
		}
		if ud, ok := flag["--user-data"]; ok {
			d.UserData = ud
		}
		c.add(d)
		// As the API answers a create: no address yet.
		v := dropletView(d)
		v["status"], v["networks"] = "new", map[string]any{"v4": []any{}}
		return send([]any{v})
	case "compute droplet delete":
		if flag["--force"] == "" {
			return needForce()
		}
		id, _ := strconv.ParseInt(arg, 10, 64)
		if c.Droplets[id] == nil {
			return missing()
		}
		delete(c.Droplets, id)
		for _, f := range c.Firewalls { // a Droplet that is gone is in no firewall
			var keep []int64
			for _, n := range f.DropletIDs {
				if n != id {
					keep = append(keep, n)
				}
			}
			f.DropletIDs = keep
		}
		return nil, nil, 0, nil
	case "compute firewall get":
		f := c.Firewalls[arg]
		if f == nil {
			return missing()
		}
		view := fwView(f)
		for id := range f.pending {
			f.pending[id]--
		}
		return send([]any{view})
	case "compute firewall add-droplets":
		f := c.Firewalls[arg]
		if f == nil {
			return missing()
		}
		for _, n := range strings.Split(flag["--droplet-ids"], ",") {
			id, err := strconv.ParseInt(n, 10, 64)
			if err != nil || c.Droplets[id] == nil {
				return fail(422, "Unprocessable Entity", "droplet "+n+" does not exist")
			}
			f.DropletIDs = append(f.DropletIDs, id)
			if f.pending == nil {
				f.pending = map[int64]int{}
			}
			f.pending[id] = c.FirewallPolls
		}
		return nil, nil, 0, nil
	case "compute firewall list":
		out := []map[string]any{}
		ids := make([]string, 0, len(c.Firewalls))
		for id := range c.Firewalls {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			out = append(out, fwView(c.Firewalls[id]))
		}
		return send(out)
	case "compute firewall create":
		f, msg := fwFrom(&Firewall{})
		if msg != "" {
			return fail(422, "Unprocessable Entity", msg)
		}
		c.next++
		f.ID = "fw-" + strconv.FormatInt(c.next, 10)
		c.Firewalls[f.ID] = f
		return send([]any{fwView(f)})
	case "compute firewall update":
		cur := c.Firewalls[arg]
		if cur == nil {
			return missing()
		}
		f, msg := fwFrom(&Firewall{ID: cur.ID, pending: cur.pending})
		if msg != "" {
			return fail(422, "Unprocessable Entity", msg)
		}
		c.Firewalls[arg] = f
		return send([]any{fwView(f)})
	case "compute firewall delete":
		if flag["--force"] == "" {
			return needForce()
		}
		if c.Firewalls[arg] == nil {
			return missing()
		}
		delete(c.Firewalls, arg)
		return nil, nil, 0, nil
	}
	return nil, []byte("Error: unknown command \"" + cmd + "\" for \"doctl\"\n"), 1, nil
}
