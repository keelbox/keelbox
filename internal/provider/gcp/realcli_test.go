package gcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/provider"
)

// The contract with the real gcloud (docs/contributing/testing.md): every
// command keelbox issues goes through the gcloud binary named by
// KEELBOX_TEST_GCLOUD, pointed at a stub of the Compute Engine API on
// 127.0.0.1. gcloud checks commands and flags, builds the requests and
// prints the answers itself, so this checks what the fake in gcptest only
// imitates. It needs no account.

// apiStub is a small in-memory Compute Engine API for one project.
type apiStub struct {
	mu      sync.Mutex
	t       *testing.T
	url     string // the server's own, for the links the API returns
	token   string
	project string
	next    int64
	// instances by "zone/name", firewalls by name, addresses by
	// "region/name", snapshots by name.
	instances map[string]map[string]any
	firewalls map[string]map[string]any
	addresses map[string]map[string]any
	snapshots map[string]map[string]any
	// ops are the operations answered, by name: gcloud asks for some again.
	ops      map[string]map[string]any
	networks []string
	// bodies are the JSON bodies received, by "METHOD /path" below the project.
	bodies map[string][]map[string]any
	// pageSize, when set, makes list answers that long at most, with the
	// token gcloud follows.
	pageSize int
	// deny answers 403 for a "METHOD /path"; apiOff answers as a project
	// whose Compute Engine API is disabled.
	deny   map[string]bool
	apiOff bool
	// staging is how many reads show a new VM as STAGING.
	staging int
	// uploading is how many reads show a new snapshot as CREATING and then
	// UPLOADING before it is READY; snapshotFails makes it FAILED instead.
	uploading     int
	snapshotFails bool
}

const stubZone, stubRegion = "europe-west3-a", "europe-west3"

func (s *apiStub) link(path string) string {
	return s.url + "/compute/v1/projects/" + s.project + "/" + path
}

