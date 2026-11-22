// Package awstest is a fake AWS CLI for tests: an in-memory Lightsail
// account behind the same arguments and JSON the aws command takes and
// prints, with its errors in the CLI's form ("aws: [ERROR]: An error
// occurred (Code) when calling the Op operation: message") and its exit
// codes: 252 for arguments it refuses, 253 without credentials, 254 for a
// service error, 255 for an unknown profile or when no endpoint answers.
// The contract tests in the aws package hold the real CLI to the same.
package awstest

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

// Instance is an instance in the fake account.
type Instance struct {
	Name, Zone, Blueprint, Bundle, IP string
	Tags                              map[string]string
	// UserData is what the file:// argument held, and UserDataMode that
	// file's mode.
	UserData     string
	UserDataMode os.FileMode
	Ports        []map[string]any
	// State is "stopped" or "stopping"; empty is running, after
	// PendingPolls answers of "pending".
	State string
	// DiskGB is the size of the system disk, and FromSnapshot the snapshot
	// the instance was created from.
	DiskGB       int
	FromSnapshot string
	// polls counts get-instance calls since creation.
	polls int
}

// Snapshot is an instance snapshot in the fake account.
type Snapshot struct {
	Name, From, Bundle, Blueprint string
	SizeGB                        int
	Tags                          map[string]string
	// State is "error" for a snapshot that failed; otherwise it is
	// "pending" for SnapshotPolls answers and then "available".
	State string
	// Created is the time the snapshot reports, in seconds since 1970.
	Created int64
	polls   int
}

// CLI is the fake. Fields may be changed between calls.
type CLI struct {
	mu sync.Mutex
	// SignedIn is whether sts get-caller-identity answers; Profile, when
	// set, is the only --profile the CLI knows.
	SignedIn bool
	Profile  string
	// Regions offer Lightsail; any other --region has no endpoint.
	Regions []string
	// Bundles and Blueprints are the catalogue, as get-bundles and
	// get-blueprints print them.
	Bundles    []map[string]any
	Blueprints []map[string]any
	Instances  map[string]*Instance
	// StaticIPs maps a static IP's name to the instance it is attached
	// to ("" for none); StaticAddr holds its address.
	StaticIPs  map[string]string
	StaticAddr map[string]string
	// ConfigRegion is what "aws configure get region" prints; empty is
	// no region configured.
	ConfigRegion string
	// PendingPolls is how many get-instance calls a new instance answers
	// "pending" before it runs.
	PendingPolls int
	// Snapshots are the account's instance snapshots. SnapshotPolls is how
	// many get-instance-snapshot calls a new one answers "pending" before
	// it is available, and SnapshotFails makes a new one end in "error".
	Snapshots     map[string]*Snapshot
	SnapshotPolls int
	SnapshotFails bool
	// StuckStopping keeps an instance "stopping" until it is forced.
	StuckStopping bool
	// PortsNeedRunning refuses firewall rules for an instance that is
	// still pending.
	PortsNeedRunning bool
	// FailWith makes an operation fail when it is called with an argument:
	// "lightsail create-instances-from-snapshot --bundle-id large_3_0".
	FailWith map[string]string
	// Fail makes the named operation ("lightsail attach-static-ip") fail
	// with a service error.
	Fail map[string]string
	// Secrets must never appear in an argument; Leaked records any that did.
	Secrets []string
	Leaked  []string
	calls   []string
	nextIP  int
	clock   int64
}

