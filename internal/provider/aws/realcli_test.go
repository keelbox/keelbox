package aws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Contract tests against the real AWS CLI (docs/contributing/testing.md).
// They need no AWS account: the CLI named by KEELBOX_TEST_AWS_CLI is
// pointed at a local stub of Lightsail and STS, with made-up credentials,
// so every command the provider issues passes the CLI's own argument
// validation, and keelbox reads what the CLI really prints.

const realCLIVar = "KEELBOX_TEST_AWS_CLI"

// stubRequest is one request the stub received from the CLI.
type stubRequest struct {
	Op     string         // the API operation: GetInstance, GetCallerIdentity
	Body   map[string]any // the JSON body of a Lightsail request
	Region string         // the region the CLI signed the request for
}

// stubError is an error the stub answers an operation with.
type stubError struct {
	Status int
	Body   string
}

func lightsailError(code, message string) stubError {
	b, _ := json.Marshal(map[string]string{"__type": code, "message": message})
	return stubError{Status: 400, Body: string(b)}
}

// stsError is an error in the form of STS's query protocol.
func stsError(status int, code, message string) stubError {
	return stubError{Status: status, Body: `<ErrorResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><Error><Type>Sender</Type><Code>` +
		code + `</Code><Message>` + message + `</Message></Error><RequestId>01234567-89ab-cdef-0123-456789abcdef</RequestId></ErrorResponse>`}
}

// stub is a one-box Lightsail account and STS, speaking their wire
// protocols: JSON 1.1 with an X-Amz-Target header, and the query protocol
// with XML answers.
type stub struct {
	mu       sync.Mutex
	requests []stubRequest
	// Fail answers the named operation with an error.
	Fail map[string]stubError
	// PageSize, when set, cuts list answers into pages of that size.
	PageSize int
	// Pending is how many GetInstance calls a new instance answers
	// "pending".
	Pending int

	created  map[string]any // the CreateInstances request, nil for no instance
	ports    []any
	staticIP string // the static IP's name, "" for none
	attached bool
	stopped  bool
	// snapshots are the instance snapshots by name, as GetInstanceSnapshot
	// answers; SnapshotPending is how many of those answers say "pending".
	snapshots       map[string]map[string]any
	SnapshotPending int
}

const stubInstanceIP, stubStaticIP = "203.0.113.10", "203.0.113.20"

var (
	stubBundles = []any{
		map[string]any{"bundleId": "nano_3_0", "name": "Nano", "price": 5.0, "cpuCount": 2, "ramSizeInGb": 0.5, "diskSizeInGb": 20, "transferPerMonthInGb": 1024, "isActive": true, "instanceType": "nano", "power": 500, "supportedPlatforms": []string{"LINUX_UNIX"}, "supportedAppCategories": []string{}, "publicIpv4AddressCount": 1},
		map[string]any{"bundleId": "medium_3_0", "name": "Medium", "price": 24.0, "cpuCount": 2, "ramSizeInGb": 4.0, "diskSizeInGb": 80, "transferPerMonthInGb": 4096, "isActive": true, "instanceType": "medium", "power": 2000, "supportedPlatforms": []string{"LINUX_UNIX"}, "supportedAppCategories": []string{}, "publicIpv4AddressCount": 1},
		map[string]any{"bundleId": "medium_ipv6_3_0", "name": "Medium", "price": 20.0, "cpuCount": 2, "ramSizeInGb": 4.0, "diskSizeInGb": 80, "transferPerMonthInGb": 4096, "isActive": true, "instanceType": "medium", "power": 2000, "supportedPlatforms": []string{"LINUX_UNIX"}, "supportedAppCategories": []string{}, "publicIpv4AddressCount": 0},
	}
	stubBlueprints = []any{
		map[string]any{"blueprintId": "ubuntu_24_04", "name": "Ubuntu", "group": "ubuntu_24", "type": "os", "description": "Ubuntu 24.04 LTS", "isActive": true, "minPower": 0, "version": "24.04 LTS", "platform": "LINUX_UNIX"},
		map[string]any{"blueprintId": "ubuntu_22_04", "name": "Ubuntu", "group": "ubuntu_22", "type": "os", "description": "Ubuntu 22.04 LTS", "isActive": true, "minPower": 0, "version": "22.04 LTS", "platform": "LINUX_UNIX"},
		map[string]any{"blueprintId": "wordpress", "name": "WordPress", "group": "wordpress", "type": "app", "description": "WordPress", "isActive": true, "minPower": 0, "version": "6.8", "platform": "LINUX_UNIX"},
	}
)

var signedFor = regexp.MustCompile(`Credential=[^/]+/\d+/([a-z0-9-]+)/`)

func (s *stub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, _ := io.ReadAll(r.Body)
	req := stubRequest{}
	if m := signedFor.FindStringSubmatch(r.Header.Get("Authorization")); m != nil {
		req.Region = m[1]
	}
	if target := r.Header.Get("X-Amz-Target"); target != "" {
		req.Op = strings.TrimPrefix(target, "Lightsail_20161128.")
		_ = json.Unmarshal(raw, &req.Body)
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	} else {
		form, _ := url.ParseQuery(string(raw))
		req.Op = form.Get("Action")
		w.Header().Set("Content-Type", "text/xml")
	}
	s.requests = append(s.requests, req)
	if e, ok := s.Fail[req.Op]; ok {
		w.WriteHeader(e.Status)
		_, _ = io.WriteString(w, e.Body)
		return
	}
	out, fail := s.answer(req)
	switch {
	case fail != nil:
		w.WriteHeader(fail.Status)
		_, _ = io.WriteString(w, fail.Body)
	case req.Op == "GetCallerIdentity":
		_, _ = io.WriteString(w, `<GetCallerIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><GetCallerIdentityResult>`+
			`<Arn>arn:aws:iam::123456789012:user/sam</Arn><UserId>AIDAEXAMPLEEXAMPLEEXA</UserId><Account>123456789012</Account>`+
			`</GetCallerIdentityResult><ResponseMetadata><RequestId>01234567-89ab-cdef-0123-456789abcdef</RequestId></ResponseMetadata></GetCallerIdentityResponse>`)
	default:
		_ = json.NewEncoder(w).Encode(out)
	}
}

