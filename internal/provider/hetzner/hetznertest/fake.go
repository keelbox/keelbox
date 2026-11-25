// Package hetznertest is a fake Hetzner Cloud API for tests: token checks,
// server types and locations, project SSH keys, and a project's servers,
// firewalls and snapshots with the actions keelbox runs on them (create,
// shut down, power on, change type, snapshot, delete). It answers like the
// real API, including its errors: 401 for an unknown token, 403 for a
// write with a read-only one, 409 for a key whose fingerprint or name is
// taken, 422 for an empty key or a type change on a running server.
package hetznertest

import (
	"crypto/md5" //nolint:gosec // G501: Hetzner's key fingerprint
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Key is a project SSH key.
type Key struct {
	ID          int64             `json:"id"`
	Name        string            `json:"name"`
	Fingerprint string            `json:"fingerprint"`
	PublicKey   string            `json:"public_key"`
	Labels      map[string]string `json:"labels"`
}

// API is the fake. Fields may be changed between requests.
type API struct {
	mu sync.Mutex
	// Token is the read and write token; ReadOnlyToken may only read.
	Token, ReadOnlyToken string
	Keys                 []Key
	// Servers are listed by GET /servers, as JSON objects.
	Servers []map[string]any
	// Firewalls and Images (snapshots) are the project's, as JSON objects.
	Firewalls []map[string]any
	Images    []map[string]any
	// Created are the bodies of the POST /servers calls, in order.
	Created []map[string]any
	// Fail makes a call answer a server error: shutdown, poweron,
	// change_type, create_image, delete_server, delete_image,
	// create_server, list_firewalls, delete_firewall or
	// list_server_types.
	Fail map[string]bool
	// OnCall, when set, sees each request as "METHOD /path" before the
	// fake answers it, and may change the fake's fields (it runs with the
	// fake locked).
	OnCall func(call string)
	// Refuse makes create_server or change_type answer with an error of
	// Hetzner's own, as it does when it will not take an order: by the
	// call's name, or "create_server fsn1" for one location only.
	Refuse map[string]Error
	// SnapshotFails makes the action of a create_image call end in an
	// error, leaving the image in status creating, as a failed snapshot
	// is on Hetzner.
	SnapshotFails bool
	// SnapshotStatus is the status a new snapshot reports once its action
	// is done; default available.
	SnapshotStatus string
	// IgnoresShutdown leaves a server running after a shutdown request, as
	// a hung operating system does; poweroff still stops it.
	IgnoresShutdown bool
	// Requests records "METHOD /path?query" for every call.
	Requests []string
	URL      string

	nextID  int
	failed  map[string]bool // ids of actions that ended in an error
	actions int
}

// Error is an error answer of the API.
type Error struct {
	Status        int
	Code, Message string
}

// refused answers a call the fake was told to refuse, and reports whether
// it did.
func (a *API) refused(call, location string, fail func(int, string, string)) bool {
	e, ok := a.Refuse[call+" "+location]
	if !ok {
		e, ok = a.Refuse[call]
	}
	if ok {
		fail(e.Status, e.Code, e.Message)
	}
	return ok
}

// Server is a keelbox-made server for box as the API lists it: running,
// of type cx23 in fsn1 with a 40 GB disk, labelled for the box.
func Server(id int, box, ip string) map[string]any {
	return map[string]any{"id": id, "name": box, "status": "running",
		"labels":      map[string]string{"keelbox": "true", "keelbox-box": box},
		"public_net":  map[string]any{"ipv4": map[string]any{"ip": ip}},
		"server_type": serverType("cx23"), "primary_disk_size": 40,
		"location": map[string]any{"name": "fsn1"}}
}

// Firewall is the box's keelbox firewall as the API lists it, admitting
// Tailscale and SSH, applied to the servers given.
func Firewall(id int, box string, servers ...int) map[string]any {
	applied := []any{}
	for _, s := range servers {
		applied = append(applied, map[string]any{"type": "server", "server": map[string]any{"id": s}})
	}
	anywhere := []string{"0.0.0.0/0", "::/0"}
	return map[string]any{"id": id, "name": "keelbox-" + box,
		"labels": map[string]string{"keelbox": "true", "keelbox-box": box},
		"rules": []any{
			map[string]any{"direction": "in", "protocol": "udp", "port": "41641", "source_ips": anywhere, "description": "Tailscale direct connections"},
			map[string]any{"direction": "in", "protocol": "tcp", "port": "22", "source_ips": anywhere, "description": "SSH until keelbox reaches the box over the tailnet"},
		},
		"applied_to": applied}
}

// Snapshot is the parked snapshot of box as the API lists it.
func Snapshot(id int, box string, created time.Time) map[string]any {
	return map[string]any{"id": id, "type": "snapshot", "status": "available", "description": "keelbox: parked box " + box,
		"image_size": 2.5, "disk_size": 40.0, "architecture": "x86", "created": created.UTC().Format(time.RFC3339),
		"labels": map[string]string{"keelbox": "true", "keelbox-box": box, "keelbox-parked": "true"}}
}

func serverType(name string) map[string]any {
	for _, t := range ServerTypes() {
		if t["name"] == name {
			return t
		}
	}
	return nil
}

// labelsOf reads an object's labels, whichever map type holds them.
func labelsOf(o map[string]any) map[string]string {
	out := map[string]string{}
	switch l := o["labels"].(type) {
	case map[string]string:
		return l
	case map[string]any:
		for k, v := range l {
			out[k] = fmt.Sprint(v)
		}
	}
	return out
}

// selected reports whether labels match a label selector of k=v terms.
func selected(sel string, o map[string]any) bool {
	labels := labelsOf(o)
	for _, kv := range strings.Split(sel, ",") {
		k, v, _ := strings.Cut(kv, "=")
		if k != "" && labels[k] != v {
			return false
		}
	}
	return true
}

func (a *API) newID() int {
	if a.nextID == 0 {
		a.nextID = 1000
	}
	a.nextID++
	return a.nextID
}

// action is a new action's JSON; one that failed says so when polled.
func (a *API) action(failed bool) map[string]any {
	a.actions++
	id := strconv.Itoa(a.actions)
	if failed {
		if a.failed == nil {
			a.failed = map[string]bool{}
		}
		a.failed[id] = true
	}
	return map[string]any{"id": a.actions, "status": "running"}
}

func find(list []map[string]any, id string) (int, map[string]any) {
	for i, o := range list {
		if fmt.Sprint(o["id"]) == id {
			return i, o
		}
	}
	return -1, nil
}

func number(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	}
	return 0, false
}

