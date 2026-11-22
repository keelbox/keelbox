// Package gcptest is a fake gcloud for tests: an in-memory Google Cloud
// project behind the same arguments and JSON the gcloud command takes and
// prints. As gcloud does, it reports an error as text on standard error
// ("ERROR: (gcloud.compute.instances.create) Could not fetch resource:"
// and a line per cause) and exits 1, and for the lists gcloud gathers
// from several requests it prints a warning and an empty list and exits 0.
package gcptest

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

// Instance is a VM in the fake project.
type Instance struct {
	ID                      string
	Name, Zone, MachineType string
	IP                      string
	Labels                  map[string]string
	Tags                    []string
	// Metadata are the --metadata pairs; UserData is what the file named
	// by --metadata-from-file user-data= held, and UserDataMode its mode.
	Metadata     map[string]string
	UserData     string
	UserDataMode os.FileMode
	// ServiceAccount is the attached service account, or "".
	ServiceAccount             string
	Image, Disk, DiskType, Net string
	// Status is the VM's state when it is not RUNNING: TERMINATED after
	// a stop, or whatever a test sets.
	Status string
	// KeepDisk turns the boot disk's auto-delete off; ExtraDisks are
	// other disks attached to the VM. SourceSnapshot is the snapshot the
	// boot disk was made from.
	KeepDisk       bool
	ExtraDisks     []string
	SourceSnapshot string
	// polls counts the lists that showed the VM since it was created.
	polls int
}

// Snapshot is a disk snapshot in the fake project.
type Snapshot struct {
	Name, Description string
	// Status is READY once the snapshot has been shown SnapshotPolls
	// times; a test may set it to anything else, which is then shown.
	Status string
	Labels map[string]string
	// SourceDisk is the disk it is of, as zone/name; DiskGB that disk's
	// size.
	SourceDisk string
	DiskGB     string
	Created    time.Time
	polls      int
}

// Rule is a firewall rule in the fake project.
type Rule struct {
	Name, Description, Network string
	// Action is ALLOW or DENY; Specs are "tcp:22", "udp:41641" or "all".
	Action       string
	Specs        []string
	Priority     int
	SourceRanges []string
	TargetTags   []string
	Disabled     bool
}

// Address is a reserved address in the fake project.
type Address struct {
	Name, Region, IP, Description string
}

// CLI is the fake. Fields may be changed between calls.
type CLI struct {
	mu sync.Mutex
	// SignedIn is whether gcloud has an account; Expired makes Google
	// refuse its sign-in. Account, Project and Zone are what gcloud's own
	// configuration holds.
	SignedIn bool
	Expired  bool
	Account  string
	Project  string
	Zone     string
	// Projects are the projects the account can use. APIDisabled turns
	// the Compute Engine API off in all of them.
	Projects    map[string]bool
	APIDisabled bool
	// Zones, Types and Networks are the catalogue.
	Zones     []string
	Types     []map[string]any
	Networks  []string
	Instances map[string]*Instance // by zone/name
	Rules     map[string]*Rule
	Addresses map[string]*Address
	// Disks are the disks that exist, by zone/name; Snapshots the
	// snapshots, by name.
	Disks     map[string]bool
	Snapshots map[string]*Snapshot
	// SnapshotPolls is how many reads show a new snapshot as CREATING and
	// then UPLOADING before it is READY; SnapshotFails makes it FAILED
	// instead.
	SnapshotPolls int
	SnapshotFails bool
	// Hook, when set, is called with each call as Calls lists it, before
	// it runs.
	Hook func(call string)
	// StagingPolls is how many lists show a new VM as STAGING, without
	// an address, before it runs.
	StagingPolls int
	// Fail makes the named command ("compute firewall-rules create") fail
	// with that message.
	Fail map[string]string
	// Secrets must never appear in an argument; Leaked records any that did.
	Secrets []string
	Leaked  []string
	calls   []string
	next    int64
}