// page answers a list operation, in pages when PageSize is set.
func (s *stub) page(req stubRequest, key string, items []any) map[string]any {
	if s.PageSize == 0 {
		return map[string]any{key: items}
	}
	from, _ := strconv.Atoi(fmt.Sprint(req.Body["pageToken"]))
	to := min(from+s.PageSize, len(items))
	out := map[string]any{key: items[from:to]}
	if to < len(items) {
		out["nextPageToken"] = strconv.Itoa(to)
	}
	return out
}

func (s *stub) instance() map[string]any {
	state, ip := map[string]any{"code": 16, "name": "running"}, stubInstanceIP
	if s.stopped {
		state = map[string]any{"code": 80, "name": "stopped"}
	} else if s.Pending > 0 {
		s.Pending--
		state = map[string]any{"code": 0, "name": "pending"}
	}
	if s.attached {
		ip = stubStaticIP
	}
	name := s.created["instanceNames"].([]any)[0].(string)
	zone := s.created["availabilityZone"].(string)
	return map[string]any{"name": name, "arn": "arn:aws:lightsail:" + zone[:len(zone)-1] + ":123456789012:Instance/01234567-89ab-cdef-0123-456789abcdef",
		"supportCode": "123456789012/i-0123456789abcdef0", "createdAt": 1794000000.123, "resourceType": "Instance",
		"location": map[string]any{"availabilityZone": zone, "regionName": zone[:len(zone)-1]}, "tags": s.created["tags"],
		"blueprintId": s.created["blueprintId"], "blueprintName": "Ubuntu", "bundleId": s.created["bundleId"],
		"isStaticIp": s.attached, "privateIpAddress": "172.26.0.10", "publicIpAddress": ip, "ipAddressType": "dualstack",
		"hardware": map[string]any{"cpuCount": 2, "ramSizeInGb": 4.0, "disks": []any{map[string]any{"createdAt": 1794000000.123, "sizeInGb": 80, "isSystemDisk": true,
			"iops": 240, "path": "/dev/xvda", "attachedTo": name, "attachmentState": "attached"}}},
		"networking": map[string]any{"monthlyTransfer": map[string]any{"gbPerMonthAllocated": 4096}, "ports": []any{}},
		"state":      state, "username": "ubuntu", "sshKeyName": "LightsailDefaultKeyPair"}
}