// New starts the fake with a realistic catalogue and returns it.
func New(t *testing.T, token string) *API {
	a := &API{Token: token, ReadOnlyToken: "read-only-" + token}
	srv := httptest.NewServer(http.HandlerFunc(a.serve))
	t.Cleanup(srv.Close)
	a.URL = srv.URL
	return a
}

// Fingerprint is Hetzner's MD5 fingerprint of a public key line.
func Fingerprint(pub string) string {
	f := strings.Fields(pub)
	if len(f) < 2 {
		return ""
	}
	blob, _ := base64.StdEncoding.DecodeString(f[1])
	sum := md5.Sum(blob) //nolint:gosec // G401: an identifier
	hex := make([]string, len(sum))
	for i, b := range sum {
		hex[i] = fmt.Sprintf("%02x", b)
	}
	return strings.Join(hex, ":")
}

func price(loc, hourly, monthly string) map[string]any {
	return map[string]any{"location": loc, "price_hourly": map[string]string{"net": hourly, "gross": hourly},
		"price_monthly": map[string]string{"net": monthly, "gross": monthly}}
}

func avail(names ...string) []map[string]any {
	var out []map[string]any
	for _, n := range names {
		out = append(out, map[string]any{"name": n, "available": true})
	}
	return out
}

// ServerTypes is the catalogue: cx23 is the cheapest 2 vCPU 4 GB type,
// cpx11 is priced in fsn1 but no longer sold there, cx33 is being retired
// in fsn1, and cax11 is arm.
func ServerTypes() []map[string]any {
	return []map[string]any{
		{"id": 1, "name": "cpx02", "cores": 1, "memory": 1.0, "disk": 20, "cpu_type": "shared", "architecture": "x86",
			"prices": []any{price("fsn1", "0.0096", "5.99"), price("ash", "0.0110", "6.49")}, "locations": avail("fsn1", "ash")},
		{"id": 2, "name": "cx23", "cores": 2, "memory": 4.0, "disk": 40, "cpu_type": "shared", "architecture": "x86",
			"prices": []any{price("fsn1", "0.0060", "3.79"), price("ash", "0.0090", "5.49")}, "locations": avail("fsn1", "ash")},
		{"id": 3, "name": "cpx22", "cores": 2, "memory": 4.0, "disk": 80, "cpu_type": "shared", "architecture": "x86",
			"prices": []any{price("fsn1", "0.0120", "7.49")}, "locations": avail("fsn1")},
		{"id": 4, "name": "cpx11", "cores": 2, "memory": 2.0, "disk": 40, "cpu_type": "shared", "architecture": "x86",
			"prices": []any{price("fsn1", "0.0050", "3.29")}, "locations": []map[string]any{{"name": "fsn1", "available": false}}},
		{"id": 5, "name": "cx33", "cores": 4, "memory": 8.0, "disk": 80, "cpu_type": "shared", "architecture": "x86",
			"prices":    []any{price("fsn1", "0.0100", "6.49")},
			"locations": []map[string]any{{"name": "fsn1", "available": true, "deprecation": map[string]string{"announced": "2026-10-01T00:00:00Z", "unavailable_after": "2027-01-01T00:00:00Z"}}}},
		{"id": 6, "name": "cax11", "cores": 2, "memory": 4.0, "disk": 40, "cpu_type": "shared", "architecture": "arm",
			"prices": []any{price("fsn1", "0.0060", "3.79")}, "locations": avail("fsn1")},
		{"id": 7, "name": "ccx13", "cores": 2, "memory": 8.0, "disk": 80, "cpu_type": "dedicated", "architecture": "x86",
			"prices": []any{price("fsn1", "0.0230", "14.49")}, "locations": avail("fsn1")},
	}
}