// New is a signed-in gcloud, configured for the project "acme-dev", with
// a realistic catalogue and nothing created.
func New() *CLI {
	mt := func(name string, cpus, mb int, shared bool, arch string) map[string]any {
		return map[string]any{"name": name, "guestCpus": cpus, "memoryMb": mb, "isSharedCpu": shared, "architecture": arch,
			"description": name, "zone": "europe-west3-a", "kind": "compute#machineType"}
	}
	old := mt("n1-retired-2", 2, 7680, false, "X86_64")
	old["deprecated"] = map[string]any{"state": "DEPRECATED"}
	return &CLI{
		SignedIn: true, Account: "sam@example.com", Project: "acme-dev",
		Projects: map[string]bool{"acme-dev": true, "acme-prod": true},
		Zones:    []string{"europe-west3-a", "europe-west3-b", "us-central1-a"},
		Types: []map[string]any{
			mt("e2-standard-4", 4, 16384, false, "X86_64"),
			mt("e2-micro", 2, 1024, true, "X86_64"),
			mt("e2-medium", 2, 4096, true, "X86_64"),
			mt("e2-small", 2, 2048, true, "X86_64"),
			mt("e2-standard-2", 2, 8192, false, "X86_64"),
			mt("e2-standard-16", 16, 65536, false, "X86_64"),
			mt("e2-highmem-2", 2, 16384, false, "X86_64"),
			mt("c3-standard-4", 4, 16384, false, "X86_64"),
			mt("t2a-standard-2", 2, 8192, false, "ARM64"),
			old,
		},
		Networks:  []string{"default"},
		Instances: map[string]*Instance{}, Rules: map[string]*Rule{}, Addresses: map[string]*Address{},
		Disks: map[string]bool{}, Snapshots: map[string]*Snapshot{},
		Fail: map[string]string{}, next: 4241,
	}
}

// Calls lists every call as "command arg...", without the global flags.
func (c *CLI) Calls() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.calls...)
}

// Called reports whether a command ("compute instances delete") ran.
func (c *CLI) Called(cmd string) bool {
	for _, l := range c.Calls() {
		if l == cmd || strings.HasPrefix(l, cmd+" ") {
			return true
		}
	}
	return false
}

// AddInstance puts a running VM in the fake project, in europe-west3-a,
// and returns it.
func (c *CLI) AddInstance(name string, labels map[string]string, tags ...string) *Instance {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.add(&Instance{Name: name, Zone: "europe-west3-a", MachineType: "e2-medium", Labels: labels, Tags: tags,
		Metadata: map[string]string{"block-project-ssh-keys": "true", "enable-oslogin": "false"}})
}

func (c *CLI) add(i *Instance) *Instance {
	c.next++
	i.ID = strconv.FormatInt(c.next*1000003, 10)
	if i.IP == "" {
		i.IP = "203.0.113." + strconv.FormatInt(c.next-4235, 10)
	}
	c.Instances[i.Zone+"/"+i.Name] = i
	c.Disks[i.Zone+"/"+i.Name] = true
	return i
}

// AddSnapshot puts a READY snapshot of the disk europe-west3-a/<box> in
// the fake project, labelled as the box's parked one, and returns it.
func (c *CLI) AddSnapshot(name, box string) *Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := &Snapshot{Name: name, Status: "READY", SourceDisk: "europe-west3-a/" + box, DiskGB: "50", Created: time.Now().UTC().Truncate(time.Second),
		Labels: map[string]string{"keelbox": "true", "keelbox-box": box, "keelbox-parked": "true"}}
	c.Snapshots[name] = s
	return s
}