func (s *stub) answer(req stubRequest) (map[string]any, *stubError) {
	missing := func(what string, name any) (map[string]any, *stubError) {
		e := lightsailError("NotFoundException", fmt.Sprintf("The %s does not exist: %v", what, name))
		return nil, &e
	}
	op := func(resource any) map[string]any {
		return map[string]any{"id": "01234567-89ab-cdef-0123-456789abcdef", "resourceName": resource, "createdAt": 1794000000.5,
			"operationType": req.Op, "status": "Succeeded", "isTerminal": true}
	}
	hasInstance := s.created != nil && s.created["instanceNames"].([]any)[0] == req.Body["instanceName"]
	switch req.Op {
	case "GetCallerIdentity":
		return nil, nil
	case "GetRegions":
		zones := func(r string) []any {
			if req.Body["includeAvailabilityZones"] != true {
				return []any{}
			}
			return []any{map[string]any{"zoneName": r + "a", "state": "available"}, map[string]any{"zoneName": r + "b", "state": "available"}}
		}
		return map[string]any{"regions": []any{
			map[string]any{"continentCode": "NA", "description": "This region is recommended to serve users in the eastern United States", "displayName": "Virginia", "name": "us-east-1", "availabilityZones": zones("us-east-1"), "relationalDatabaseAvailabilityZones": []any{}},
			map[string]any{"continentCode": "EU", "description": "This region is recommended to serve users in Europe", "displayName": "Frankfurt", "name": "eu-central-1", "availabilityZones": zones("eu-central-1"), "relationalDatabaseAvailabilityZones": []any{}},
		}}, nil
	case "GetBundles":
		bundles := stubBundles
		if req.Body["includeInactive"] == true {
			bundles = append(append([]any{}, stubBundles...), map[string]any{"bundleId": "medium_2_0", "name": "Medium", "price": 20.0, "cpuCount": 2, "ramSizeInGb": 4.0, "diskSizeInGb": 80,
				"transferPerMonthInGb": 4096, "isActive": false, "instanceType": "medium", "power": 2000, "supportedPlatforms": []string{"LINUX_UNIX"}, "supportedAppCategories": []string{}, "publicIpv4AddressCount": 1})
		}
		return s.page(req, "bundles", bundles), nil
	case "GetBlueprints":
		return s.page(req, "blueprints", stubBlueprints), nil
	case "GetInstances":
		var all []any
		if s.created != nil {
			// Two more, so that a page size of one makes three pages.
			all = []any{s.instance(), map[string]any{"name": "other-1", "state": map[string]any{"code": 16, "name": "running"}},
				map[string]any{"name": "other-2", "state": map[string]any{"code": 80, "name": "stopped"}}}
		}
		return s.page(req, "instances", all), nil
	case "GetInstance":
		if !hasInstance {
			return missing("Instance", req.Body["instanceName"])
		}
		return map[string]any{"instance": s.instance()}, nil
	case "CreateInstances":
		s.created = req.Body
		any4, any6 := []any{"0.0.0.0/0"}, []any{"::/0"}
		s.ports = []any{
			map[string]any{"fromPort": 22, "toPort": 22, "protocol": "tcp", "state": "open", "cidrs": any4, "ipv6Cidrs": any6, "cidrListAliases": []any{}},
			map[string]any{"fromPort": 80, "toPort": 80, "protocol": "tcp", "state": "open", "cidrs": any4, "ipv6Cidrs": any6, "cidrListAliases": []any{}}}
		return map[string]any{"operations": []any{op(req.Body["instanceNames"].([]any)[0])}}, nil
	case "DeleteInstance":
		if !hasInstance {
			return missing("Instance", req.Body["instanceName"])
		}
		s.created, s.attached = nil, false
		return map[string]any{"operations": []any{op(req.Body["instanceName"])}}, nil
	case "GetInstancePortStates":
		if !hasInstance {
			return missing("Instance", req.Body["instanceName"])
		}
		return map[string]any{"portStates": s.ports}, nil
	case "PutInstancePublicPorts":
		if !hasInstance {
			return missing("Instance", req.Body["instanceName"])
		}
		s.ports = nil
		for _, p := range req.Body["portInfos"].([]any) {
			state := map[string]any{"state": "open", "cidrListAliases": []any{}}
			for k, v := range p.(map[string]any) {
				state[k] = v
			}
			s.ports = append(s.ports, state)
		}
		return map[string]any{"operation": op(req.Body["instanceName"])}, nil
	case "AllocateStaticIp":
		s.staticIP = req.Body["staticIpName"].(string)
		return map[string]any{"operations": []any{op(s.staticIP)}}, nil
	case "AttachStaticIp":
		if s.staticIP != req.Body["staticIpName"] {
			return missing("StaticIp", req.Body["staticIpName"])
		}
		s.attached = true
		return map[string]any{"operations": []any{op(s.staticIP)}}, nil
	case "GetStaticIp", "ReleaseStaticIp":
		if s.staticIP == "" || s.staticIP != req.Body["staticIpName"] {
			return missing("StaticIp", req.Body["staticIpName"])
		}
		if req.Op == "ReleaseStaticIp" {
			s.staticIP, s.attached = "", false
			return map[string]any{"operations": []any{op(req.Body["staticIpName"])}}, nil
		}
		return map[string]any{"staticIp": s.staticIPView()}, nil
	case "GetStaticIps":
		var all []any
		if s.staticIP != "" {
			all = []any{s.staticIPView()}
		}
		return s.page(req, "staticIps", all), nil
	case "DetachStaticIp":
		if s.staticIP == "" || s.staticIP != req.Body["staticIpName"] {
			return missing("StaticIp", req.Body["staticIpName"])
		}
		s.attached = false
		return map[string]any{"operations": []any{op(s.staticIP)}}, nil
	case "StopInstance", "StartInstance":
		if !hasInstance {
			return missing("Instance", req.Body["instanceName"])
		}
		s.stopped = req.Op == "StopInstance"
		return map[string]any{"operations": []any{op(req.Body["instanceName"])}}, nil
	case "CreateInstanceSnapshot":
		if !hasInstance {
			return missing("Instance", req.Body["instanceName"])
		}
		name := req.Body["instanceSnapshotName"].(string)
		if s.snapshots == nil {
			s.snapshots = map[string]map[string]any{}
		}
		s.snapshots[name] = map[string]any{"name": name, "arn": "arn:aws:lightsail:eu-central-1:123456789012:InstanceSnapshot/01234567-89ab-cdef-0123-456789abcdef",
			"supportCode": "123456789012/ami-0123456789abcdef0", "createdAt": 1794000100.5, "resourceType": "InstanceSnapshot",
			"location": map[string]any{"availabilityZone": "all", "regionName": "eu-central-1"}, "tags": req.Body["tags"], "state": "pending",
			"fromAttachedDisks": []any{}, "fromInstanceName": req.Body["instanceName"], "fromInstanceArn": "arn:aws:lightsail:eu-central-1:123456789012:Instance/01234567-89ab-cdef-0123-456789abcdef",
			"fromBlueprintId": s.created["blueprintId"], "fromBundleId": s.created["bundleId"], "isFromAutoSnapshot": false, "sizeInGb": 80}
		return map[string]any{"operations": []any{op(name)}}, nil
	case "GetInstanceSnapshot", "DeleteInstanceSnapshot":
		snap := s.snapshots[fmt.Sprint(req.Body["instanceSnapshotName"])]
		if snap == nil {
			return missing("InstanceSnapshot", req.Body["instanceSnapshotName"])
		}
		if req.Op == "DeleteInstanceSnapshot" {
			delete(s.snapshots, snap["name"].(string))
			return map[string]any{"operations": []any{op(snap["name"])}}, nil
		}
		if s.SnapshotPending > 0 {
			s.SnapshotPending--
		} else {
			snap["state"] = "available"
		}
		return map[string]any{"instanceSnapshot": snap}, nil
	case "GetInstanceSnapshots":
		var all []any
		for _, snap := range s.snapshots {
			all = append(all, snap)
		}
		// Two more, so that a page size of one makes pages.
		all = append(all, map[string]any{"name": "other-1", "state": "available", "createdAt": 1794000900.0}, map[string]any{"name": "other-2", "state": "pending"})
		return s.page(req, "instanceSnapshots", all), nil
	case "CreateInstancesFromSnapshot":
		snap := s.snapshots[fmt.Sprint(req.Body["instanceSnapshotName"])]
		if snap == nil {
			return missing("InstanceSnapshot", req.Body["instanceSnapshotName"])
		}
		s.created = map[string]any{}
		for k, v := range req.Body {
			s.created[k] = v
		}
		s.created["blueprintId"] = snap["fromBlueprintId"]
		s.stopped, s.attached = false, false
		any4, any6 := []any{"0.0.0.0/0"}, []any{"::/0"}
		s.ports = []any{
			map[string]any{"fromPort": 22, "toPort": 22, "protocol": "tcp", "state": "open", "cidrs": any4, "ipv6Cidrs": any6, "cidrListAliases": []any{}},
			map[string]any{"fromPort": 80, "toPort": 80, "protocol": "tcp", "state": "open", "cidrs": any4, "ipv6Cidrs": any6, "cidrListAliases": []any{}}}
		return map[string]any{"operations": []any{op(req.Body["instanceNames"].([]any)[0])}}, nil
	case "TagResource":
		snap := s.snapshots[fmt.Sprint(req.Body["resourceName"])]
		if snap == nil {
			return missing("Resource", req.Body["resourceName"])
		}
		snap["tags"] = append(append([]any{}, snap["tags"].([]any)...), req.Body["tags"].([]any)...)
		return map[string]any{"operations": []any{op(req.Body["resourceName"])}}, nil
	}
	e := lightsailError("InvalidInputException", "the stub does not know "+req.Op)
	return nil, &e
}

func (s *stub) staticIPView() map[string]any {
	ip := map[string]any{"name": s.staticIP, "arn": "arn:aws:lightsail:eu-central-1:123456789012:StaticIp/01234567-89ab-cdef-0123-456789abcdef",
		"createdAt": 1794000001.0, "resourceType": "StaticIp", "ipAddress": stubStaticIP, "isAttached": s.attached,
		"location": map[string]any{"availabilityZone": "all", "regionName": "eu-central-1"}}
	if s.attached {
		ip["attachedTo"] = s.created["instanceNames"].([]any)[0]
	}
	return ip
}

// set changes the stub between calls, while no request is being answered.
func (s *stub) set(change func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	change()
}

// fail makes the stub answer an operation with an error from now on.
func (s *stub) fail(op string, e stubError) { s.set(func() { s.Fail[op] = e }) }