// New is a signed-in account with a realistic catalogue and no instances.
func New() *CLI {
	one, zero := 1, 0
	return &CLI{
		SignedIn: true,
		Regions:  []string{"eu-central-1", "us-east-1"},
		Bundles: []map[string]any{
			{"bundleId": "nano_3_0", "name": "Nano", "price": 5.0, "cpuCount": 2, "ramSizeInGb": 0.5, "diskSizeInGb": 20, "transferPerMonthInGb": 1024, "isActive": true, "supportedPlatforms": []string{"LINUX_UNIX"}, "publicIpv4AddressCount": one},
			{"bundleId": "small_3_0", "name": "Small", "price": 12.0, "cpuCount": 2, "ramSizeInGb": 2.0, "diskSizeInGb": 60, "transferPerMonthInGb": 3072, "isActive": true, "supportedPlatforms": []string{"LINUX_UNIX"}, "publicIpv4AddressCount": one},
			{"bundleId": "medium_3_0", "name": "Medium", "price": 24.0, "cpuCount": 2, "ramSizeInGb": 4.0, "diskSizeInGb": 80, "transferPerMonthInGb": 4096, "isActive": true, "supportedPlatforms": []string{"LINUX_UNIX"}, "publicIpv4AddressCount": one},
			{"bundleId": "large_3_0", "name": "Large", "price": 44.0, "cpuCount": 2, "ramSizeInGb": 8.0, "diskSizeInGb": 160, "transferPerMonthInGb": 5120, "isActive": true, "supportedPlatforms": []string{"LINUX_UNIX"}, "publicIpv4AddressCount": one},
			{"bundleId": "medium_ipv6_3_0", "name": "Medium", "price": 20.0, "cpuCount": 2, "ramSizeInGb": 4.0, "diskSizeInGb": 80, "transferPerMonthInGb": 4096, "isActive": true, "supportedPlatforms": []string{"LINUX_UNIX"}, "publicIpv4AddressCount": zero},
			{"bundleId": "medium_win_3_0", "name": "Medium", "price": 20.0, "cpuCount": 2, "ramSizeInGb": 4.0, "diskSizeInGb": 80, "transferPerMonthInGb": 4096, "isActive": true, "supportedPlatforms": []string{"WINDOWS"}, "publicIpv4AddressCount": one},
			{"bundleId": "medium_2_0", "name": "Medium", "price": 20.0, "cpuCount": 2, "ramSizeInGb": 4.0, "diskSizeInGb": 80, "transferPerMonthInGb": 4096, "isActive": false, "supportedPlatforms": []string{"LINUX_UNIX"}, "publicIpv4AddressCount": one},
		},
		Blueprints: []map[string]any{
			{"blueprintId": "ubuntu_24_04", "name": "Ubuntu", "group": "ubuntu_24", "type": "os", "isActive": true, "platform": "LINUX_UNIX"},
			{"blueprintId": "ubuntu_22_04", "name": "Ubuntu", "group": "ubuntu_22", "type": "os", "isActive": true, "platform": "LINUX_UNIX"},
			{"blueprintId": "ubuntu_20_04", "name": "Ubuntu", "group": "ubuntu_20", "type": "os", "isActive": false, "platform": "LINUX_UNIX"},
			{"blueprintId": "wordpress", "name": "WordPress", "group": "wordpress", "type": "app", "isActive": true, "platform": "LINUX_UNIX"},
		},
		Instances: map[string]*Instance{}, StaticIPs: map[string]string{}, StaticAddr: map[string]string{},
		Snapshots: map[string]*Snapshot{}, Fail: map[string]string{}, FailWith: map[string]string{},
	}
}

// Calls lists every call as "service operation arg...", without the
// global options.
func (c *CLI) Calls() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.calls...)
}

// Called reports whether an operation ("lightsail delete-instance") ran.
func (c *CLI) Called(op string) bool {
	for _, l := range c.Calls() {
		if l == op || strings.HasPrefix(l, op+" ") {
			return true
		}
	}
	return false
}