func (a *API) serve(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.Requests = append(a.Requests, r.Method+" "+r.URL.RequestURI())
	if a.OnCall != nil {
		a.OnCall(r.Method + " " + r.URL.Path)
	}
	fail := func(status int, code, msg string) {
		w.WriteHeader(status)
		_, _ = fmt.Fprintf(w, `{"error":{"code":%q,"message":%q}}`, code, msg) //nolint:gosec // G705: a test server writing JSON
	}
	auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	switch {
	case auth == a.Token:
	case auth == a.ReadOnlyToken && r.Method == "GET":
	case auth == a.ReadOnlyToken:
		fail(403, "forbidden", "Insufficient permissions for this request.")
		return
	default:
		fail(401, "unauthorized", "unable to authenticate") //nolint:misspell // the API's error code
		return
	}
	send := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	page := map[string]any{"pagination": map[string]any{"next_page": nil}}
	q := r.URL.Query()
	var body map[string]any
	if r.Method == "POST" && r.URL.Path != "/ssh_keys" {
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &body)
	}
	if a.project(w, r, body, fail, send, page) {
		return
	}
	switch p := r.URL.Path; {
	case p == "/server_types" && a.Fail["list_server_types"]:
		fail(500, "server_error", "the fake was told to fail list_server_types")
	case p == "/server_types":
		send(map[string]any{"server_types": ServerTypes(), "meta": page})
	case p == "/locations":
		send(map[string]any{"locations": []map[string]string{
			{"name": "fsn1", "city": "Falkenstein", "country": "DE", "network_zone": "eu-central", "description": "Falkenstein DC Park 1"},
			{"name": "ash", "city": "Ashburn, VA", "country": "US", "network_zone": "us-east", "description": "Ashburn, VA"},
		}})
	case p == "/servers" && r.Method == "GET":
		out := []map[string]any{}
		for _, s := range a.Servers {
			if n := q.Get("name"); (n == "" || s["name"] == n) && selected(q.Get("label_selector"), s) {
				out = append(out, s)
			}
		}
		send(map[string]any{"servers": out, "meta": page})
	case p == "/ssh_keys" && r.Method == "GET":
		out := []Key{}
		for _, k := range a.Keys {
			if (q.Get("name") == "" || k.Name == q.Get("name")) && (q.Get("fingerprint") == "" || k.Fingerprint == q.Get("fingerprint")) {
				out = append(out, k)
			}
		}
		send(map[string]any{"ssh_keys": out, "meta": page})
	case p == "/ssh_keys" && r.Method == "POST":
		var k Key
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &k)
		if k.Name == "" || k.PublicKey == "" {
			fail(422, "invalid_input", "invalid input in fields 'name', 'public_key'")
			return
		}
		k.Fingerprint = Fingerprint(k.PublicKey)
		for _, e := range a.Keys {
			if e.Fingerprint == k.Fingerprint {
				fail(409, "uniqueness_error", "SSH key with the same fingerprint already exists")
				return
			}
			if e.Name == k.Name {
				fail(409, "uniqueness_error", "name is already used")
				return
			}
		}
		k.ID = int64(100 + len(a.Keys))
		a.Keys = append(a.Keys, k)
		w.WriteHeader(201)
		send(map[string]any{"ssh_key": k})
	case strings.HasPrefix(p, "/ssh_keys/") && r.Method == "DELETE":
		for i, k := range a.Keys {
			if p == fmt.Sprintf("/ssh_keys/%d", k.ID) {
				a.Keys = append(a.Keys[:i], a.Keys[i+1:]...)
				w.WriteHeader(204)
				return
			}
		}
		fail(404, "not_found", "ssh_key not found")
	default:
		fail(404, "not_found", "the fake has no "+r.Method+" "+p)
	}
}