// Requests returns the requests for one operation, or all for "".
func (s *stub) Requests(op string) []stubRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []stubRequest
	for _, r := range s.requests {
		if r.Op == op || op == "" {
			out = append(out, r)
		}
	}
	return out
}

// realRunner runs the real CLI through Exec and fails the test when the
// CLI refuses a command's arguments (exit 252) or the host key reaches a
// command line.
type realRunner struct {
	t *testing.T
	Exec
	mu    sync.Mutex
	calls [][]string
}

func (r *realRunner) Run(ctx context.Context, args []string) ([]byte, []byte, int, error) {
	r.mu.Lock()
	r.calls = append(r.calls, args)
	r.mu.Unlock()
	for _, a := range args {
		if strings.Contains(a, "DO-NOT-LEAK") || strings.Contains(a, "OPENSSH PRIVATE KEY") {
			r.t.Errorf("the host key reached a command line: %q", a)
		}
	}
	stdout, stderr, exit, err := r.Exec.Run(ctx, args)
	if err != nil {
		r.t.Errorf("aws %s: %v", strings.Join(args, " "), err)
	}
	if exit == 252 {
		r.t.Errorf("the AWS CLI refused the arguments of: aws %s\n%s", strings.Join(args, " "), stderr)
	}
	return stdout, stderr, exit, err
}

// realCLI skips the test unless KEELBOX_TEST_AWS_CLI names an aws binary,
// then returns a runner for it and the stub it talks to. The CLI gets its
// own HOME and configuration files, never the user's, and made-up
// credentials through the environment.
func realCLI(t *testing.T) (*realRunner, *stub, string) {
	t.Helper()
	path := os.Getenv(realCLIVar)
	if path == "" {
		t.Skip("set " + realCLIVar + "=/path/to/aws to run the contract tests against the real AWS CLI")
	}
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); strings.HasPrefix(k, "AWS_") {
			t.Setenv(k, "") // restored after the test
			_ = os.Unsetenv(k)
		}
	}
	s := &stub{Fail: map[string]stubError{}}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	configFile := filepath.Join(dir, "config")
	// output = text: the provider's --output json must win over it.
	writeFile(t, configFile, "[default]\noutput = text\n")
	writeFile(t, filepath.Join(dir, "credentials"), "")
	t.Setenv("HOME", dir)
	t.Setenv("AWS_CONFIG_FILE", configFile)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "credentials"))
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_ENDPOINT_URL", srv.URL)
	t.Setenv("AWS_ACCESS_KEY_ID", "test-access-key")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test-secret-key")
	old := poll
	poll = time.Millisecond
	t.Cleanup(func() { poll = old })
	return &realRunner{t: t, Exec: Exec{Path: path}}, s, configFile
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The whole life of a box: every command Preflight, Create, Find, the
// firewall calls and Destroy issue is accepted by the CLI, arrives at the
// service as the request keelbox means, and its output is understood.
func TestRealCLILifecycle(t *testing.T) {
	run, s, _ := realCLI(t)
	s.set(func() { s.Pending = 2 })
	cl := &Client{Region: "eu-central-1", Run: run}
	ctx := context.Background()
	c := boxConfig(t, `, tags: {team: web, "cost:centre": "a=b/c@d+e", note: ""}`)

	id, err := cl.Preflight(ctx, c.Target.AWS.Bundle, c.Target.AWS.Blueprint)
	if err != nil || id.Account != "123456789012" || id.Arn != "arn:aws:iam::123456789012:user/sam" || id.UserID == "" {
		t.Fatalf("preflight: %+v, %v", id, err)
	}
	in, err := Create(ctx, cl, c, testHostKey, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	if in.Name != "acme" || in.PublicIPAddress != stubStaticIP || !in.IsStaticIP || in.State.Name != "running" ||
		in.Location.AvailabilityZone != "eu-central-1a" || in.BundleID != "medium_3_0" || !in.Managed("acme") {
		t.Errorf("created instance %+v", in)
	}

	// The CLI read the user data from the file and sent it whole; the
	// file is gone, and the key was in no argument (realRunner checks).
	create := s.Requests("CreateInstances")
	if len(create) != 1 {
		t.Fatalf("CreateInstances requests: %d", len(create))
	}
	keys, _ := c.Users.Admin.AllKeys()
	userData, _ := UserData("acme", c.Host.Hostname, testHostKey, keys)
	if got := create[0].Body["userData"]; got != userData || !strings.Contains(userData, privateKey) {
		t.Errorf("user data in the request:\n%v\nwant:\n%s", got, userData)
	}
	for _, call := range run.calls {
		for _, a := range call {
			if path, ok := strings.CutPrefix(a, "file://"); ok {
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("the user data file was left behind: %v", err)
				}
			}
		}
	}
	// The tag shorthand, with every character a tag may hold and an
	// empty value.
	tag := func(k, v string) any { return map[string]any{"key": k, "value": v} }
	want := map[string]any{"instanceNames": []any{"acme"}, "availabilityZone": "eu-central-1a", "blueprintId": "ubuntu_24_04",
		"bundleId": "medium_3_0", "userData": userData,
		"tags": []any{tag("cost:centre", "a=b/c@d+e"), tag("keelbox", "true"), tag("keelbox-box", "acme"), tag("note", ""), tag("team", "web")}}
	if !reflect.DeepEqual(create[0].Body, want) {
		t.Errorf("CreateInstances request:\n%v\nwant:\n%v", create[0].Body, want)
	}

	// The port shorthand: numbers as numbers, address ranges as lists.
	rule := func(port int, protocol string, v4, v6 []any) any {
		return map[string]any{"fromPort": float64(port), "toPort": float64(port), "protocol": protocol, "cidrs": v4, "ipv6Cidrs": v6}
	}
	any4, any6 := []any{"0.0.0.0/0"}, []any{"::/0"}
	wantPorts := func(what string, rules ...any) {
		t.Helper()
		put := s.Requests("PutInstancePublicPorts")
		want := map[string]any{"instanceName": "acme", "portInfos": rules}
		if got := put[len(put)-1].Body; !reflect.DeepEqual(got, want) {
			t.Errorf("%s: PutInstancePublicPorts request:\n%v\nwant:\n%v", what, got, want)
		}
	}
	wantPorts("create", rule(TailscalePort, "udp", any4, any6), rule(22, "tcp", any4, any6))
	closed := false
	changes, _, err := EnsurePorts(ctx, cl, "acme", &closed)
	if err != nil || len(changes) != 1 {
		t.Fatalf("close public SSH: %v, %v", changes, err)
	}
	wantPorts("close public SSH", rule(TailscalePort, "udp", any4, any6))
	if ports, err := cl.Ports(ctx, "acme"); err != nil || !SamePorts(ports, PortRules(false)) || HasPublicSSH(ports) {
		t.Errorf("ports after closing SSH: %+v, %v", ports, err)
	}
	if changes, _, err := EnsurePorts(ctx, cl, "acme", &closed); err != nil || len(changes) != 0 {
		t.Errorf("a second EnsurePorts changed something: %v, %v", changes, err)
	}
	err = cl.PutPorts(ctx, "acme", []Port{
		{FromPort: 8000, ToPort: 8100, Protocol: "tcp", Cidrs: []string{"192.0.2.0/24", "198.51.100.7/32"}, Ipv6Cidrs: []string{"2001:db8::/32", "2001:db8:1::/48"}},
		{FromPort: 53, ToPort: 53, Protocol: "udp", Cidrs: []string{"192.0.2.0/24"}}})
	if err != nil {
		t.Fatal(err)
	}
	wantPorts("several ranges",
		map[string]any{"fromPort": 8000.0, "toPort": 8100.0, "protocol": "tcp", "cidrs": []any{"192.0.2.0/24", "198.51.100.7/32"}, "ipv6Cidrs": []any{"2001:db8::/32", "2001:db8:1::/48"}},
		map[string]any{"fromPort": 53.0, "toPort": 53.0, "protocol": "udp", "cidrs": []any{"192.0.2.0/24"}})

	found, err := Find(ctx, cl, "acme")
	if err != nil || found.Labels()["cost:centre"] != "a=b/c@d+e" || found.Arn == "" {
		t.Fatalf("find: %+v, %v", found, err)
	}
	if all, err := cl.Instances(ctx); err != nil || len(all) != 3 || all[0].Name != "acme" {
		t.Errorf("instances: %+v, %v", all, err)
	}
	if ips, err := cl.StaticIPs(ctx); err != nil || len(ips) != 1 || ips[0].Name != "keelbox-acme" || ips[0].AttachedTo != "acme" || !ips[0].IsAttached {
		t.Errorf("static IPs: %+v, %v", ips, err)
	}

	done, err := Destroy(ctx, cl, found, "acme")
	if err != nil || len(done) != 2 || !strings.Contains(done[1], stubStaticIP) {
		t.Fatalf("destroy: %q, %v", done, err)
	}
	if del := s.Requests("DeleteInstance"); len(del) != 1 || !reflect.DeepEqual(del[0].Body, map[string]any{"instanceName": "acme", "forceDeleteAddOns": true}) {
		t.Errorf("DeleteInstance requests: %+v", del)
	}
	if ip, err := ReleaseStaticIP(ctx, cl, "acme"); ip != nil || err != nil {
		t.Errorf("release with nothing left: %+v, %v", ip, err)
	}
	if regions := s.Requests("GetRegions"); len(regions) == 0 || regions[0].Body["includeAvailabilityZones"] != true {
		t.Errorf("GetRegions requests: %+v", regions)
	}
	// --region, placed after the operation's arguments, is the region
	// every request was signed for.
	for _, r := range s.Requests("") {
		if r.Region != "eu-central-1" {
			t.Errorf("%s was signed for region %q", r.Op, r.Region)
		}
	}
}