func (s *apiStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	answer := func(status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	fail := func(status int, reason, msg string) {
		answer(status, map[string]any{"error": map[string]any{"code": status, "message": msg,
			"errors": []any{map[string]any{"message": msg, "domain": "global", "reason": reason}}}})
	}
	if r.Header.Get("Authorization") != "Bearer "+s.token {
		fail(401, "authError", "Request had invalid authentication credentials. Expected OAuth 2 access token, login cookie or other valid authentication credential.")
		return
	}
	prefix := "/compute/v1/projects/" + s.project
	if !strings.HasPrefix(r.URL.Path, prefix) {
		fail(404, "notFound", "The resource '"+strings.TrimPrefix(r.URL.Path, "/compute/v1/")+"' was not found")
		return
	}
	path := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, prefix), "/")
	key := r.Method + " /" + path
	if s.apiOff {
		fail(403, "accessNotConfigured", "Compute Engine API has not been used in project "+s.project+" before or it is disabled. Enable it by visiting "+
			"https://console.developers.google.com/apis/api/compute.googleapis.com/overview?project="+s.project+" then retry.")
		return
	}
	if s.deny[key] {
		fail(403, "forbidden", "Required 'compute."+strings.ReplaceAll(strings.TrimPrefix(path, "aggregated/"), "/", ".")+".list' permission for 'projects/"+s.project+"'")
		return
	}
	var body map[string]any
	if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			s.t.Errorf("%s: body is not JSON: %s", key, raw)
		}
		s.bodies[key] = append(s.bodies[key], body)
	}
	missing := func() { fail(404, "notFound", "The resource 'projects/"+s.project+"/"+path+"' was not found") }
	// done is the finished operation every change answers with.
	done := func(scope, target string) {
		s.next++
		name := fmt.Sprintf("operation-%d", s.next)
		op := map[string]any{"kind": "compute#operation", "id": fmt.Sprint(s.next), "name": name, "status": "DONE", "progress": 100,
			"operationType": strings.ToLower(r.Method), "targetLink": s.link(target), "selfLink": s.link(scope + "operations/" + name)}
		switch {
		case strings.HasPrefix(scope, "zones/"):
			op["zone"] = s.link(strings.TrimSuffix(scope, "/"))
		case strings.HasPrefix(scope, "regions/"):
			op["region"] = s.link(strings.TrimSuffix(scope, "/"))
		}
		s.ops[name] = op
		answer(200, op)
	}
	// page cuts a sorted list into the pages gcloud follows.
	page := func(items []map[string]any) ([]map[string]any, string) {
		n := s.pageSize
		if n == 0 || len(items) <= n {
			return items, ""
		}
		start := 0
		_, _ = fmt.Sscan(r.URL.Query().Get("pageToken"), &start)
		end := min(start+n, len(items))
		next := ""
		if end < len(items) {
			next = fmt.Sprint(end)
		}
		return items[start:end], next
	}
	list := func(kind string, items []map[string]any) {
		items, next := page(items)
		if items == nil {
			items = []map[string]any{}
		}
		out := map[string]any{"kind": kind, "items": items}
		if next != "" {
			out["nextPageToken"] = next
		}
		answer(200, out)
	}
	// aggregated answers a list grouped by zone or region, as the API does.
	aggregated := func(kind, field string, byScope map[string][]map[string]any) {
		var flat []map[string]any
		scopes := make([]string, 0, len(byScope))
		for sc := range byScope {
			scopes = append(scopes, sc)
		}
		sort.Strings(scopes)
		for _, sc := range scopes {
			for _, it := range byScope[sc] {
				it["_scope"] = sc
				flat = append(flat, it)
			}
		}
		flat, next := page(flat)
		items := map[string]any{"zones/us-central1-a": map[string]any{"warning": map[string]any{"code": "NO_RESULTS_ON_PAGE", "message": "There are no results for scope 'zones/us-central1-a' on this page."}}}
		for _, it := range flat {
			sc := it["_scope"].(string)
			delete(it, "_scope")
			group, _ := items[sc].(map[string]any)
			if group == nil {
				group = map[string]any{}
				items[sc] = group
			}
			l, _ := group[field].([]map[string]any)
			group[field] = append(l, it)
		}
		out := map[string]any{"kind": kind, "items": items}
		if next != "" {
			out["nextPageToken"] = next
		}
		answer(200, out)
	}
	sortedKeys := func(m map[string]map[string]any) []string {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return keys
	}
	// inUse marks an address with the VM that has it.
	inUse := func(a map[string]any) map[string]any {
		a["status"] = "RESERVED"
		delete(a, "users")
		for k, in := range s.instances {
			if in["_ip"] == a["address"] {
				a["status"], a["users"] = "IN_USE", []string{s.link("zones/" + strings.Replace(k, "/", "/instances/", 1))}
			}
		}
		return a
	}
	// shown is a VM as a read returns it: STAGING without its address for
	// the first reads, then RUNNING.
	shown := func(in map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range in {
			if !strings.HasPrefix(k, "_") {
				out[k] = v
			}
		}
		reads, _ := in["_reads"].(int)
		in["_reads"] = reads + 1
		access := map[string]any{"name": "external-nat", "type": "ONE_TO_ONE_NAT", "kind": "compute#accessConfig"}
		out["status"] = "STAGING"
		if reads >= s.staging {
			out["status"], access["natIP"] = "RUNNING", in["_ip"]
		}
		if st, _ := in["_status"].(string); st != "" {
			// A stopped VM keeps its reserved address.
			out["status"] = st
		}
		// The disks as the API lists them: each with the URL of the disk
		// made for it, which the create request does not have.
		name, _ := in["name"].(string)
		zone := in["zone"].(string)
		var disks []any
		list, _ := in["disks"].([]any)
		for i, d := range list {
			src, _ := d.(map[string]any)
			disk := map[string]any{"kind": "compute#attachedDisk", "index": i, "boot": src["boot"], "autoDelete": src["autoDelete"], "mode": "READ_WRITE",
				"deviceName": "persistent-disk-" + fmt.Sprint(i), "diskSizeGb": "50", "architecture": "X86_64", "source": zone + "/disks/" + name}
			if i > 0 {
				disk["source"] = zone + "/disks/" + name + "-" + fmt.Sprint(i)
			}
			disks = append(disks, disk)
		}
		out["disks"] = disks
		out["networkInterfaces"] = []any{map[string]any{"name": "nic0", "network": s.link("global/networks/default"), "networkIP": "10.156.0.2", "accessConfigs": []any{access}}}
		return out
	}
	// snapshot is a snapshot as a read returns it: CREATING, then
	// UPLOADING, then READY with what it stores, or FAILED.
	snapshot := func(snap map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range snap {
			if !strings.HasPrefix(k, "_") {
				out[k] = v
			}
		}
		reads, _ := snap["_reads"].(int)
		snap["_reads"] = reads + 1
		switch {
		case reads < s.uploading/2:
			out["status"] = "CREATING"
		case reads < s.uploading:
			out["status"] = "UPLOADING"
		case s.snapshotFails:
			out["status"] = "FAILED"
		default:
			out["status"], out["storageBytes"], out["storageBytesStatus"] = "READY", "1401125952", "UP_TO_DATE"
		}
		return out
	}
	parts := strings.Split(path, "/")
	switch {
	case path == "" && r.Method == http.MethodGet:
		answer(200, map[string]any{"kind": "compute#project", "name": s.project, "id": "1234567890", "selfLink": s.url + prefix})
	case key == "GET /zones":
		list("compute#zoneList", []map[string]any{
			{"name": stubZone, "status": "UP", "region": s.link("regions/" + stubRegion), "selfLink": s.link("zones/" + stubZone)},
			{"name": "europe-west3-b", "status": "UP", "region": s.link("regions/" + stubRegion), "selfLink": s.link("zones/europe-west3-b")},
			{"name": "europe-west3-c", "status": "DOWN", "region": s.link("regions/" + stubRegion), "selfLink": s.link("zones/europe-west3-c")}})
	case key == "GET /zones/"+stubZone:
		answer(200, map[string]any{"kind": "compute#zone", "name": stubZone, "status": "UP", "region": s.link("regions/" + stubRegion), "selfLink": s.link("zones/" + stubZone)})
	case key == "GET /zones/"+stubZone+"/machineTypes":
		mt := func(name string, cpus, mb int, shared bool, arch string) map[string]any {
			return map[string]any{"kind": "compute#machineType", "name": name, "guestCpus": cpus, "memoryMb": mb, "isSharedCpu": shared, "architecture": arch,
				"zone": stubZone, "selfLink": s.link("zones/" + stubZone + "/machineTypes/" + name)}
		}
		list("compute#machineTypeList", []map[string]any{mt("e2-medium", 2, 4096, true, "X86_64"), mt("e2-small", 2, 2048, true, "X86_64"),
			mt("e2-standard-2", 2, 8192, false, "X86_64"), mt("t2a-standard-2", 2, 8192, false, "ARM64")})
	case key == "GET /global/networks":
		var nets []map[string]any
		for _, n := range s.networks {
			nets = append(nets, map[string]any{"kind": "compute#network", "name": n, "autoCreateSubnetworks": true, "selfLink": s.link("global/networks/" + n)})
		}
		list("compute#networkList", nets)
	case key == "GET /global/firewalls":
		var out []map[string]any
		for _, k := range sortedKeys(s.firewalls) {
			out = append(out, s.firewalls[k])
		}
		list("compute#firewallList", out)
	case key == "POST /global/firewalls":
		name, _ := body["name"].(string)
		if s.firewalls[name] != nil {
			fail(409, "alreadyExists", "The resource 'projects/"+s.project+"/global/firewalls/"+name+"' already exists")
			return
		}
		body["kind"], body["selfLink"], body["disabled"] = "compute#firewall", s.link("global/firewalls/"+name), false
		s.firewalls[name] = body
		done("global/", "global/firewalls/"+name)
	case len(parts) == 3 && parts[0] == "global" && parts[1] == "firewalls":
		f := s.firewalls[parts[2]]
		if f == nil {
			missing()
			return
		}
		switch r.Method {
		case http.MethodGet:
			answer(200, f)
		case http.MethodPatch:
			for k, v := range body {
				f[k] = v
			}
			done("global/", "global/firewalls/"+parts[2])
		case http.MethodDelete:
			delete(s.firewalls, parts[2])
			done("global/", "global/firewalls/"+parts[2])
		}
	case key == "GET /aggregated/addresses":
		by := map[string][]map[string]any{}
		for _, k := range sortedKeys(s.addresses) {
			region := strings.Split(k, "/")[0]
			by["regions/"+region] = append(by["regions/"+region], inUse(s.addresses[k]))
		}
		aggregated("compute#addressAggregatedList", "addresses", by)
	case len(parts) == 3 && parts[0] == "regions" && parts[2] == "addresses" && r.Method == http.MethodPost:
		name, _ := body["name"].(string)
		k := parts[1] + "/" + name
		if s.addresses[k] != nil {
			fail(409, "alreadyExists", "The resource 'projects/"+s.project+"/regions/"+parts[1]+"/addresses/"+name+"' already exists")
			return
		}
		s.next++
		body["kind"], body["address"], body["addressType"], body["networkTier"] = "compute#address", fmt.Sprintf("198.51.100.%d", s.next-4235), "EXTERNAL", "PREMIUM"
		body["region"], body["selfLink"] = s.link("regions/"+parts[1]), s.link("regions/"+k[:len(parts[1])]+"/addresses/"+name)
		s.addresses[k] = body
		done("regions/"+parts[1]+"/", "regions/"+parts[1]+"/addresses/"+name)
	case len(parts) == 4 && parts[0] == "regions" && parts[2] == "addresses":
		k := parts[1] + "/" + parts[3]
		a := s.addresses[k]
		if a == nil {
			missing()
			return
		}
		switch r.Method {
		case http.MethodGet:
			answer(200, inUse(a))
		case http.MethodDelete:
			if inUse(a)["status"] == "IN_USE" {
				fail(400, "resourceInUseByAnotherResource", "The address resource 'projects/"+s.project+"/regions/"+parts[1]+"/addresses/"+parts[3]+"' is already being used by another resource")
				return
			}
			delete(s.addresses, k)
			done("regions/"+parts[1]+"/", "regions/"+parts[1]+"/addresses/"+parts[3])
		}
	case key == "GET /aggregated/instances":
		by := map[string][]map[string]any{}
		for _, k := range sortedKeys(s.instances) {
			zone := strings.Split(k, "/")[0]
			by["zones/"+zone] = append(by["zones/"+zone], shown(s.instances[k]))
		}
		aggregated("compute#instanceAggregatedList", "instances", by)
	case len(parts) == 3 && parts[0] == "zones" && parts[2] == "instances" && r.Method == http.MethodPost:
		name, _ := body["name"].(string)
		k := parts[1] + "/" + name
		if s.instances[k] != nil {
			fail(409, "alreadyExists", "The resource 'projects/"+s.project+"/zones/"+parts[1]+"/instances/"+name+"' already exists")
			return
		}
		s.next++
		body["kind"], body["id"], body["zone"], body["selfLink"] = "compute#instance", fmt.Sprint(s.next*1000003), s.link("zones/"+parts[1]), s.link("zones/"+parts[1]+"/instances/"+name)
		// The address asked for, else one Google lends.
		body["_ip"] = "203.0.113.7"
		if nics, _ := body["networkInterfaces"].([]any); len(nics) > 0 {
			if acs, _ := nics[0].(map[string]any)["accessConfigs"].([]any); len(acs) > 0 {
				if ip, _ := acs[0].(map[string]any)["natIP"].(string); ip != "" {
					body["_ip"] = ip
				}
			}
		}
		s.instances[k] = body
		done("zones/"+parts[1]+"/", "zones/"+parts[1]+"/instances/"+name)
	case len(parts) == 4 && parts[0] == "zones" && parts[2] == "instances":
		k := parts[1] + "/" + parts[3]
		in := s.instances[k]
		if in == nil {
			missing()
			return
		}
		switch r.Method {
		case http.MethodGet:
			answer(200, shown(in))
		case http.MethodDelete:
			delete(s.instances, k)
			done("zones/"+parts[1]+"/", "zones/"+parts[1]+"/instances/"+parts[3])
		}
	case len(parts) == 5 && parts[0] == "zones" && parts[2] == "instances" && r.Method == http.MethodPost:
		in := s.instances[parts[1]+"/"+parts[3]]
		if in == nil {
			missing()
			return
		}
		status, _ := in["_status"].(string)
		switch parts[4] {
		case "stop":
			in["_status"] = "TERMINATED"
		case "start":
			in["_status"] = ""
		case "setMachineType":
			if status != "TERMINATED" {
				fail(400, "resourceNotReady", "Instance is starting or running, and its machine type can be changed only while it is stopped.")
				return
			}
			in["machineType"] = body["machineType"]
		case "setDiskAutoDelete":
			q := r.URL.Query()
			for _, d := range in["disks"].([]any) {
				if q.Get("deviceName") == "persistent-disk-0" {
					d.(map[string]any)["autoDelete"] = strings.EqualFold(q.Get("autoDelete"), "true")
				}
			}
		default:
			s.t.Errorf("the stub has no answer for %s", key)
			missing()
			return
		}
		done("zones/"+parts[1]+"/", "zones/"+parts[1]+"/instances/"+parts[3])
	case len(parts) >= 4 && parts[len(parts)-2] != "operations" && parts[len(parts)-3] == "operations" && parts[len(parts)-1] == "wait",
		len(parts) >= 3 && parts[len(parts)-2] == "operations" && r.Method == http.MethodGet:
		// gcloud waits for a stop, a start and a change of type by asking
		// for the operation again; it is done.
		name := parts[len(parts)-1]
		if name == "wait" {
			name = parts[len(parts)-2]
		}
		if s.ops[name] == nil {
			missing()
			return
		}
		answer(200, s.ops[name])
	case key == "GET /global/snapshots":
		var out []map[string]any
		for _, k := range sortedKeys(s.snapshots) {
			out = append(out, snapshot(s.snapshots[k]))
		}
		list("compute#snapshotList", out)
	case key == "POST /global/snapshots":
		name, _ := body["name"].(string)
		if s.snapshots[name] != nil {
			fail(409, "alreadyExists", "The resource 'projects/"+s.project+"/global/snapshots/"+name+"' already exists")
			return
		}
		s.next++
		body["kind"], body["id"], body["selfLink"] = "compute#snapshot", fmt.Sprint(s.next*1000003), s.link("global/snapshots/"+name)
		body["creationTimestamp"], body["diskSizeGb"], body["storageLocations"] = "2026-11-14T04:02:25.941-08:00", "50", []string{"eu"}
		s.snapshots[name] = body
		done("global/", "global/snapshots/"+name)
	case len(parts) == 3 && parts[0] == "global" && parts[1] == "snapshots":
		snap := s.snapshots[parts[2]]
		if snap == nil {
			missing()
			return
		}
		switch r.Method {
		case http.MethodGet:
			answer(200, snapshot(snap))
		case http.MethodDelete:
			delete(s.snapshots, parts[2])
			done("global/", "global/snapshots/"+parts[2])
		}
	default:
		s.t.Errorf("the stub has no answer for %s?%s", key, r.URL.RawQuery)
		missing()
	}
}