// project answers the calls on servers, firewalls, snapshots, actions and
// prices, and reports whether the request was one of them.
func (a *API) project(w http.ResponseWriter, r *http.Request, body map[string]any, fail func(int, string, string), send func(any), page map[string]any) bool {
	q, p := r.URL.Query(), r.URL.Path
	rest := func(prefix string) (id, act string) {
		id, act, _ = strings.Cut(strings.TrimPrefix(p, prefix), "/actions/")
		return id, act
	}
	broken := func(call string) bool {
		if a.Fail[call] {
			fail(500, "server_error", "the fake was told to fail "+call)
			return true
		}
		return false
	}
	switch {
	case p == "/pricing":
		send(map[string]any{"pricing": map[string]any{"currency": "EUR", "vat_rate": "19.00",
			"image": map[string]any{"price_per_gb_month": map[string]string{"net": "0.0120", "gross": "0.0143"}}}})
	case strings.HasPrefix(p, "/actions/") && r.Method == "GET":
		id := strings.TrimPrefix(p, "/actions/")
		if a.failed[id] {
			send(map[string]any{"action": map[string]any{"id": 1, "status": "error", "error": map[string]string{"code": "action_failed", "message": "Action failed"}}})
			return true
		}
		send(map[string]any{"action": map[string]any{"id": 1, "status": "success"}})
	case p == "/servers" && r.Method == "POST":
		if broken("create_server") || a.refused("create_server", fmt.Sprint(body["location"]), fail) {
			return true
		}
		a.createServer(body, fail, send)
	case strings.HasPrefix(p, "/servers/") && r.Method == "GET":
		_, s := find(a.Servers, strings.TrimPrefix(p, "/servers/"))
		if s == nil {
			fail(404, "not_found", "server not found")
			return true
		}
		send(map[string]any{"server": s})
	case strings.HasPrefix(p, "/servers/") && r.Method == "DELETE":
		i, s := find(a.Servers, strings.TrimPrefix(p, "/servers/"))
		if s == nil {
			fail(404, "not_found", "server not found")
			return true
		}
		if broken("delete_server") {
			return true
		}
		a.Servers = append(a.Servers[:i], a.Servers[i+1:]...)
		for _, f := range a.Firewalls {
			var keep []any
			for _, t := range applied(f) {
				if fmt.Sprint(t["server"].(map[string]any)["id"]) != fmt.Sprint(s["id"]) {
					keep = append(keep, t)
				}
			}
			f["applied_to"] = keep
		}
		send(map[string]any{"action": a.action(false)})
	case strings.HasPrefix(p, "/servers/") && r.Method == "POST":
		id, act := rest("/servers/")
		_, s := find(a.Servers, id)
		if s == nil {
			fail(404, "not_found", "server not found")
			return true
		}
		if broken(act) {
			return true
		}
		switch act {
		case "shutdown":
			if !a.IgnoresShutdown {
				s["status"] = "off"
			}
		case "poweroff":
			s["status"] = "off"
		case "poweron":
			s["status"] = "running"
		case "change_type":
			t := serverType(fmt.Sprint(body["server_type"]))
			loc, _ := s["location"].(map[string]any)
			if a.refused("change_type", fmt.Sprint(loc["name"]), fail) {
				return true
			}
			switch {
			case s["status"] != "off":
				fail(422, "server_not_stopped", "server must be stopped")
				return true
			case t == nil:
				fail(422, "invalid_input", "invalid input in field 'server_type'")
				return true
			case body["upgrade_disk"] != false:
				s["primary_disk_size"] = t["disk"]
			}
			s["server_type"] = t
		case "create_image":
			status := a.SnapshotStatus
			if status == "" {
				status = "available"
			}
			if a.SnapshotFails {
				status = "creating"
			}
			disk, _ := number(s["primary_disk_size"])
			img := map[string]any{"id": a.newID(), "type": body["type"], "status": status, "description": body["description"],
				"labels": body["labels"], "image_size": 2.5, "disk_size": float64(disk), "architecture": "x86",
				"created":      time.Now().UTC().Format(time.RFC3339),
				"created_from": map[string]any{"id": s["id"], "name": s["name"]}}
			a.Images = append(a.Images, img)
			w.WriteHeader(201)
			send(map[string]any{"image": img, "action": a.action(a.SnapshotFails)})
			return true
		default:
			return false
		}
		w.WriteHeader(201)
		send(map[string]any{"action": a.action(false)})
	case p == "/images" && r.Method == "GET":
		out := []map[string]any{}
		if q.Get("type") == "system" {
			if n := q.Get("name"); n == "ubuntu-24.04" || n == "ubuntu-26.04" {
				out = append(out, map[string]any{"id": 9, "type": "system", "name": n, "architecture": q.Get("architecture")})
			}
		} else {
			for _, img := range a.Images {
				if selected(q.Get("label_selector"), img) {
					out = append(out, img)
				}
			}
		}
		send(map[string]any{"images": out, "meta": page})
	case strings.HasPrefix(p, "/images/") && r.Method == "GET":
		_, img := find(a.Images, strings.TrimPrefix(p, "/images/"))
		if img == nil {
			fail(404, "not_found", "image not found")
			return true
		}
		send(map[string]any{"image": img})
	case strings.HasPrefix(p, "/images/") && r.Method == "DELETE":
		i, img := find(a.Images, strings.TrimPrefix(p, "/images/"))
		if img == nil {
			fail(404, "not_found", "image not found")
			return true
		}
		if broken("delete_image") {
			return true
		}
		a.Images = append(a.Images[:i], a.Images[i+1:]...)
		w.WriteHeader(204)
	case p == "/firewalls" && r.Method == "GET":
		if broken("list_firewalls") {
			return true
		}
		out := []map[string]any{}
		for _, f := range a.Firewalls {
			if (q.Get("name") == "" || f["name"] == q.Get("name")) && selected(q.Get("label_selector"), f) {
				out = append(out, f)
			}
		}
		send(map[string]any{"firewalls": out, "meta": page})
	case p == "/firewalls" && r.Method == "POST":
		f := map[string]any{"id": a.newID(), "name": body["name"], "labels": body["labels"], "rules": body["rules"], "applied_to": []any{}}
		if to, ok := body["apply_to"].([]any); ok {
			f["applied_to"] = to
		}
		a.Firewalls = append(a.Firewalls, f)
		w.WriteHeader(201)
		send(map[string]any{"firewall": f, "actions": []any{a.action(false)}})
	case strings.HasPrefix(p, "/firewalls/") && r.Method == "DELETE":
		i, f := find(a.Firewalls, strings.TrimPrefix(p, "/firewalls/"))
		switch {
		case broken("delete_firewall"):
		case f == nil:
			fail(404, "not_found", "firewall not found")
		case len(applied(f)) > 0:
			fail(422, "resource_in_use", "firewall is still in use")
		default:
			a.Firewalls = append(a.Firewalls[:i], a.Firewalls[i+1:]...)
			w.WriteHeader(204)
		}
	case strings.HasPrefix(p, "/firewalls/") && r.Method == "POST":
		id, act := rest("/firewalls/")
		_, f := find(a.Firewalls, id)
		if f == nil {
			fail(404, "not_found", "firewall not found")
			return true
		}
		switch act {
		case "set_rules":
			f["rules"] = body["rules"]
		case "apply_to_resources":
			to, _ := body["apply_to"].([]any)
			f["applied_to"] = append(appliedAny(f), to...)
		case "remove_from_resources":
			f["applied_to"] = []any{}
		default:
			return false
		}
		w.WriteHeader(201)
		send(map[string]any{"actions": []any{a.action(false)}})
	default:
		return false
	}
	return true
}