// The CLI follows nextPageToken itself and prints the pages as one list.
func TestRealCLIPagination(t *testing.T) {
	run, s, _ := realCLI(t)
	s.set(func() {
		s.PageSize = 1
		s.created = map[string]any{"instanceNames": []any{"acme"}, "availabilityZone": "eu-central-1a"}
	})
	cl := &Client{Region: "eu-central-1", Run: run}
	ctx := context.Background()
	bundles, err := cl.Bundles(ctx)
	if err != nil || len(bundles) != len(stubBundles) || bundles[2].BundleID != "medium_ipv6_3_0" {
		t.Errorf("bundles: %+v, %v", bundles, err)
	}
	if offers := Offers(bundles); len(offers) != 2 || offers[0].Bundle.Price != 5 || offers[1].Bundle.RAMSizeInGb != 4 {
		t.Errorf("offers: %+v", offers)
	}
	blueprints, err := cl.Blueprints(ctx)
	if err != nil || len(blueprints) != len(stubBlueprints) || blueprints[2].Type != "app" {
		t.Errorf("blueprints: %+v, %v", blueprints, err)
	}
	instances, err := cl.Instances(ctx)
	if err != nil || len(instances) != 3 || instances[2].Name != "other-2" {
		t.Errorf("instances: %+v, %v", instances, err)
	}
	for _, op := range []string{"GetBundles", "GetBlueprints", "GetInstances"} {
		reqs := s.Requests(op)
		if len(reqs) != 3 || reqs[0].Body["pageToken"] != nil || reqs[1].Body["pageToken"] != "1" || reqs[2].Body["pageToken"] != "2" {
			t.Errorf("%s: the CLI did not follow the page tokens: %+v", op, reqs)
		}
	}
}