func fail(code, op, msg string) ([]byte, []byte, int, error) {
	// The CLI names the API operation: GetInstance for get-instance.
	parts := strings.Split(op, "-")
	for i, p := range parts {
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	op = strings.Join(parts, "")
	return cliError(254, fmt.Sprintf("An error occurred (%s) when calling the %s operation: %s", code, op, msg))
}

// cliError is the line the CLI prints for an error, and its exit code.
func cliError(exit int, msg string) ([]byte, []byte, int, error) {
	return nil, []byte("\naws: [ERROR]: " + msg + "\n"), exit, nil
}

func (c *CLI) addr() string {
	c.nextIP++
	return "203.0.113." + strconv.Itoa(c.nextIP)
}

// Run is aws.Runner.
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
	// Split the global options off.
	var rest []string
	opt := map[string]string{}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--output", "--region", "--profile", "--cli-connect-timeout", "--cli-read-timeout":
			if i+1 < len(args) {
				opt[args[i]] = args[i+1]
				i++
			}
		case "--no-cli-pager":
		default:
			rest = append(rest, args[i])
		}
	}
	if len(rest) < 2 {
		return cliError(252, "the following arguments are required: operation")
	}
	c.calls = append(c.calls, strings.Join(rest, " "))
	// A profile the CLI does not have stops every command, configure too.
	if p := opt["--profile"]; p != "" && c.Profile != "" && p != c.Profile {
		return cliError(255, fmt.Sprintf("The config profile (%s) could not be found", p))
	}
	if len(rest) == 3 && strings.Join(rest, " ") == "configure get region" {
		if c.ConfigRegion == "" {
			return nil, nil, 1, nil
		}
		return []byte(c.ConfigRegion + "\n"), nil, 0, nil
	}
	if opt["--output"] != "json" {
		return cliError(252, "the fake needs --output json")
	}
	service, op := rest[0], rest[1]
	// Operation arguments: "--name value value ...".
	arg := map[string][]string{}
	last := ""
	for _, a := range rest[2:] {
		if strings.HasPrefix(a, "--") {
			last = a
			arg[last] = []string{}
			continue
		}
		arg[last] = append(arg[last], a)
	}
	one := func(name string) string {
		if v := arg[name]; len(v) > 0 {
			return v[0]
		}
		return ""
	}
	send := func(v any) ([]byte, []byte, int, error) {
		b, _ := json.Marshal(v)
		return b, nil, 0, nil
	}
	if !c.SignedIn {
		return cliError(253, "Unable to locate credentials. You can configure credentials by running \"aws login\".")
	}
	if msg, ok := c.Fail[service+" "+op]; ok {
		return fail("ServiceException", op, msg)
	}
	for name, values := range arg {
		for _, v := range values {
			if msg, ok := c.FailWith[service+" "+op+" "+name+" "+v]; ok {
				return fail("ServiceException", op, msg)
			}
		}
	}
	if service == "sts" && op == "get-caller-identity" {
		return send(map[string]string{"UserId": "AIDAEXAMPLE", "Account": "123456789012", "Arn": "arn:aws:iam::123456789012:user/sam"})
	}
	if service != "lightsail" {
		return cliError(252, "argument command: Found invalid choice '"+service+"'")
	}
	region := opt["--region"]
	known := false
	for _, r := range c.Regions {
		known = known || r == region
	}
	if !known {
		return cliError(255, fmt.Sprintf("Could not connect to the endpoint URL: \"https://lightsail.%s.amazonaws.com/\"", region))
	}
	view := func(in *Instance) map[string]any {
		state := "running"
		if in.State != "" {
			state = in.State
		} else if in.polls <= c.PendingPolls {
			state = "pending"
		}
		tags := tagList(in.Tags)
		static := false
		for _, to := range c.StaticIPs {
			static = static || to == in.Name
		}
		return map[string]any{"name": in.Name, "arn": "arn:aws:lightsail:" + region + ":123456789012:Instance/" + in.Name,
			"blueprintId": in.Blueprint, "bundleId": in.Bundle, "isStaticIp": static, "publicIpAddress": in.IP, "tags": tags,
			"state": map[string]any{"code": 16, "name": state}, "username": "ubuntu",
			"hardware": map[string]any{"disks": []map[string]any{{"sizeInGb": in.DiskGB, "isSystemDisk": true}}},
			"location": map[string]string{"availabilityZone": in.Zone, "regionName": region}}
	}
	missing := func(what, name string) ([]byte, []byte, int, error) {
		return fail("NotFoundException", op, "The "+what+" does not exist: "+name)
	}
	switch op {
	case "get-regions":
		var out []map[string]any
		for _, r := range c.Regions {
			out = append(out, map[string]any{"name": r, "displayName": r, "availabilityZones": []map[string]string{
				{"zoneName": r + "a", "state": "available"}, {"zoneName": r + "b", "state": "available"}}})
		}
		return send(map[string]any{"regions": out})
	case "get-bundles":
		_, inactive := arg["--include-inactive"]
		var out []map[string]any
		for _, b := range c.Bundles {
			if inactive || b["isActive"] == true {
				out = append(out, b)
			}
		}
		return send(map[string]any{"bundles": out})
	case "get-blueprints":
		return send(map[string]any{"blueprints": c.Blueprints})
	case "get-instance":
		in := c.Instances[one("--instance-name")]
		if in == nil {
			return missing("Instance", one("--instance-name"))
		}
		in.polls++
		return send(map[string]any{"instance": view(in)})
	case "get-instances":
		var out []map[string]any
		names := make([]string, 0, len(c.Instances))
		for n := range c.Instances {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			out = append(out, view(c.Instances[n]))
		}
		return send(map[string]any{"instances": out})
	case "create-instances":
		name := one("--instance-names")
		if c.Instances[name] != nil {
			return fail("InvalidInputException", op, "Some names are already in use: "+name)
		}
		ok := false
		for _, b := range c.Bundles {
			ok = ok || b["bundleId"] == one("--bundle-id")
		}
		if !ok {
			return fail("InvalidInputException", op, "The bundle "+one("--bundle-id")+" does not exist")
		}
		ok = false
		for _, b := range c.Blueprints {
			ok = ok || b["blueprintId"] == one("--blueprint-id")
		}
		if !ok {
			return fail("InvalidInputException", op, "The blueprint "+one("--blueprint-id")+" does not exist")
		}
		in := &Instance{Name: name, Zone: one("--availability-zone"), Blueprint: one("--blueprint-id"), Bundle: one("--bundle-id"),
			IP: c.addr(), Tags: parseTags(arg["--tags"]), DiskGB: c.disk(one("--bundle-id"))}
		if ud := one("--user-data"); strings.HasPrefix(ud, "file://") {
			path := strings.TrimPrefix(ud, "file://")
			if fi, err := os.Stat(path); err == nil {
				in.UserDataMode = fi.Mode().Perm()
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return cliError(252, "Error parsing parameter '--user-data': Unable to load paramfile "+ud+": "+err.Error())
			}
			in.UserData = string(b)
		} else {
			in.UserData = ud
		}
		in.Ports = newPorts()
		c.Instances[name] = in
		return send(map[string]any{"operations": []map[string]any{{"id": "op-1", "resourceName": name, "status": "Started"}}})
	case "delete-instance":
		name := one("--instance-name")
		if c.Instances[name] == nil {
			return missing("Instance", name)
		}
		delete(c.Instances, name)
		for n, to := range c.StaticIPs {
			if to == name {
				c.StaticIPs[n] = "" // Lightsail keeps the static IP
			}
		}
		return send(map[string]any{"operations": []map[string]any{{"id": "op-2", "resourceName": name, "status": "Succeeded"}}})
	case "get-instance-port-states":
		in := c.Instances[one("--instance-name")]
		if in == nil {
			return missing("Instance", one("--instance-name"))
		}
		return send(map[string]any{"portStates": in.Ports})
	case "put-instance-public-ports":
		in := c.Instances[one("--instance-name")]
		if in == nil {
			return missing("Instance", one("--instance-name"))
		}
		if c.PortsNeedRunning && in.State == "" && in.polls <= c.PendingPolls {
			return fail("OperationFailureException", op, "The instance is not in a state that takes this operation: pending")
		}
		in.Ports = nil
		for _, spec := range arg["--port-infos"] {
			p := map[string]any{"state": "open"}
			key := ""
			for _, part := range strings.Split(spec, ",") {
				k, v, ok := strings.Cut(part, "=")
				if !ok { // another value of the list before it
					p[key] = append(p[key].([]string), part)
					continue
				}
				key = k
				switch k {
				case "fromPort", "toPort":
					n, _ := strconv.Atoi(v)
					p[k] = n
				case "protocol":
					p[k] = v
				default:
					p[k] = []string{v}
				}
			}
			in.Ports = append(in.Ports, p)
		}
		return send(map[string]any{"operation": map[string]any{"id": "op-3", "status": "Succeeded"}})
	case "allocate-static-ip":
		name := one("--static-ip-name")
		if _, ok := c.StaticIPs[name]; ok {
			return fail("InvalidInputException", op, "Some names are already in use: "+name)
		}
		c.StaticIPs[name], c.StaticAddr[name] = "", c.addr()
		return send(map[string]any{"operations": []map[string]any{{"id": "op-4", "status": "Succeeded"}}})
	case "attach-static-ip":
		name, to := one("--static-ip-name"), one("--instance-name")
		if _, ok := c.StaticIPs[name]; !ok {
			return missing("StaticIp", name)
		}
		if c.Instances[to] == nil {
			return missing("Instance", to)
		}
		if cur := c.StaticIPs[name]; cur != "" && cur != to {
			return fail("InvalidInputException", op, "The static IP is attached to "+cur)
		}
		c.StaticIPs[name] = to
		c.Instances[to].IP = c.StaticAddr[name]
		return send(map[string]any{"operations": []map[string]any{{"id": "op-5", "status": "Succeeded"}}})
	case "get-static-ip":
		name := one("--static-ip-name")
		to, ok := c.StaticIPs[name]
		if !ok {
			return missing("StaticIp", name)
		}
		ip := map[string]any{"name": name, "ipAddress": c.StaticAddr[name], "isAttached": to != ""}
		if to != "" {
			ip["attachedTo"] = to
		}
		return send(map[string]any{"staticIp": ip})
	case "get-static-ips":
		var out []map[string]any
		for name, to := range c.StaticIPs {
			out = append(out, map[string]any{"name": name, "ipAddress": c.StaticAddr[name], "isAttached": to != "", "attachedTo": to})
		}
		return send(map[string]any{"staticIps": out})
	case "release-static-ip":
		name := one("--static-ip-name")
		if _, ok := c.StaticIPs[name]; !ok {
			return missing("StaticIp", name)
		}
		delete(c.StaticIPs, name)
		delete(c.StaticAddr, name)
		return send(map[string]any{"operations": []map[string]any{{"id": "op-6", "status": "Succeeded"}}})
	case "detach-static-ip":
		name := one("--static-ip-name")
		to, ok := c.StaticIPs[name]
		if !ok {
			return missing("StaticIp", name)
		}
		if in := c.Instances[to]; in != nil {
			in.IP = c.addr()
		}
		c.StaticIPs[name] = ""
		return send(map[string]any{"operations": []map[string]any{{"id": "op-7", "status": "Succeeded"}}})
	case "stop-instance":
		in := c.Instances[one("--instance-name")]
		_, force := arg["--force"]
		switch {
		case in == nil:
			return missing("Instance", one("--instance-name"))
		case in.State == "stopping" && force:
			in.State = "stopped"
		case in.State != "":
			return fail("OperationFailureException", op, "The instance is "+in.State)
		case c.StuckStopping:
			in.State = "stopping"
		default:
			in.State = "stopped"
		}
		return send(map[string]any{"operations": []map[string]any{{"id": "op-8", "status": "Started"}}})
	case "start-instance":
		in := c.Instances[one("--instance-name")]
		if in == nil {
			return missing("Instance", one("--instance-name"))
		}
		if in.State != "stopped" {
			return fail("OperationFailureException", op, "The instance is not stopped")
		}
		in.State, in.polls = "", 0
		return send(map[string]any{"operations": []map[string]any{{"id": "op-9", "status": "Started"}}})
	case "create-instance-snapshot":
		name, from := one("--instance-snapshot-name"), c.Instances[one("--instance-name")]
		if from == nil {
			return missing("Instance", one("--instance-name"))
		}
		if c.Snapshots[name] != nil {
			return fail("InvalidInputException", op, "Some names are already in use: "+name)
		}
		c.clock += 60
		snap := &Snapshot{Name: name, From: from.Name, Bundle: from.Bundle, Blueprint: from.Blueprint, SizeGB: from.DiskGB,
			Tags: parseTags(arg["--tags"]), Created: 1794000000 + c.clock}
		if c.SnapshotFails {
			snap.State = "error"
		}
		c.Snapshots[name] = snap
		return send(map[string]any{"operations": []map[string]any{{"id": "op-10", "resourceName": name, "status": "Started"}}})
	case "get-instance-snapshot":
		snap := c.Snapshots[one("--instance-snapshot-name")]
		if snap == nil {
			return missing("InstanceSnapshot", one("--instance-snapshot-name"))
		}
		view := c.snapshotView(snap)
		snap.polls++
		return send(map[string]any{"instanceSnapshot": view})
	case "get-instance-snapshots":
		var out []map[string]any
		names := make([]string, 0, len(c.Snapshots))
		for n := range c.Snapshots {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			out = append(out, c.snapshotView(c.Snapshots[n]))
		}
		return send(map[string]any{"instanceSnapshots": out})
	case "delete-instance-snapshot":
		name := one("--instance-snapshot-name")
		if c.Snapshots[name] == nil {
			return missing("InstanceSnapshot", name)
		}
		delete(c.Snapshots, name)
		return send(map[string]any{"operations": []map[string]any{{"id": "op-11", "resourceName": name, "status": "Succeeded"}}})
	case "create-instances-from-snapshot":
		name, snap := one("--instance-names"), c.Snapshots[one("--instance-snapshot-name")]
		switch {
		case c.Instances[name] != nil:
			return fail("InvalidInputException", op, "Some names are already in use: "+name)
		case snap == nil:
			return missing("InstanceSnapshot", one("--instance-snapshot-name"))
		case c.snapshotState(snap) != "available":
			return fail("InvalidInputException", op, "The snapshot is not available")
		case c.disk(one("--bundle-id")) == 0:
			return fail("InvalidInputException", op, "The bundle "+one("--bundle-id")+" does not exist")
		case c.disk(one("--bundle-id")) < snap.SizeGB:
			return fail("InvalidInputException", op, "The bundle is smaller than the snapshot")
		}
		c.Instances[name] = &Instance{Name: name, Zone: one("--availability-zone"), Blueprint: snap.Blueprint, Bundle: one("--bundle-id"),
			IP: c.addr(), Tags: parseTags(arg["--tags"]), DiskGB: c.disk(one("--bundle-id")), FromSnapshot: snap.Name,
			UserData: one("--user-data"), Ports: newPorts()}
		return send(map[string]any{"operations": []map[string]any{{"id": "op-12", "resourceName": name, "status": "Started"}}})
	case "tag-resource", "untag-resource":
		name := one("--resource-name")
		var tags map[string]string
		if in := c.Instances[name]; in != nil {
			tags = in.Tags
		} else if snap := c.Snapshots[name]; snap != nil {
			tags = snap.Tags
		} else {
			return missing("Resource", name)
		}
		for k, v := range parseTags(arg["--tags"]) {
			tags[k] = v
		}
		for _, k := range arg["--tag-keys"] {
			delete(tags, k)
		}
		return send(map[string]any{"operations": []map[string]any{{"id": "op-13", "resourceName": name, "status": "Succeeded"}}})
	}
	return cliError(252, "argument operation: Found invalid choice '"+op+"'")
}