func has(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func region(zone string) string { return zone[:strings.LastIndex(zone, "-")] }

// matches evaluates the filters keelbox issues: terms joined by " OR ",
// each name=, labels.KEY= or tags.items=.
func matches(filter, name string, labels map[string]string, tags []string) bool {
	if filter == "" {
		return true
	}
	for _, term := range strings.Split(filter, " OR ") {
		k, v, _ := strings.Cut(term, "=")
		switch {
		case k == "name" && v == name:
			return true
		case strings.HasPrefix(k, "labels.") && labels[strings.TrimPrefix(k, "labels.")] == v && v != "":
			return true
		case k == "tags.items" && has(tags, v):
			return true
		}
	}
	return false
}

// Run is gcp.Runner. As the real command would be, it is killed when
// its context is done: it changes nothing and exits -1.
func (c *CLI) Run(ctx context.Context, args []string) ([]byte, []byte, int, error) {
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
	var words, shown []string
	flag := map[string]string{}
	json_, quiet, project := false, false, ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--format=json":
			json_ = true
		case a == "--quiet":
			quiet = true
		case a == "--project" && i+1 < len(args):
			project = args[i+1]
			i++
		case a == "--no-service-account", a == "--no-scopes", a == "--no-disabled", a == "--async", a == "--auto-delete":
			flag[a] = "true"
			shown = append(shown, a)
		case strings.HasPrefix(a, "--") && i+1 < len(args):
			flag[a] = args[i+1]
			shown = append(shown, a, args[i+1])
			i++
		default:
			words = append(words, a)
			shown = append(shown, a)
		}
	}
	c.calls = append(c.calls, strings.Join(shown, " "))
	if c.Hook != nil {
		c.Hook(strings.Join(shown, " "))
	}
	if ctx.Err() != nil {
		return nil, nil, -1, nil
	}
	n := 2
	if len(words) > 0 && words[0] == "compute" {
		n = 3
	}
	if len(words) < n {
		return nil, []byte("ERROR: (gcloud) Invalid choice\n"), 2, nil
	}
	cmd, rest := strings.Join(words[:n], " "), words[n:]
	dotted := "gcloud." + strings.ReplaceAll(cmd, " ", ".")
	if !json_ || !quiet {
		return nil, []byte("ERROR: (" + dotted + ") the fake needs --format=json and --quiet\n"), 1, nil
	}
	arg := ""
	if len(rest) > 0 {
		arg = rest[0]
	}
	send := func(v any) ([]byte, []byte, int, error) {
		b, _ := json.MarshalIndent(v, "", "  ")
		return append(b, '\n'), nil, 0, nil
	}
	// plain is an error gcloud raises itself; fetch one the API answered.
	plain := func(msg string) ([]byte, []byte, int, error) {
		return nil, []byte("ERROR: (" + dotted + ") " + msg + "\n"), 1, nil
	}
	fetch := func(msg string) ([]byte, []byte, int, error) {
		switch cmd {
		case "compute instances list", "compute addresses list", "compute machine-types list":
			return []byte("[]\n"), []byte("WARNING: Some requests did not succeed.\n - " + msg + "\n\n"), 0, nil
		case "compute firewall-rules list", "compute zones list", "compute networks list", "compute snapshots list":
			return []byte("[]\n"), []byte("ERROR: (" + dotted + ") Some requests did not succeed:\n - " + msg + "\n\n"), 1, nil
		}
		return nil, []byte("ERROR: (" + dotted + ") Could not fetch resource:\n - " + msg + "\n\n"), 1, nil
	}
	if cmd == "config list" {
		core := map[string]any{"disable_prompts": "True"}
		if c.SignedIn && c.Account != "" {
			core["account"] = c.Account
		}
		if c.Project != "" {
			core["project"] = c.Project
		}
		doc := map[string]any{"core": core}
		if c.Zone != "" {
			doc["compute"] = map[string]any{"zone": c.Zone}
		}
		return send(doc)
	}
	if project == "" {
		project = c.Project
	}
	switch {
	case !c.SignedIn:
		return plain("You do not currently have an active account selected.\nPlease run:\n\n  $ gcloud auth login\n\nto obtain new credentials.\n")
	case c.Expired:
		return plain("There was a problem refreshing your current auth tokens: ('invalid_grant: Bad Request', {'error': 'invalid_grant', 'error_description': 'Bad Request'})\n" +
			"Please run:\n\n  $ gcloud auth login\n\nto obtain new credentials.\n")
	case project == "":
		return plain("The required property [project] is not currently set.\nIt can be set on a per-command basis by re-running your command with the [--project] flag.\n")
	case !c.Projects[project]:
		return fetch("The resource 'projects/" + project + "' was not found")
	case c.APIDisabled:
		return fetch("Compute Engine API has not been used in project " + project + " before or it is disabled. Enable it by visiting " +
			"https://console.developers.google.com/apis/api/compute.googleapis.com/overview?project=" + project + " then retry.")
	}
	if msg, ok := c.Fail[cmd]; ok {
		return fetch(msg)
	}
	url := func(path string) string {
		return "https://www.googleapis.com/compute/v1/projects/" + project + "/" + path
	}
	instView := func(i *Instance) map[string]any {
		status, access := "RUNNING", []map[string]any{{"name": "external-nat", "natIP": i.IP, "type": "ONE_TO_ONE_NAT"}}
		if i.polls < c.StagingPolls {
			status, access = "STAGING", []map[string]any{{"name": "external-nat", "type": "ONE_TO_ONE_NAT"}}
		}
		if i.Status != "" {
			status = i.Status
			// A stopped VM keeps a reserved address and gives back one
			// Google lent it.
			reserved := false
			for _, a := range c.Addresses {
				reserved = reserved || a.IP == i.IP
			}
			if !reserved {
				access = []map[string]any{{"name": "external-nat", "type": "ONE_TO_ONE_NAT"}}
			}
		}
		items := []map[string]any{}
		keys := make([]string, 0, len(i.Metadata))
		for k := range i.Metadata {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			items = append(items, map[string]any{"key": k, "value": i.Metadata[k]})
		}
		v := map[string]any{"id": i.ID, "name": i.Name, "status": status, "zone": url("zones/" + i.Zone), "kind": "compute#instance",
			"machineType": url("zones/" + i.Zone + "/machineTypes/" + i.MachineType), "labels": i.Labels,
			"metadata":          map[string]any{"items": items, "kind": "compute#metadata"},
			"networkInterfaces": []map[string]any{{"network": url("global/networks/default"), "networkIP": "10.156.0.2", "accessConfigs": access}}}
		size := strings.TrimSuffix(i.Disk, "GB")
		if size == "" {
			size = "50"
		}
		disks := []map[string]any{{"source": url("zones/" + i.Zone + "/disks/" + i.Name), "boot": true, "autoDelete": !i.KeepDisk, "diskSizeGb": size,
			"deviceName": "persistent-disk-0", "architecture": "X86_64", "kind": "compute#attachedDisk"}}
		for _, d := range i.ExtraDisks {
			disks = append(disks, map[string]any{"source": url("zones/" + i.Zone + "/disks/" + d), "boot": false, "autoDelete": false, "diskSizeGb": "100", "kind": "compute#attachedDisk"})
		}
		v["disks"] = disks
		if len(i.Tags) > 0 {
			v["tags"] = map[string]any{"items": i.Tags}
		}
		if i.ServiceAccount != "" {
			v["serviceAccounts"] = []map[string]any{{"email": i.ServiceAccount, "scopes": []string{"https://www.googleapis.com/auth/cloud-platform"}}}
		}
		return v
	}
	sorted := func(m map[string]bool) []string {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return keys
	}
	ruleView := func(r *Rule) map[string]any {
		var ps []map[string]any
		for _, s := range r.Specs {
			proto, port, ok := strings.Cut(s, ":")
			p := map[string]any{"IPProtocol": proto}
			if ok {
				p["ports"] = []string{port}
			}
			ps = append(ps, p)
		}
		v := map[string]any{"name": r.Name, "description": r.Description, "network": url("global/networks/" + r.Network), "direction": "INGRESS",
			"priority": r.Priority, "disabled": r.Disabled, "sourceRanges": r.SourceRanges, "kind": "compute#firewall"}
		if len(r.TargetTags) > 0 {
			v["targetTags"] = r.TargetTags
		}
		if r.Action == "DENY" {
			v["denied"] = ps
		} else {
			v["allowed"] = ps
		}
		return v
	}
	addrView := func(a *Address) map[string]any {
		v := map[string]any{"name": a.Name, "address": a.IP, "description": a.Description, "addressType": "EXTERNAL", "networkTier": "PREMIUM",
			"region": url("regions/" + a.Region), "status": "RESERVED", "kind": "compute#address"}
		for _, i := range c.Instances {
			if i.IP == a.IP {
				v["status"], v["users"] = "IN_USE", []string{url("zones/" + i.Zone + "/instances/" + i.Name)}
			}
		}
		return v
	}
	snapView := func(s *Snapshot) map[string]any {
		status := s.Status
		if status == "" {
			switch {
			case s.polls < c.SnapshotPolls/2:
				status = "CREATING"
			case s.polls < c.SnapshotPolls:
				status = "UPLOADING"
			case c.SnapshotFails:
				status = "FAILED"
			default:
				status = "READY"
			}
		}
		s.polls++
		v := map[string]any{"id": "77" + strconv.Itoa(len(s.Name)), "name": s.Name, "status": status, "description": s.Description, "labels": s.Labels, "diskSizeGb": s.DiskGB,
			"sourceDisk": url("zones/" + strings.Replace(s.SourceDisk, "/", "/disks/", 1)), "storageLocations": []string{"eu"}, "architecture": "X86_64", "kind": "compute#snapshot"}
		if !s.Created.IsZero() {
			// As Google prints it: with the offset of the Pacific coast.
			v["creationTimestamp"] = s.Created.In(time.FixedZone("", -8*3600)).Format("2006-01-02T15:04:05.000-07:00")
		}
		if status == "READY" {
			v["storageBytes"], v["storageBytesStatus"] = "1401125952", "UP_TO_DATE"
		}
		return v
	}
	ruleFrom := func(r *Rule) string {
		if v, ok := flag["--rules"]; ok {
			r.Specs = strings.Split(v, ",")
		}
		if v, ok := flag["--source-ranges"]; ok {
			r.SourceRanges = strings.Split(v, ",")
		}
		if v, ok := flag["--priority"]; ok {
			p, err := strconv.Atoi(v)
			if err != nil || p < 0 || p > 65535 {
				return "Invalid value for field 'resource.priority': '" + v + "'"
			}
			r.Priority = p
		}
		if flag["--no-disabled"] != "" {
			r.Disabled = false
		}
		return ""
	}
	switch cmd {
	case "compute project-info describe":
		return send(map[string]any{"name": project, "id": "1234567890", "kind": "compute#project", "defaultServiceAccount": "1234567890-compute@developer.gserviceaccount.com"})
	case "compute zones list":
		out := []map[string]any{}
		for _, z := range c.Zones {
			out = append(out, map[string]any{"name": z, "status": "UP", "region": url("regions/" + region(z)), "kind": "compute#zone"})
		}
		out = append(out, map[string]any{"name": "europe-west3-c", "status": "DOWN", "region": url("regions/europe-west3")})
		return send(out)
	case "compute machine-types list":
		if !has(c.Zones, flag["--zones"]) {
			return fetch("Invalid value for field 'zone': '" + flag["--zones"] + "'. Unknown zone.")
		}
		return send(c.Types)
	case "compute networks list":
		out := []map[string]any{}
		for _, n := range c.Networks {
			out = append(out, map[string]any{"name": n, "autoCreateSubnetworks": true, "kind": "compute#network"})
		}
		return send(out)
	case "compute instances list":
		keys := map[string]bool{}
		for k := range c.Instances {
			keys[k] = true
		}
		out := []map[string]any{}
		for _, k := range sorted(keys) {
			if i := c.Instances[k]; matches(flag["--filter"], i.Name, i.Labels, i.Tags) {
				out = append(out, instView(i))
				i.polls++
			}
		}
		return send(out)
	case "compute instances create":
		zone := flag["--zone"]
		i := &Instance{Name: arg, Zone: zone, MachineType: flag["--machine-type"], Labels: map[string]string{}, Metadata: map[string]string{},
			Image: flag["--image-project"] + "/" + flag["--image-family"], Disk: flag["--boot-disk-size"], DiskType: flag["--boot-disk-type"], Net: flag["--network"], IP: flag["--address"]}
		switch {
		case !has(c.Zones, zone):
			return fetch("Invalid value for field 'zone': '" + zone + "'. Unknown zone.")
		case c.Instances[zone+"/"+arg] != nil:
			return fetch("The resource 'projects/" + project + "/zones/" + zone + "/instances/" + arg + "' already exists")
		case c.Disks[zone+"/"+arg]:
			return fetch("The resource 'projects/" + project + "/zones/" + zone + "/disks/" + arg + "' already exists")
		case !has(c.Networks, i.Net):
			return fetch("The resource 'projects/" + project + "/global/networks/" + i.Net + "' was not found")
		}
		if name, ok := flag["--source-snapshot"]; ok {
			s := c.Snapshots[name]
			if s == nil {
				return fetch("The resource 'projects/" + project + "/global/snapshots/" + name + "' was not found")
			}
			if snapView(s)["status"] != "READY" {
				return fetch("The resource 'projects/" + project + "/global/snapshots/" + name + "' is not ready")
			}
			i.Image, i.SourceSnapshot, i.Disk = "", name, s.DiskGB+"GB"
		}
		ok := false
		for _, t := range c.Types {
			ok = ok || t["name"] == i.MachineType
		}
		if !ok {
			return fetch("Invalid value for field 'resource.machineType': '" + i.MachineType + "'. Machine type with name '" + i.MachineType + "' does not exist in zone '" + zone + "'.")
		}
		for _, kv := range strings.Split(flag["--labels"], ",") {
			if k, v, ok := strings.Cut(kv, "="); ok {
				i.Labels[k] = v
			}
		}
		for _, kv := range strings.Split(flag["--metadata"], ",") {
			if k, v, ok := strings.Cut(kv, "="); ok {
				i.Metadata[k] = v
			}
		}
		if t := flag["--tags"]; t != "" {
			i.Tags = strings.Split(t, ",")
		}
		if flag["--no-service-account"] == "" {
			// As Compute Engine does unless told otherwise.
			i.ServiceAccount = "1234567890-compute@developer.gserviceaccount.com"
		}
		if key, path, ok := strings.Cut(flag["--metadata-from-file"], "="); ok {
			if fi, err := os.Stat(path); err == nil {
				i.UserDataMode = fi.Mode().Perm()
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return plain("Unable to read file [" + path + "]: [Errno 2] No such file or directory: '" + path + "'")
			}
			i.Metadata[key] = string(b)
			if key == "user-data" {
				i.UserData = string(b)
			}
		}
		c.add(i)
		v := instView(i)
		return send([]any{v})
	case "compute instances delete":
		key := flag["--zone"] + "/" + arg
		if c.Instances[key] == nil {
			return fetch("The resource 'projects/" + project + "/zones/" + flag["--zone"] + "/instances/" + arg + "' was not found")
		}
		if !c.Instances[key].KeepDisk {
			delete(c.Disks, key)
		}
		delete(c.Instances, key)
		return nil, []byte("Deleted [" + url("zones/"+key) + "].\n"), 0, nil
	case "compute instances stop", "compute instances start", "compute instances set-machine-type", "compute instances set-disk-auto-delete":
		key := flag["--zone"] + "/" + arg
		i := c.Instances[key]
		if i == nil {
			return fetch("The resource 'projects/" + project + "/zones/" + flag["--zone"] + "/instances/" + arg + "' was not found")
		}
		switch cmd {
		case "compute instances stop":
			i.Status = "TERMINATED"
			return []byte("[]\n"), []byte("Updated [" + url("zones/"+key) + "].\n"), 0, nil
		case "compute instances start":
			i.Status = ""
		case "compute instances set-machine-type":
			ok := false
			for _, t := range c.Types {
				ok = ok || t["name"] == flag["--machine-type"]
			}
			switch {
			case i.Status != "TERMINATED":
				return fetch("Invalid value for field 'resource.machineType': the instance must be stopped before its machine type can be changed.")
			case !ok:
				return fetch("Invalid value for field 'machineType': '" + flag["--machine-type"] + "'. Machine type with name '" + flag["--machine-type"] + "' does not exist in zone '" + i.Zone + "'.")
			}
			i.MachineType = flag["--machine-type"]
		case "compute instances set-disk-auto-delete":
			if flag["--disk"] != i.Name || flag["--auto-delete"] == "" {
				return plain("the fake takes --disk with the boot disk's name and --auto-delete")
			}
			i.KeepDisk = false
		}
		return send([]any{instView(i)})
	case "compute snapshots list":
		keys := map[string]bool{}
		for k := range c.Snapshots {
			keys[k] = true
		}
		out := []map[string]any{}
		for _, k := range sorted(keys) {
			if s := c.Snapshots[k]; matches(flag["--filter"], s.Name, s.Labels, nil) {
				out = append(out, snapView(s))
			}
		}
		return send(out)
	case "compute snapshots describe":
		s := c.Snapshots[arg]
		if s == nil {
			return fetch("The resource 'projects/" + project + "/global/snapshots/" + arg + "' was not found")
		}
		return send(snapView(s))
	case "compute snapshots create":
		disk := flag["--source-disk-zone"] + "/" + flag["--source-disk"]
		switch {
		case c.Snapshots[arg] != nil:
			return fetch("The resource 'projects/" + project + "/global/snapshots/" + arg + "' already exists")
		case !c.Disks[disk]:
			return fetch("The resource 'projects/" + project + "/zones/" + flag["--source-disk-zone"] + "/disks/" + flag["--source-disk"] + "' was not found")
		case flag["--async"] == "":
			return plain("the fake takes snapshots create with --async")
		}
		s := &Snapshot{Name: arg, Description: flag["--description"], SourceDisk: disk, DiskGB: "50", Labels: map[string]string{}, Created: time.Now().UTC().Truncate(time.Second)}
		if i := c.Instances[disk]; i != nil && i.Disk != "" {
			s.DiskGB = strings.TrimSuffix(i.Disk, "GB")
		}
		for _, kv := range strings.Split(flag["--labels"], ",") {
			if k, v, ok := strings.Cut(kv, "="); ok {
				s.Labels[k] = v
			}
		}
		c.Snapshots[arg] = s
		return send(map[string]any{"kind": "compute#operation", "operationType": "insert", "status": "RUNNING", "targetLink": url("global/snapshots/" + arg)})
	case "compute snapshots delete":
		if c.Snapshots[arg] == nil {
			return fetch("The resource 'projects/" + project + "/global/snapshots/" + arg + "' was not found")
		}
		delete(c.Snapshots, arg)
		return nil, []byte("Deleted [" + url("global/snapshots/"+arg) + "].\n"), 0, nil
	case "compute firewall-rules list":
		keys := map[string]bool{}
		for k := range c.Rules {
			keys[k] = true
		}
		out := []map[string]any{}
		for _, k := range sorted(keys) {
			out = append(out, ruleView(c.Rules[k]))
		}
		return send(out)
	case "compute firewall-rules create":
		if c.Rules[arg] != nil {
			return fetch("The resource 'projects/" + project + "/global/firewalls/" + arg + "' already exists")
		}
		r := &Rule{Name: arg, Description: flag["--description"], Network: flag["--network"], Action: flag["--action"], Priority: 1000}
		if flag["--direction"] != "INGRESS" || (r.Action != "ALLOW" && r.Action != "DENY") || !has(c.Networks, r.Network) {
			return plain("Invalid value for [--action]: the fake takes --direction INGRESS, --action ALLOW or DENY and a network that exists")
		}
		if t := flag["--target-tags"]; t != "" {
			r.TargetTags = strings.Split(t, ",")
		}
		if msg := ruleFrom(r); msg != "" {
			return fetch(msg)
		}
		c.Rules[arg] = r
		return send([]any{ruleView(r)})
	case "compute firewall-rules update":
		r := c.Rules[arg]
		if r == nil {
			return fetch("The resource 'projects/" + project + "/global/firewalls/" + arg + "' was not found")
		}
		if msg := ruleFrom(r); msg != "" {
			return fetch(msg)
		}
		return nil, []byte("Updated [" + url("global/firewalls/"+arg) + "].\n"), 0, nil
	case "compute firewall-rules delete":
		if c.Rules[arg] == nil {
			return fetch("The resource 'projects/" + project + "/global/firewalls/" + arg + "' was not found")
		}
		delete(c.Rules, arg)
		return nil, []byte("Deleted [" + url("global/firewalls/"+arg) + "].\n"), 0, nil
	case "compute addresses list":
		keys := map[string]bool{}
		for k := range c.Addresses {
			keys[k] = true
		}
		out := []map[string]any{}
		for _, k := range sorted(keys) {
			if a := c.Addresses[k]; matches(flag["--filter"], a.Name, nil, nil) {
				out = append(out, addrView(a))
			}
		}
		return send(out)
	case "compute addresses create":
		key := flag["--region"] + "/" + arg
		if c.Addresses[key] != nil {
			return fetch("The resource 'projects/" + project + "/regions/" + flag["--region"] + "/addresses/" + arg + "' already exists")
		}
		c.next++
		a := &Address{Name: arg, Region: flag["--region"], Description: flag["--description"], IP: "198.51.100." + strconv.FormatInt(c.next-4235, 10)}
		c.Addresses[key] = a
		return send([]any{addrView(a)})
	case "compute addresses delete":
		key := flag["--region"] + "/" + arg
		a := c.Addresses[key]
		if a == nil {
			return fetch("The resource 'projects/" + project + "/regions/" + flag["--region"] + "/addresses/" + arg + "' was not found")
		}
		if addrView(a)["status"] == "IN_USE" {
			return fetch("The address resource 'projects/" + project + "/regions/" + a.Region + "/addresses/" + arg + "' is already being used by another resource")
		}
		delete(c.Addresses, key)
		return nil, []byte("Deleted [" + url("regions/"+key) + "].\n"), 0, nil
	}
	return nil, []byte(fmt.Sprintf("ERROR: (gcloud) Invalid choice: '%s'.\n", cmd)), 2, nil
}