// What the CLI prints and exits with when a call fails, and what keelbox
// makes of it.
func TestRealCLIErrors(t *testing.T) {
	run, s, _ := realCLI(t)
	cl := &Client{Region: "eu-central-1", Run: run}
	ctx := context.Background()
	failure := func(err error) *Error {
		t.Helper()
		var e *Error
		if !errors.As(err, &e) {
			t.Fatalf("not an *Error: %v", err)
		}
		return e
	}

	// A missing resource is nil, not an error.
	if in, err := cl.Instance(ctx, "nope"); in != nil || err != nil {
		t.Errorf("missing instance: %+v, %v", in, err)
	}
	if ip, err := cl.StaticIP(ctx, "nope"); ip != nil || err != nil {
		t.Errorf("missing static IP: %+v, %v", ip, err)
	}
	err := cl.lightsail(ctx, nil, "get-instance", "--instance-name", "nope")
	if e := failure(err); !IsCode(err, "NotFoundException") || e.Exit != 254 || e.Message != "The Instance does not exist: nope" || e.Command != "lightsail get-instance" {
		t.Errorf("not found: %+v", e)
	}

	denied := "User: arn:aws:iam::123456789012:user/sam is not authorized to perform: lightsail:GetInstance on resource: arn:aws:lightsail:eu-central-1:123456789012:*"
	s.fail("GetInstance", lightsailError("AccessDeniedException", denied))
	_, err = cl.Instance(ctx, "acme")
	if e := failure(err); !IsCode(err, "AccessDeniedException") || e.Exit != 254 || e.Message != denied {
		t.Errorf("access denied: %+v", e)
	}
	if _, err := Find(ctx, cl, "acme"); !IsCode(err, "AccessDeniedException") {
		t.Errorf("find, access denied: %v", err)
	}

	// Credentials AWS refuses: not signed in, with AWS's reason.
	for code, message := range map[string]string{
		"InvalidClientTokenId": "The security token included in the request is invalid.",
		"ExpiredToken":         "The security token included in the request is expired",
	} {
		s.fail("GetCallerIdentity", stsError(403, code, message))
		_, err = cl.Identity(ctx)
		if !errors.Is(err, ErrNotSignedIn) || !strings.Contains(err.Error(), message) || !strings.Contains(err.Error(), "aws login;") {
			t.Errorf("%s: %v", code, err)
		}
		err = cl.call(ctx, cl.Region, nil, "sts", "get-caller-identity")
		if e := failure(err); e.Code != code || e.Exit != 254 || e.Message != message {
			t.Errorf("%s: %+v", code, e)
		}
	}
	s.set(func() { delete(s.Fail, "GetCallerIdentity") })

	// A profile the CLI does not have: exit 255, before any request.
	sent := len(s.Requests(""))
	wrong := &Client{Region: "eu-central-1", Profile: "wrok", Run: run}
	if _, err = wrong.Identity(ctx); !errors.Is(err, ErrNotSignedIn) || !strings.Contains(err.Error(), "no profile named wrok") {
		t.Errorf("unknown profile: %v", err)
	}
	err = wrong.lightsail(ctx, nil, "get-instances")
	if e := failure(err); e.Code != "" || e.Exit != 255 || e.Message != "The config profile (wrok) could not be found" {
		t.Errorf("unknown profile: %+v", e)
	}

	// No credentials at all: exit 253.
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	if _, err = cl.Identity(ctx); !errors.Is(err, ErrNotSignedIn) || !strings.Contains(err.Error(), "with the default profile (Unable to locate credentials.") {
		t.Errorf("no credentials: %v", err)
	}
	err = cl.lightsail(ctx, nil, "get-instances")
	if e := failure(err); e.Code != "" || e.Exit != 253 || !strings.HasPrefix(e.Message, "Unable to locate credentials.") {
		t.Errorf("no credentials: %+v", e)
	}
	if n := len(s.Requests("")); n != sent {
		t.Errorf("%d requests were sent without a profile or credentials", n-sent)
	}

	// A call AWS throttles is retried by the CLI before it gives up.
	t.Setenv("AWS_ACCESS_KEY_ID", "test-access-key")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test-secret-key")
	s.fail("GetBundles", lightsailError("ThrottlingException", "Rate exceeded"))
	_, err = cl.Bundles(ctx)
	if e := failure(err); e.Code != "ThrottlingException" || e.Exit != 254 || e.Message != "Rate exceeded" || len(s.Requests("GetBundles")) < 2 {
		t.Errorf("throttled: %+v after %d requests", e, len(s.Requests("GetBundles")))
	}

	// Nothing answers at the endpoint: exit 255.
	t.Setenv("AWS_ENDPOINT_URL", "http://127.0.0.1:1")
	err = cl.lightsail(ctx, nil, "get-regions")
	if e := failure(err); e.Code != "" || e.Exit != 255 || !strings.HasPrefix(e.Message, "Could not connect to the endpoint URL") {
		t.Errorf("no endpoint: %+v", e)
	}
}

// rawCLI runs the CLI without Exec, so that a test chooses the error
// format, and returns its stderr and exit code.
func rawCLI(t *testing.T, path string, env []string, args ...string) ([]byte, int) {
	t.Helper()
	cmd := exec.Command(path, args...)
	cmd.Env = append(os.Environ(), env...)
	var errOut strings.Builder
	cmd.Stderr = &errOut
	var ee *exec.ExitError
	if err := cmd.Run(); err != nil && !errors.As(err, &ee) {
		t.Fatal(err)
	}
	return []byte(errOut.String()), cmd.ProcessState.ExitCode()
}

// Exec asks for the CLI's "legacy" error format. parseError reads that
// and the CLI's default ("enhanced", which adds the error's other fields
// below the same first line), and Exec's choice wins over the user's own
// cli_error_format.
func TestRealCLIErrorFormat(t *testing.T) {
	run, s, configFile := realCLI(t)
	message := "User: arn:aws:iam::123456789012:user/sam is not authorized to perform: lightsail:GetInstance"
	b, _ := json.Marshal(map[string]string{"__type": "AccessDeniedException", "code": "AccessDenied", "message": message,
		"docs": "https://docs.aws.amazon.com/lightsail/", "tip": "Check the policy."})
	s.fail("GetInstance", stubError{Status: 400, Body: string(b)})
	s.fail("GetCallerIdentity", stsError(403, "ExpiredToken", "The security token included in the request is expired"))
	global := []string{"--output", "json", "--region", "eu-central-1"}
	cases := []struct {
		name          string
		args          []string
		exit          int
		code, message string
	}{
		{"service error with more fields", []string{"lightsail", "get-instance", "--instance-name", "acme"}, 254, "AccessDeniedException", message},
		{"service error", []string{"lightsail", "get-static-ip", "--static-ip-name", "keelbox-acme"}, 254, "NotFoundException", "The StaticIp does not exist: keelbox-acme"},
		{"STS error", []string{"sts", "get-caller-identity"}, 254, "ExpiredToken", "The security token included in the request is expired"},
		{"unknown profile", []string{"sts", "get-caller-identity", "--profile", "wrok"}, 255, "", "The config profile (wrok) could not be found"},
		{"bad arguments", []string{"lightsail", "get-instance"}, 252, "", "the following arguments are required: --instance-name"},
	}
	for _, format := range []string{"legacy", "enhanced", ""} {
		var env []string
		if format != "" {
			env = []string{"AWS_CLI_ERROR_FORMAT=" + format}
		}
		for _, c := range cases {
			stderr, exit := rawCLI(t, run.Path, env, append(c.args, global...)...)
			e := parseError("x", stderr, exit)
			code := c.code
			if c.exit == 252 && format != "legacy" {
				code = "ParamValidation" // the default format gives the CLI's own errors a code
			}
			if e.Exit != c.exit || e.Message != c.message || e.Code != code {
				t.Errorf("%s, format %q: %+v\nstderr: %s", c.name, format, e, stderr)
			}
		}
	}
	// Legacy has only the one line; the default lists the other fields.
	legacy, _ := rawCLI(t, run.Path, []string{"AWS_CLI_ERROR_FORMAT=legacy"}, append(cases[0].args, global...)...)
	enhanced, _ := rawCLI(t, run.Path, nil, append(cases[0].args, global...)...)
	if want := "\naws: [ERROR]: An error occurred (AccessDeniedException) when calling the GetInstance operation: " + message + "\n"; string(legacy) != want {
		t.Errorf("legacy format:\n%q\nwant:\n%q", legacy, want)
	}
	if !strings.Contains(string(enhanced), "Additional error details:") || !strings.Contains(string(enhanced), "tip: Check the policy.") {
		t.Errorf("default format: %q", enhanced)
	}
	// The user's own setting does not reach keelbox.
	writeFile(t, configFile, "[default]\ncli_error_format = json\n")
	if stderr, _ := rawCLI(t, run.Path, nil, append(cases[0].args, global...)...); !strings.HasPrefix(string(stderr), "{") {
		t.Fatalf("cli_error_format = json printed: %q", stderr)
	}
	if _, stderr, _, _ := run.Run(context.Background(), append(cases[0].args, global...)); string(stderr) != string(legacy) {
		t.Errorf("Exec with cli_error_format = json:\n%q\nwant:\n%q", stderr, legacy)
	}
}