func parseTags(args []string) map[string]string {
	tags := map[string]string{}
	for _, t := range args {
		k, v, _ := strings.Cut(t, ",value=")
		tags[strings.TrimPrefix(k, "key=")] = v
	}
	return tags
}

func tagList(tags map[string]string) []map[string]string {
	keys := make([]string, 0, len(tags))
	for k := range tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []map[string]string
	for _, k := range keys {
		out = append(out, map[string]string{"key": k, "value": tags[k]})
	}
	return out
}

// newPorts is the firewall Lightsail gives a new instance.
func newPorts() []map[string]any {
	any4 := []string{"0.0.0.0/0"}
	return []map[string]any{
		{"fromPort": 22, "toPort": 22, "protocol": "tcp", "state": "open", "cidrs": any4, "ipv6Cidrs": []string{"::/0"}},
		{"fromPort": 80, "toPort": 80, "protocol": "tcp", "state": "open", "cidrs": any4, "ipv6Cidrs": []string{"::/0"}}}
}

// disk is the disk size of a bundle, or 0 for one the catalogue lacks.
func (c *CLI) disk(bundle string) int {
	for _, b := range c.Bundles {
		if b["bundleId"] == bundle {
			n, _ := b["diskSizeInGb"].(int)
			return n
		}
	}
	return 0
}

func (c *CLI) snapshotState(s *Snapshot) string {
	switch {
	case s.State != "":
		return s.State
	case s.polls < c.SnapshotPolls:
		return "pending"
	}
	return "available"
}

// snapshotView is a snapshot as the CLI prints it, the time as ISO 8601.
func (c *CLI) snapshotView(s *Snapshot) map[string]any {
	return map[string]any{"name": s.Name, "resourceType": "InstanceSnapshot", "state": c.snapshotState(s),
		"createdAt":        time.Unix(s.Created, 123000000).UTC().Format("2006-01-02T15:04:05.000000-07:00"),
		"fromInstanceName": s.From, "fromBundleId": s.Bundle, "fromBlueprintId": s.Blueprint, "sizeInGb": s.SizeGB,
		"isFromAutoSnapshot": false, "tags": tagList(s.Tags)}
}