func appliedAny(f map[string]any) []any {
	l, _ := f["applied_to"].([]any)
	return l
}

// applied are the server resources a firewall applies to.
func applied(f map[string]any) []map[string]any {
	var out []map[string]any
	for _, t := range appliedAny(f) {
		if m, ok := t.(map[string]any); ok && m["server"] != nil {
			out = append(out, m)
		}
	}
	return out
}

// createServer answers POST /servers: a system image by name, or a
// snapshot by its id, which must be available.
func (a *API) createServer(body map[string]any, fail func(int, string, string), send func(any)) {
	a.Created = append(a.Created, body)
	for _, s := range a.Servers {
		if s["name"] == body["name"] {
			fail(409, "uniqueness_error", "server name is already used")
			return
		}
	}
	t := serverType(fmt.Sprint(body["server_type"]))
	if t == nil {
		fail(422, "invalid_input", "invalid input in field 'server_type'")
		return
	}
	disk := t["disk"]
	// A system image is ordered by name; the server reports it by id.
	image := map[string]any{"id": 1, "name": body["image"]}
	if id, ok := number(body["image"]); ok {
		image = map[string]any{"id": id}
		_, img := find(a.Images, strconv.Itoa(id))
		if img == nil || img["status"] != "available" {
			fail(422, "invalid_input", "image not found or not available")
			return
		}
		if need, _ := img["disk_size"].(float64); float64(t["disk"].(int)) < need {
			fail(422, "invalid_input", "image disk is bigger than server type disk")
			return
		}
	}
	id := a.newID()
	s := map[string]any{"id": id, "name": body["name"], "status": "running", "labels": body["labels"],
		"public_net":  map[string]any{"ipv4": map[string]any{"ip": fmt.Sprintf("198.51.100.%d", id%250+1)}},
		"server_type": t, "primary_disk_size": disk, "image": image,
		"location": map[string]any{"name": body["location"]}}
	a.Servers = append(a.Servers, s)
	if fws, ok := body["firewalls"].([]any); ok {
		for _, fw := range fws {
			if _, f := find(a.Firewalls, fmt.Sprint(fw.(map[string]any)["firewall"])); f != nil {
				f["applied_to"] = append(appliedAny(f), map[string]any{"type": "server", "server": map[string]any{"id": id}})
			}
		}
	}
	send(map[string]any{"server": s, "action": a.action(false)})
}

// Calls returns the requests made so far.
func (a *API) Calls() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.Requests...)
}