// ConfiguredRegion is the region the CLI itself would use: the one it
// signs a request for when given no --region.
func TestRealCLIConfiguredRegion(t *testing.T) {
	run, s, configFile := realCLI(t)
	ctx := context.Background()
	config := "[default]\nregion = eu-west-1\n[profile work]\nregion = us-west-2\n[profile bare]\noutput = json\n"
	// With --profile the CLI takes no credentials from the environment.
	credentials := "aws_access_key_id = test-access-key\naws_secret_access_key = test-secret-key\n"
	writeFile(t, os.Getenv("AWS_SHARED_CREDENTIALS_FILE"), "[work]\n"+credentials+"[bare]\n"+credentials)
	cases := []struct {
		name, config, profile string
		env                   map[string]string
		want                  string
	}{
		{"nothing configured", "", "", nil, ""},
		{"default profile", config, "", nil, "eu-west-1"},
		{"named profile", config, "work", nil, "us-west-2"},
		{"profile without a region", config, "bare", nil, ""},
		{"unknown profile", config, "wrok", nil, ""},
		{"AWS_PROFILE", config, "", map[string]string{"AWS_PROFILE": "work"}, "us-west-2"},
		{"AWS_DEFAULT_REGION over the file", config, "", map[string]string{"AWS_DEFAULT_REGION": "ap-south-1"}, "ap-south-1"},
		{"AWS_REGION over AWS_DEFAULT_REGION", config, "work", map[string]string{"AWS_REGION": "sa-east-1", "AWS_DEFAULT_REGION": "ap-south-1"}, "sa-east-1"},
		{"AWS_REGION alone", "", "", map[string]string{"AWS_REGION": "sa-east-1"}, "sa-east-1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			writeFile(t, configFile, c.config)
			for k, v := range c.env {
				t.Setenv(k, v)
			}
			cl := &Client{Profile: c.profile, Run: run}
			if got := cl.ConfiguredRegion(ctx); got != c.want {
				t.Errorf("ConfiguredRegion = %q, want %q", got, c.want)
			}
			// What the CLI does with no --region.
			args := []string{"sts", "get-caller-identity"}
			if c.profile != "" {
				args = append(args, "--profile", c.profile)
			}
			before := len(s.Requests("GetCallerIdentity"))
			_, stderr, exit, _ := run.Exec.Run(ctx, args)
			reqs := s.Requests("GetCallerIdentity")
			switch {
			case c.name == "unknown profile":
				if exit != 255 {
					t.Errorf("exit %d: %s", exit, stderr)
				}
			case c.want == "":
				if exit != 253 || !strings.Contains(string(stderr), "You must specify a region") {
					t.Errorf("exit %d: %s", exit, stderr)
				}
			case exit != 0 || len(reqs) != before+1 || reqs[before].Region != c.want:
				t.Errorf("the CLI used another region: exit %d, %+v\n%s", exit, reqs[before:], stderr)
			}
		})
	}
}