// realGcloud starts the stub and returns a client that runs the real
// gcloud against it. The token reaches gcloud through a file named in the
// environment (gcloud's auth/access_token_file property); it is on no
// command line. gcloud's own configuration lives under a directory of the
// test's.
func realGcloud(t *testing.T) (*apiStub, *Client) {
	t.Helper()
	bin := os.Getenv("KEELBOX_TEST_GCLOUD")
	if bin == "" {
		t.Skip("set KEELBOX_TEST_GCLOUD to a gcloud binary to check keelbox against the real CLI (docs/contributing/testing.md)")
	}
	old := poll
	poll = time.Millisecond
	t.Cleanup(func() { poll = old })
	s := &apiStub{t: t, token: "stub-token", project: "acme-dev", next: 4241, networks: []string{"default"},
		instances: map[string]map[string]any{}, firewalls: map[string]map[string]any{}, addresses: map[string]map[string]any{}, snapshots: map[string]map[string]any{}, ops: map[string]map[string]any{},
		bodies: map[string][]map[string]any{}, deny: map[string]bool{}}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	s.url = srv.URL
	home := t.TempDir()
	token := filepath.Join(home, "token")
	if err := os.WriteFile(token, []byte(s.token), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("CLOUDSDK_CONFIG", filepath.Join(home, "gcloud"))
	t.Setenv("CLOUDSDK_API_ENDPOINT_OVERRIDES_COMPUTE", srv.URL+"/compute/v1/")
	t.Setenv("CLOUDSDK_AUTH_ACCESS_TOKEN_FILE", token)
	t.Setenv("CLOUDSDK_CORE_PROJECT", s.project)
	t.Setenv("CLOUDSDK_CORE_DISABLE_USAGE_REPORTING", "true")
	t.Setenv("CLOUDSDK_COMPONENT_MANAGER_DISABLE_UPDATE_CHECK", "true")
	for _, v := range []string{"CLOUDSDK_COMPUTE_ZONE", "CLOUDSDK_COMPUTE_REGION", "CLOUDSDK_CORE_ACCOUNT", "CLOUDSDK_ACTIVE_CONFIG_NAME"} {
		t.Setenv(v, "")
	}
	return s, &Client{Run: Exec{Path: bin}}
}

func TestRealCLICreateFindDestroy(t *testing.T) {
	s, cl := realGcloud(t)
	s.staging = 2
	ctx := context.Background()
	c := boxConfig(t, ", labels: {team: web}")

	acct, err := cl.Preflight(ctx, stubZone, "e2-medium")
	if err != nil || acct.Describe() != "project acme-dev" || cl.Project != "acme-dev" {
		t.Fatalf("preflight: %+v %v", acct, err)
	}
	if _, err := cl.Preflight(ctx, "europe-west3-c", ""); err == nil || !strings.Contains(err.Error(), "europe-west3-a, europe-west3-b") {
		t.Errorf("a zone that is down: %v", err)
	}
	if _, err := cl.Preflight(ctx, stubZone, "t2a-standard-2"); err == nil || !strings.Contains(err.Error(), "an Arm type") {
		t.Errorf("an Arm type: %v", err)
	}
	in, err := Create(ctx, cl, c, testHostKey, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	if in.Name != "acme" || in.Status != "RUNNING" || !in.Managed("acme") || in.ZoneName() != stubZone || in.TypeName() != "e2-medium" || !in.HasTag("keelbox-box-acme") || len(in.LoginProblems()) != 0 {
		t.Errorf("instance %+v, problems %v", in, in.LoginProblems())
	}
	// What gcloud sent for the VM: the user data read from the file, the
	// labels and the tag, the reserved address, no service account.
	sent := s.bodies["POST /zones/"+stubZone+"/instances"]
	if len(sent) != 1 {
		t.Fatalf("%d instance creates", len(sent))
	}
	raw, _ := json.Marshal(sent[0])
	var b struct {
		Name, MachineType string
		Labels            map[string]string
		Tags              struct{ Items []string }
		Metadata          struct{ Items []struct{ Key, Value string } }
		ServiceAccounts   []any
		Disks             []struct {
			AutoDelete, Boot bool
			InitializeParams struct{ DiskSizeGb, DiskType, SourceImage string }
		}
		NetworkInterfaces []struct {
			Network       string
			AccessConfigs []struct{ NatIP string }
		}
	}
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatal(err)
	}
	meta := map[string]string{}
	for _, m := range b.Metadata.Items {
		meta[m.Key] = m.Value
	}
	if ud := meta["user-data"]; !strings.HasPrefix(ud, "#cloud-config\n") || !strings.Contains(ud, "DO-NOT-LEAK") || !strings.Contains(ud, "set_host_keys = false") {
		t.Errorf("user data: %q", ud)
	}
	if meta["block-project-ssh-keys"] != "true" || meta["enable-oslogin"] != "false" || len(meta) != 3 || len(b.ServiceAccounts) != 0 {
		t.Errorf("metadata %v, service accounts %v", meta, b.ServiceAccounts)
	}
	addr := s.addresses[stubRegion+"/keelbox-acme"]
	if addr == nil || addr["description"] != managedDescription("the address", "acme") {
		t.Fatalf("address %v", addr)
	}
	if b.Name != "acme" || !strings.HasSuffix(b.MachineType, "/zones/"+stubZone+"/machineTypes/e2-medium") ||
		fmt.Sprint(b.Labels) != "map[keelbox:true keelbox-box:acme team:web]" || fmt.Sprint(b.Tags.Items) != "[keelbox-box-acme]" ||
		len(b.Disks) != 1 || !b.Disks[0].Boot || !b.Disks[0].AutoDelete || b.Disks[0].InitializeParams.DiskSizeGb != "50" ||
		!strings.HasSuffix(b.Disks[0].InitializeParams.DiskType, "/diskTypes/pd-balanced") ||
		!strings.HasSuffix(b.Disks[0].InitializeParams.SourceImage, "projects/ubuntu-os-cloud/zones/-/imageFamilyViews/ubuntu-2404-lts-amd64") ||
		len(b.NetworkInterfaces) != 1 || !strings.HasSuffix(b.NetworkInterfaces[0].Network, "/global/networks/default") ||
		len(b.NetworkInterfaces[0].AccessConfigs) != 1 || b.NetworkInterfaces[0].AccessConfigs[0].NatIP != addr["address"] || in.PublicIPv4() != addr["address"] {
		t.Errorf("create body %s", raw)
	}
	// And for the firewall: two rules on the box's tag, the deny one first.
	fw := s.bodies["POST /global/firewalls"]
	if len(fw) != 2 || fw[0]["name"] != "keelbox-acme-deny" || fw[1]["name"] != "keelbox-acme-allow" {
		t.Fatalf("firewall creates %v", fw)
	}
	f, err := FindFirewall(ctx, cl, "acme")
	if err != nil || !f.InStep(true) || f.Describe() != "in tcp 22, in udp 41641" || f.Allow.Priority != 900 || f.Deny.Priority != 901 {
		t.Fatalf("firewall as gcloud prints it: %+v %v", f, err)
	}
	got, err := Find(ctx, cl, "acme")
	if err != nil || got.ID != in.ID {
		t.Fatalf("find %+v %v", got, err)
	}
	// Closed to SSH by an update that keeps the rule's tag and description.
	no := false
	f, changes, err := EnsureFirewall(ctx, cl, "acme", &no)
	if err != nil || len(changes) != 1 || f.HasPublicSSH() || !f.InStep(false) {
		t.Fatalf("%+v %q %v", f, changes, err)
	}
	if f, changes, err = EnsureFirewall(ctx, cl, "acme", nil); err != nil || len(changes) != 0 || f.HasPublicSSH() {
		t.Fatalf("after the update: %+v %q %v", f, changes, err)
	}
	// A rule disabled by hand is enabled again.
	s.firewalls["keelbox-acme-deny"]["disabled"] = true
	if f, changes, err = EnsureFirewall(ctx, cl, "acme", nil); err != nil || len(changes) != 1 || !f.InStep(false) {
		t.Fatalf("a disabled rule: %+v %q %v", f, changes, err)
	}
	if _, err := Create(ctx, cl, c, testHostKey, t.Logf); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("second create: %v", err)
	}
	// The address is in use: the sweep leaves it and the rules.
	if _, err := Sweep(ctx, cl, "acme"); err == nil || !strings.Contains(err.Error(), "still carries") {
		t.Errorf("sweep with the VM there: %v", err)
	}
	done, err := Destroy(ctx, cl, got, "acme")
	if err != nil || len(done) != 4 {
		t.Fatalf("destroy %q %v", done, err)
	}
	if len(s.instances)+len(s.firewalls)+len(s.addresses) != 0 {
		t.Errorf("left: %v %v %v", s.instances, s.firewalls, s.addresses)
	}
	if _, err := Find(ctx, cl, "acme"); err == nil || !strings.Contains(err.Error(), "keelbox up acme") {
		t.Errorf("find after destroy: %v", err)
	}
}