// Resize, park and resume: every command they issue is accepted by the
// CLI, arrives at the service as the request keelbox means, and its
// output is understood, the timestamps included.
func TestRealCLIPark(t *testing.T) {
	run, s, _ := realCLI(t)
	cl := &Client{Region: "eu-central-1", Run: run}
	ctx := context.Background()
	c := boxConfig(t, `, tags: {team: web, "cost:centre": "a=b/c@d+e"}`)
	in, err := Create(ctx, cl, c, testHostKey, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	if in.DiskGB() != 80 {
		t.Errorf("disk %d GB", in.DiskGB())
	}
	last := func(op string) map[string]any {
		t.Helper()
		reqs := s.Requests(op)
		if len(reqs) == 0 {
			t.Fatalf("no %s request", op)
		}
		return reqs[len(reqs)-1].Body
	}
	tag := func(k, v string) any { return map[string]any{"key": k, "value": v} }

	if err := Stop(ctx, cl, "acme", in, t.Logf); err != nil {
		t.Fatal(err)
	}
	if got := last("StopInstance"); !reflect.DeepEqual(got, map[string]any{"instanceName": "acme"}) {
		t.Errorf("StopInstance request: %v", got)
	}
	if cur, err := cl.Instance(ctx, "acme"); err != nil || cur.State.Name != "stopped" {
		t.Fatalf("after stop: %+v, %v", cur, err)
	}
	// The forced stop, as Stop sends it for an instance that hangs.
	if err := cl.lightsail(ctx, nil, "stop-instance", "--instance-name", "acme", "--force"); err != nil {
		t.Fatal(err)
	}
	if got := last("StopInstance"); !reflect.DeepEqual(got, map[string]any{"instanceName": "acme", "force": true}) {
		t.Errorf("forced StopInstance request: %v", got)
	}

	s.set(func() { s.SnapshotPending = 2 })
	snap, err := ParkSnapshot(ctx, cl, c, in)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"instanceSnapshotName": snap.Name, "instanceName": "acme", "tags": []any{tag("cost:centre", "a=b/c@d+e"),
		tag("keelbox", "true"), tag("keelbox-box", "acme"), tag("keelbox-parked", "true"), tag("keelbox-public-ssh", "open"), tag("team", "web")}}
	if got := last("CreateInstanceSnapshot"); !reflect.DeepEqual(got, want) {
		t.Errorf("CreateInstanceSnapshot request:\n%v\nwant:\n%v", got, want)
	}
	if !snap.Parked("acme") || snap.State != "available" || snap.SizeInGb != 80 || snap.FromInstanceName != "acme" || snap.FromBundleID != "medium_3_0" ||
		!snap.CreatedAt.Equal(time.Unix(1794000100, 500000000)) || len(s.Requests("GetInstanceSnapshot")) != 3 {
		t.Errorf("snapshot %+v after %d reads", snap, len(s.Requests("GetInstanceSnapshot")))
	}
	if _, err := Destroy(ctx, cl, in, "acme"); err != nil {
		t.Fatal(err)
	}
	// The list is read through every page.
	s.set(func() { s.PageSize = 1 })
	parked, err := Parked(ctx, cl, "acme")
	if err != nil || parked == nil || parked.Name != snap.Name || !parked.CreatedAt.Equal(snap.CreatedAt.Time) {
		t.Fatalf("parked: %+v, %v", parked, err)
	}
	if all, err := cl.Snapshots(ctx); err != nil || len(all) != 3 {
		t.Errorf("snapshots: %+v, %v", all, err)
	}
	s.set(func() { s.PageSize = 0 })

	s.set(func() { s.Pending = 2 })
	c.Target.AWS.AvailabilityZone = "eu-central-1b"
	got, err := Restore(ctx, cl, c, parked, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "acme" || got.PublicIPAddress != stubStaticIP || got.State.Name != "running" || got.Location.AvailabilityZone != "eu-central-1b" {
		t.Errorf("restored instance %+v", got)
	}
	// The launch script reaches the service as it is, with its newlines.
	want = map[string]any{"instanceNames": []any{"acme"}, "availabilityZone": "eu-central-1b", "instanceSnapshotName": snap.Name, "bundleId": "medium_3_0",
		"userData": LaunchScript("acme"), "tags": []any{tag("cost:centre", "a=b/c@d+e"), tag("keelbox", "true"), tag("keelbox-box", "acme"), tag("team", "web")}}
	if got := last("CreateInstancesFromSnapshot"); !reflect.DeepEqual(got, want) {
		t.Errorf("CreateInstancesFromSnapshot request:\n%v\nwant:\n%v", got, want)
	}
	if ports, err := cl.Ports(ctx, "acme"); err != nil || !SamePorts(ports, PortRules(true)) {
		t.Errorf("ports after restore: %+v, %v", ports, err)
	}
	if err := DeleteSnapshot(ctx, cl, "acme", snap.Name); err != nil {
		t.Fatal(err)
	}
	if got := last("DeleteInstanceSnapshot"); !reflect.DeepEqual(got, map[string]any{"instanceSnapshotName": snap.Name}) {
		t.Errorf("DeleteInstanceSnapshot request: %v", got)
	}
	if p, err := Parked(ctx, cl, "acme"); p != nil || err != nil {
		t.Errorf("parked after the delete: %+v, %v", p, err)
	}

	// A resize: the stub has one bundle bigger than none, so the change is
	// refused by keelbox itself after the bundles were read with the
	// inactive ones; then the calls a resize adds, one by one.
	if err := Stop(ctx, cl, "acme", got, t.Logf); err != nil {
		t.Fatal(err)
	}
	if _, err := Resize(ctx, cl, c, got, "nano_3_0", t.Logf); err == nil || !strings.Contains(err.Error(), "cannot change to bundle nano_3_0") {
		t.Errorf("resize to a smaller bundle: %v", err)
	}
	if got := last("GetBundles"); got["includeInactive"] != true {
		t.Errorf("GetBundles request: %v", got)
	}
	if all, err := cl.AllBundles(ctx); err != nil || len(all) != 4 || all[3].BundleID != "medium_2_0" || all[3].IsActive {
		t.Errorf("all bundles: %+v, %v", all, err)
	}
	labels := map[string]string{"keelbox": "true", "keelbox-box": "acme", TagResize: "true"}
	temp, err := cl.takeSnapshot(ctx, "acme", got, "keelbox-acme-resize-20261114T101500Z", labels)
	if err != nil {
		t.Fatal(err)
	}
	if err := cl.lightsail(ctx, nil, "tag-resource", "--resource-name", temp.Name, "--tags", "key=keelbox-parked,value=true"); err != nil {
		t.Fatal(err)
	}
	if got := last("TagResource"); !reflect.DeepEqual(got, map[string]any{"resourceName": temp.Name, "tags": []any{tag("keelbox-parked", "true")}}) {
		t.Errorf("TagResource request: %v", got)
	}
	if p, err := Parked(ctx, cl, "acme"); err != nil || p == nil || p.Name != temp.Name || p.Labels()[TagResize] != "true" {
		t.Errorf("the snapshot of a resize, tagged as parked: %+v, %v", p, err)
	}
	if err := cl.lightsail(ctx, nil, "detach-static-ip", "--static-ip-name", "keelbox-acme"); err != nil {
		t.Fatal(err)
	}
	if got := last("DetachStaticIp"); !reflect.DeepEqual(got, map[string]any{"staticIpName": "keelbox-acme"}) {
		t.Errorf("DetachStaticIp request: %v", got)
	}
	// Start attaches the address again.
	started, err := Start(ctx, cl, "acme", got)
	if err != nil || started.State.Name != "running" || started.PublicIPAddress != stubStaticIP {
		t.Errorf("start: %+v, %v", started, err)
	}
	if got := last("StartInstance"); !reflect.DeepEqual(got, map[string]any{"instanceName": "acme"}) {
		t.Errorf("StartInstance request: %v", got)
	}
	if done, err := SweepSnapshots(ctx, cl, "acme"); err != nil || len(done) != 1 || done[0] != temp.Name {
		t.Errorf("sweep: %v, %v", done, err)
	}
}