// The calls of keelbox resize, park and resume, as the real gcloud takes
// them and sends them on.
func TestRealCLIResizeParkResume(t *testing.T) {
	s, cl := realGcloud(t)
	ctx := context.Background()
	c := boxConfig(t, ", labels: {team: web}")
	in, err := Create(ctx, cl, c, testHostKey, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	ip := in.PublicIPv4()
	// gcloud prints the VM's disks: the boot disk, deleted with the VM.
	if d := in.BootDisk(); d == nil || d.Name() != "acme" || !d.AutoDelete || len(in.OtherDisks()) != 0 {
		t.Fatalf("disks as gcloud prints them: %+v", in.Disks)
	}
	vm := s.instances[stubZone+"/acme"]

	// Resize: stop, change the type, start, at the address it had.
	if err := Stop(ctx, cl, "acme", in, t.Logf); err != nil {
		t.Fatal(err)
	}
	off, err := Find(ctx, cl, "acme")
	if err != nil || !off.Stopped() || off.PublicIPv4() != ip {
		t.Fatalf("the stopped VM: %+v %v", off, err)
	}
	got, err := Resize(ctx, cl, "acme", in, "e2-standard-2")
	if err != nil || got.TypeName() != "e2-standard-2" || !got.Stopped() {
		t.Fatalf("resize: %+v %v", got, err)
	}
	if sent := s.bodies["POST /zones/"+stubZone+"/instances/acme/setMachineType"]; len(sent) != 1 || !strings.HasSuffix(fmt.Sprint(sent[0]["machineType"]), "/zones/"+stubZone+"/machineTypes/e2-standard-2") {
		t.Errorf("setMachineType bodies %v", sent)
	}
	run, err := Start(ctx, cl, "acme", in)
	if err != nil || run.Status != "RUNNING" || run.PublicIPv4() != ip || run.TypeName() != "e2-standard-2" {
		t.Fatalf("start: %+v %v", run, err)
	}
	// A VM that runs is refused by the API, and the error says so.
	if err := cl.compute(ctx, nil, "instances", "set-machine-type", "acme", "--zone", stubZone, "--machine-type", "e2-small"); err == nil || !strings.Contains(err.Error(), "only while it is stopped") {
		t.Errorf("set-machine-type on a running VM: %v", err)
	}

	// Park: a snapshot of the boot disk with the parked labels, read
	// until it is READY; one that fails is deleted.
	if err := Stop(ctx, cl, "acme", in, t.Logf); err != nil {
		t.Fatal(err)
	}
	s.uploading, s.snapshotFails = 2, true
	if _, err := TakeSnapshot(ctx, cl, c, in); err == nil || !strings.Contains(err.Error(), "FAILED") || len(s.snapshots) != 0 {
		t.Fatalf("a snapshot that fails: %v, %d left", err, len(s.snapshots))
	}
	s.uploading, s.snapshotFails = 4, false
	snap, err := TakeSnapshot(ctx, cl, c, in)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Status != "READY" || !snap.Parked("acme") || snap.Created().IsZero() || snap.Describe() != "1.3 GB stored of a 50 GB disk" || s.snapshots[snap.Name] == nil {
		t.Fatalf("snapshot as gcloud prints it: %+v", snap)
	}
	sent := s.bodies["POST /global/snapshots"]
	if len(sent) != 2 || sent[1]["name"] != snap.Name || !strings.HasSuffix(fmt.Sprint(sent[1]["sourceDisk"]), "/zones/"+stubZone+"/disks/acme") ||
		fmt.Sprint(sent[1]["labels"]) != "map[keelbox:true keelbox-box:acme keelbox-parked:true team:web]" || !strings.HasPrefix(fmt.Sprint(sent[1]["description"]), "Managed by keelbox") {
		t.Errorf("snapshot bodies %v", sent)
	}
	if p, err := Parked(ctx, cl, "acme"); err != nil || p == nil || p.Name != snap.Name {
		t.Fatalf("parked: %+v %v", p, err)
	}
	// A boot disk told to outlive its VM is told otherwise first.
	vm["disks"].([]any)[0].(map[string]any)["autoDelete"] = false
	done, err := Release(ctx, cl, "acme", in)
	if err != nil || len(done) != 2 || len(s.instances)+len(s.addresses) != 0 || len(s.firewalls) != 2 || len(s.snapshots) != 1 {
		t.Fatalf("release: %q %v; left %d VMs, %d addresses, %d rules, %d snapshots", done, err, len(s.instances), len(s.addresses), len(s.firewalls), len(s.snapshots))
	}
	if vm["disks"].([]any)[0].(map[string]any)["autoDelete"] != true {
		t.Error("the boot disk was not set to go with the VM")
	}

	// Resume: the VM from the snapshot, with the login settings of a
	// created box and user data that creates no account.
	back, err := Restore(ctx, cl, c, snap, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	creates := s.bodies["POST /zones/"+stubZone+"/instances"]
	raw, _ := json.Marshal(creates[len(creates)-1])
	var b struct {
		Name, MachineType string
		Labels            map[string]string
		Tags              struct{ Items []string }
		Metadata          struct{ Items []struct{ Key, Value string } }
		ServiceAccounts   []any
		Disks             []struct {
			AutoDelete, Boot bool
			InitializeParams struct{ DiskSizeGb, DiskType, SourceImage, SourceSnapshot string }
		}
		NetworkInterfaces []struct {
			AccessConfigs []struct{ NatIP string }
		}
	}
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatal(err)
	}
	meta := map[string]string{}
	for _, m := range b.Metadata.Items {
		meta[m.Key] = m.Value
	}
	addr := s.addresses[stubRegion+"/keelbox-acme"]
	if addr == nil || back.PublicIPv4() != addr["address"] || back.Status != "RUNNING" || !back.Managed("acme") || !back.HasTag("keelbox-box-acme") || len(back.LoginProblems()) != 0 {
		t.Fatalf("restored %+v on %v", back, addr)
	}
	if meta["user-data"] != ResumeUserData("acme") || meta["block-project-ssh-keys"] != "true" || meta["enable-oslogin"] != "false" || len(meta) != 3 || len(b.ServiceAccounts) != 0 {
		t.Errorf("metadata %v, service accounts %v", meta, b.ServiceAccounts)
	}
	if len(creates) != 2 || b.Name != "acme" || !strings.HasSuffix(b.MachineType, "/machineTypes/e2-medium") ||
		fmt.Sprint(b.Labels) != "map[keelbox:true keelbox-box:acme team:web]" || fmt.Sprint(b.Tags.Items) != "[keelbox-box-acme]" ||
		len(b.Disks) != 1 || !b.Disks[0].Boot || !b.Disks[0].AutoDelete || b.Disks[0].InitializeParams.SourceImage != "" || b.Disks[0].InitializeParams.DiskSizeGb != "" ||
		!strings.HasSuffix(b.Disks[0].InitializeParams.SourceSnapshot, "/global/snapshots/"+snap.Name) || !strings.HasSuffix(b.Disks[0].InitializeParams.DiskType, "/diskTypes/pd-balanced") ||
		len(b.NetworkInterfaces) != 1 || len(b.NetworkInterfaces[0].AccessConfigs) != 1 || b.NetworkInterfaces[0].AccessConfigs[0].NatIP != addr["address"] {
		t.Errorf("create body %s", raw)
	}
	// The rules park kept were not made again.
	if fw := s.bodies["POST /global/firewalls"]; len(fw) != 2 {
		t.Errorf("%d firewall creates", len(fw))
	}
	if err := DeleteSnapshot(ctx, cl, "acme", snap); err != nil || len(s.snapshots) != 0 {
		t.Fatalf("delete the snapshot: %v, %d left", err, len(s.snapshots))
	}
	// Gone already: gcloud's error for it is not one.
	if err := DeleteSnapshot(ctx, cl, "acme", snap); err != nil {
		t.Errorf("delete a snapshot that is gone: %v", err)
	}
	if p, err := Parked(ctx, cl, "acme"); p != nil || err != nil {
		t.Errorf("parked after the resume: %+v %v", p, err)
	}
	// gcloud's filter finds the box's snapshots by label, and the sweep
	// deletes them once the VM is gone.
	for _, name := range []string{"keelbox-acme-left", "keelbox-other-left"} {
		box := strings.TrimSuffix(strings.TrimPrefix(name, "keelbox-"), "-left")
		s.snapshots[name] = map[string]any{"kind": "compute#snapshot", "name": name, "id": "7", "labels": provider.ParkedLabels(box, nil),
			"creationTimestamp": "2026-11-14T04:02:25.941-08:00", "diskSizeGb": "50", "selfLink": s.link("global/snapshots/" + name)}
	}
	if all, err := Snapshots(ctx, cl, "acme"); err != nil || len(all) != 1 || all[0].Name != "keelbox-acme-left" {
		t.Fatalf("snapshots of the box: %+v %v", all, err)
	}
	if _, err := Destroy(ctx, cl, back, "acme"); err != nil {
		t.Fatal(err)
	}
	if len(s.instances)+len(s.firewalls)+len(s.addresses) != 0 || len(s.snapshots) != 1 || s.snapshots["keelbox-other-left"] == nil {
		t.Errorf("left: %v %v %v %v", s.instances, s.firewalls, s.addresses, s.snapshots)
	}
}

func TestRealCLIErrors(t *testing.T) {
	s, cl := realGcloud(t)
	ctx := context.Background()
	if _, err := cl.Account(ctx); err != nil {
		t.Fatal(err)
	}
	// A resource that is not there: the API's 404, as gcloud prints it.
	err := cl.compute(ctx, nil, "firewall-rules", "delete", "nope")
	var ge *Error
	if !errors.As(err, &ge) || !IsNotFound(err) || ge.Message != "The resource 'projects/acme-dev/global/firewalls/nope' was not found" {
		t.Errorf("404: %+v", err)
	}
	// A list one request failed for: gcloud exits 0 for VMs and addresses,
	// 1 for firewall rules; both are errors.
	for _, path := range []string{"GET /aggregated/instances", "GET /aggregated/addresses", "GET /global/firewalls"} {
		s.deny[path] = true
	}
	if _, err := Find(ctx, cl, "acme"); err == nil || !strings.Contains(err.Error(), "permission") {
		t.Errorf("a refused list of VMs: %v", err)
	}
	if a, err := FindAddress(ctx, cl, "acme"); err == nil || a != nil {
		t.Errorf("a refused list of addresses: %+v %v", a, err)
	}
	if _, err := FindFirewall(ctx, cl, "acme"); err == nil || !strings.Contains(err.Error(), "permission") {
		t.Errorf("a refused list of rules: %v", err)
	}
	s.deny = map[string]bool{}
	// The Compute Engine API is off in the project.
	s.apiOff = true
	if _, err := cl.Account(ctx); !errors.Is(err, ErrAPIDisabled) || !strings.Contains(err.Error(), "gcloud services enable compute.googleapis.com --project acme-dev") {
		t.Errorf("API off: %v", err)
	}
	s.apiOff = false
	// A project the sign-in cannot see.
	other := &Client{Project: "acme-typo", Run: cl.Run}
	if _, err := other.Account(ctx); err == nil || !strings.Contains(err.Error(), "cannot use Compute Engine in project acme-typo") {
		t.Errorf("unknown project: %v", err)
	}
	// A token Google refuses.
	s.token = "another"
	if _, err := cl.Account(ctx); !errors.Is(err, ErrNotSignedIn) || !strings.Contains(err.Error(), "gcloud auth login") {
		t.Errorf("401: %v", err)
	}
	// No sign-in at all.
	t.Setenv("CLOUDSDK_AUTH_ACCESS_TOKEN_FILE", "")
	if _, err := cl.Account(ctx); !errors.Is(err, ErrNotSignedIn) || !strings.Contains(err.Error(), "do not currently have an active account selected. Sign in: gcloud auth login") {
		t.Errorf("no account: %v", err)
	}
	// No project anywhere.
	t.Setenv("CLOUDSDK_CORE_PROJECT", "")
	if _, err := (&Client{Run: cl.Run}).Account(ctx); !errors.Is(err, ErrNoProject) {
		t.Errorf("no project: %v", err)
	}
	// A flag gcloud does not know fails before any request.
	err = cl.compute(ctx, nil, "instances", "list", "--no-such-flag")
	if !errors.As(err, &ge) || !strings.Contains(ge.Message, "unrecognized arguments") || !strings.Contains(ge.Message, "--no-such-flag") {
		t.Errorf("unknown flag: %+v", err)
	}
}

// gcloud's own configuration supplies the account, the project and the zone.
func TestRealCLISettings(t *testing.T) {
	_, cl := realGcloud(t)
	t.Setenv("CLOUDSDK_COMPUTE_ZONE", "europe-west3-b")
	t.Setenv("CLOUDSDK_CORE_ACCOUNT", "sam@example.com")
	set, err := cl.Settings(context.Background())
	if err != nil || set.Project != "acme-dev" || set.Zone != "europe-west3-b" || set.Account != "sam@example.com" {
		t.Errorf("%+v %v", set, err)
	}
}

// gcloud follows the API's pages itself and prints one list, and its
// filter finds a VM by label or by name in any zone.
func TestRealCLIFollowsPages(t *testing.T) {
	s, cl := realGcloud(t)
	s.pageSize = 1
	for i, name := range []string{"web", "db", "acme"} {
		zone := []string{stubZone, "europe-west3-b", "europe-west3-b"}[i]
		labels := map[string]any{"keelbox": "true", "keelbox-box": name}
		s.instances[zone+"/"+name] = map[string]any{"id": fmt.Sprint(100 + i), "name": name, "zone": s.link("zones/" + zone), "labels": labels,
			"tags": map[string]any{"items": []any{"keelbox-box-" + name}}, "_ip": fmt.Sprintf("203.0.113.%d", i)}
	}
	// One of the box's name that keelbox did not make, in another zone.
	s.instances[stubZone+"/db"] = map[string]any{"id": "200", "name": "db", "zone": s.link("zones/" + stubZone), "_ip": "203.0.113.9"}
	ctx := context.Background()
	if in, err := Find(ctx, cl, "acme"); err != nil || in.ID != "102" || in.ZoneName() != "europe-west3-b" {
		t.Errorf("find on the last page: %+v %v", in, err)
	}
	if named, err := cl.InstancesNamed(ctx, "db"); err != nil || len(named) != 2 {
		t.Errorf("by name: %+v %v", named, err)
	}
	if in, err := Find(ctx, cl, "db"); err != nil || in.ID != "101" {
		t.Errorf("the labelled one of two: %+v %v", in, err)
	}
	if tagged, err := cl.InstancesWhere(ctx, "tags.items=keelbox-box-web"); err != nil || len(tagged) != 1 || tagged[0].Name != "web" {
		t.Errorf("by tag: %+v %v", tagged, err)
	}
}
