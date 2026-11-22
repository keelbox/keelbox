package digitalocean

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
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// The contract with the real doctl (docs/contributing/testing.md): every
// command keelbox issues goes through the doctl binary named by
// KEELBOX_TEST_DOCTL, pointed at a stub of the DigitalOcean API on
// 127.0.0.1. doctl checks commands and flags, builds the requests and
// prints the answers itself, so this checks what the fake in dotest only
// imitates. It needs no account.

// apiStub is a small in-memory DigitalOcean API.
type apiStub struct {
	mu        sync.Mutex
	t         *testing.T
	token     string
	next      int64
	droplets  []map[string]any
	firewalls []map[string]any
	tags      map[string]bool
	keys      []map[string]any
	// images are the account's own images, and actions what was asked of
	// its Droplets, by id. gone are the deleted images that still carry
	// tags: a tag counts them until it is taken off them.
	images  []map[string]any
	gone    []map[string]any
	actions map[string]map[string]any
	// bodies are the JSON bodies received, by "METHOD /path".
	bodies map[string][]map[string]any
	// pageSize, when set, makes list answers that long at most, with the
	// links doctl follows.
	pageSize int
	// forbid answers 403 for a "METHOD /path".
	forbid map[string]bool
}

func (s *apiStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	answer := func(status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if v != nil {
			_ = json.NewEncoder(w).Encode(v)
		}
	}
	fail := func(status int, id, msg string) { answer(status, map[string]any{"id": id, "message": msg}) }
	if r.Header.Get("Authorization") != "Bearer "+s.token {
		fail(401, "Unauthorized", "Unable to authenticate you")
		return
	}
	key := r.Method + " " + r.URL.Path
	if s.forbid[key] {
		fail(403, "Forbidden", "You are not authorized to perform this operation")
		return
	}
	var body map[string]any
	if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			s.t.Errorf("%s: body is not JSON: %s", key, raw)
		}
		s.bodies[key] = append(s.bodies[key], body)
	}
	list := func(name string, items []map[string]any) {
		if items == nil {
			items = []map[string]any{}
		}
		out := map[string]any{"meta": map[string]any{"total": len(items)}, "links": map[string]any{}}
		if n := s.pageSize; n > 0 && len(items) > n {
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			if page < 1 {
				page = 1
			}
			last := (len(items) + n - 1) / n
			url := func(p int) string {
				q := r.URL.Query()
				q.Set("page", strconv.Itoa(p))
				return "http://" + r.Host + r.URL.Path + "?" + q.Encode()
			}
			pages := map[string]any{}
			if page < last {
				pages["next"], pages["last"] = url(page+1), url(last)
			}
			if page > 1 {
				pages["prev"], pages["first"] = url(page-1), url(1)
			}
			out["links"] = map[string]any{"pages": pages}
			items = items[(page-1)*n : min(page*n, len(items))]
		}
		out[name] = items
		answer(200, out)
	}
	missing := func() { fail(404, "not_found", "The resource you were accessing could not be found.") }
	id := filepath.Base(r.URL.Path)
	find := func(items []map[string]any) int {
		for i, it := range items {
			if fmt.Sprint(it["id"]) == id {
				return i
			}
		}
		return -1
	}
	fra := []string{"fra1"}
	switch {
	case key == "GET /v2/account":
		answer(200, map[string]any{"account": map[string]any{"email": "sam@example.com", "uuid": "a1b2c3", "status": "active", "droplet_limit": 10,
			"email_verified": true, "team": map[string]any{"name": "Sam's team", "uuid": "t1"}}})
	case key == "GET /v2/regions":
		list("regions", []map[string]any{{"slug": "fra1", "name": "Frankfurt 1", "available": true, "sizes": []string{"s-2vcpu-4gb"}, "features": []string{"metadata"}},
			{"slug": "nyc2", "name": "New York 2", "available": false, "sizes": []string{}}})
	case key == "GET /v2/sizes":
		list("sizes", []map[string]any{{"slug": "s-2vcpu-4gb", "memory": 4096, "vcpus": 2, "disk": 80, "transfer": 4.0, "price_monthly": 24.0,
			"price_hourly": 0.03571, "regions": fra, "available": true, "description": "Basic"},
			{"slug": "s-4vcpu-8gb", "memory": 8192, "vcpus": 4, "disk": 160, "transfer": 5.0, "price_monthly": 48.0,
				"price_hourly": 0.07143, "regions": fra, "available": true, "description": "Basic"}})
	case key == "GET /v2/images" && r.URL.Query().Get("private") == "true":
		list("images", s.images)
	case key == "GET /v2/images":
		if r.URL.Query().Get("type") != "distribution" {
			s.t.Errorf("image list without type=distribution: %s", r.URL.RawQuery)
		}
		list("images", []map[string]any{{"id": 101, "slug": "ubuntu-24-04-x64", "distribution": "Ubuntu", "name": "24.04 (LTS) x64", "public": true, "regions": fra, "status": "available"}})
	case key == "GET /v2/images/ubuntu-24-04-x64":
		answer(200, map[string]any{"image": map[string]any{"id": 101, "slug": "ubuntu-24-04-x64", "distribution": "Ubuntu", "name": "24.04 (LTS) x64", "public": true, "regions": fra, "status": "available"}})
	case strings.HasPrefix(r.URL.Path, "/v2/images/"):
		i := find(s.images)
		if i < 0 {
			missing()
			return
		}
		if r.Method == http.MethodDelete {
			s.gone = append(s.gone, s.images[i])
			s.images = append(s.images[:i], s.images[i+1:]...)
			answer(204, nil)
			return
		}
		answer(200, map[string]any{"image": s.images[i]})
	case key == "GET /v2/account/keys":
		list("ssh_keys", s.keys)
	case key == "POST /v2/tags":
		s.tags[body["name"].(string)] = true
		answer(201, map[string]any{"tag": map[string]any{"name": body["name"], "resources": map[string]any{"count": 0}}})
	case (r.Method == http.MethodPost || r.Method == http.MethodDelete) && strings.HasPrefix(r.URL.Path, "/v2/tags/") && id == "resources":
		// A tag is put on an image, or taken off one, which may be gone.
		tag := filepath.Base(filepath.Dir(r.URL.Path))
		if !s.tags[tag] {
			missing()
			return
		}
		for _, res := range body["resources"].([]any) {
			m := res.(map[string]any)
			if m["resource_type"] != "image" {
				s.t.Errorf("tagged a %v", m["resource_type"])
			}
			for _, img := range append(append([]map[string]any{}, s.images...), s.gone...) {
				if fmt.Sprint(img["id"]) != fmt.Sprint(m["resource_id"]) {
					continue
				}
				var tags []any
				for _, t := range img["tags"].([]any) {
					if t != tag {
						tags = append(tags, t)
					}
				}
				if r.Method == http.MethodPost {
					tags = append(tags, tag)
				}
				img["tags"] = tags
			}
		}
		answer(204, nil)
	case strings.HasPrefix(r.URL.Path, "/v2/tags/"):
		if !s.tags[id] {
			missing()
			return
		}
		if r.Method == http.MethodDelete {
			delete(s.tags, id)
			answer(204, nil)
			return
		}
		n, images := 0, 0
		for _, d := range s.droplets {
			for _, t := range d["tags"].([]any) {
				if t == id {
					n++
				}
			}
		}
		for _, img := range append(append([]map[string]any{}, s.images...), s.gone...) {
			tags, _ := img["tags"].([]any)
			for _, t := range tags {
				if t == id {
					images++
				}
			}
		}
		answer(200, map[string]any{"tag": map[string]any{"name": id, "resources": map[string]any{"count": n + images, "droplets": map[string]any{"count": n}, "images": map[string]any{"count": images}}}})
	case key == "GET /v2/firewalls":
		list("firewalls", s.firewalls)
	case key == "POST /v2/firewalls", r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/v2/firewalls/"):
		for _, t := range body["tags"].([]any) {
			if !s.tags[t.(string)] {
				fail(422, "unprocessable_entity", "tag does not exist")
				return
			}
		}
		// As the API answers: every port is "0", and ICMP has ports "0".
		for _, dir := range []string{"inbound_rules", "outbound_rules"} {
			for _, rule := range body[dir].([]any) {
				m := rule.(map[string]any)
				if p, _ := m["ports"].(string); p == "" || p == "all" {
					m["ports"] = "0"
				}
			}
		}
		body["status"], body["pending_changes"] = "succeeded", []any{}
		if r.Method == http.MethodPost {
			s.next++
			body["id"] = "fw-" + strconv.FormatInt(s.next, 10)
			s.firewalls = append(s.firewalls, body)
			answer(202, map[string]any{"firewall": body})
			return
		}
		i := find(s.firewalls)
		if i < 0 {
			missing()
			return
		}
		body["id"] = id
		s.firewalls[i] = body
		answer(200, map[string]any{"firewall": body})
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v2/firewalls/") && id == "droplets":
		// A Droplet is named in a firewall.
		id = filepath.Base(filepath.Dir(r.URL.Path))
		i := find(s.firewalls)
		if i < 0 {
			missing()
			return
		}
		ids, _ := s.firewalls[i]["droplet_ids"].([]any)
		s.firewalls[i]["droplet_ids"] = append(ids, body["droplet_ids"].([]any)...)
		answer(204, nil)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v2/firewalls/"):
		i := find(s.firewalls)
		if i < 0 {
			missing()
			return
		}
		answer(200, map[string]any{"firewall": s.firewalls[i]})
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v2/firewalls/"):
		i := find(s.firewalls)
		if i < 0 {
			missing()
			return
		}
		s.firewalls = append(s.firewalls[:i], s.firewalls[i+1:]...)
		answer(204, nil)
	case key == "GET /v2/droplets":
		var out []map[string]any
		for _, d := range s.droplets {
			keep := r.URL.Query().Get("tag_name") == ""
			for _, t := range d["tags"].([]any) {
				keep = keep || t == r.URL.Query().Get("tag_name")
			}
			if keep {
				out = append(out, d)
			}
		}
		list("droplets", out)
	case key == "POST /v2/droplets":
		s.next++
		for _, t := range body["tags"].([]any) {
			s.tags[t.(string)] = true
		}
		// A snapshot is named by its id and has no slug.
		slug, _ := body["image"].(string)
		d := map[string]any{"id": s.next, "name": body["name"], "status": "new", "size_slug": body["size"], "disk": 80, "tags": body["tags"],
			"region":   map[string]any{"slug": body["region"], "name": "Frankfurt 1"},
			"image":    map[string]any{"id": 101, "slug": slug, "distribution": "Ubuntu", "name": "24.04 (LTS) x64"},
			"networks": map[string]any{"v4": []any{}, "v6": []any{}}}
		s.droplets = append(s.droplets, d)
		answer(202, map[string]any{"droplet": d, "links": map[string]any{}})
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v2/droplets/") && id == "actions":
		// An action is in progress when the API answers, and what it does
		// is done by the first time it is asked about.
		id = filepath.Base(filepath.Dir(r.URL.Path))
		i := find(s.droplets)
		if i < 0 {
			missing()
			return
		}
		d := s.droplets[i]
		s.next++
		act := map[string]any{"id": s.next, "status": "in-progress", "type": body["type"], "resource_id": d["id"], "resource_type": "droplet", "region_slug": "fra1"}
		switch body["type"] {
		case "shutdown", "power_off":
			d["status"] = "off"
		case "power_on":
			d["status"] = "active"
		case "resize":
			d["size_slug"] = body["size"]
		case "snapshot":
			s.next++
			s.images = append(s.images, map[string]any{"id": s.next, "name": body["name"], "type": "snapshot", "distribution": "Ubuntu", "regions": fra,
				"min_disk_size": d["disk"], "size_gigabytes": 2.5, "created_at": "2026-11-14T12:00:00Z", "tags": []any{}, "status": "available"})
		default:
			s.t.Errorf("the stub has no action %v", body["type"])
		}
		s.actions[fmt.Sprint(act["id"])] = act
		answer(201, map[string]any{"action": act})
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v2/droplets/") && s.actions[id] != nil && strings.HasSuffix(filepath.Dir(r.URL.Path), "/actions"):
		s.actions[id]["status"] = "completed"
		answer(200, map[string]any{"action": s.actions[id]})
	case strings.HasPrefix(r.URL.Path, "/v2/droplets/"):
		i := find(s.droplets)
		if i < 0 {
			missing()
			return
		}
		if r.Method == http.MethodDelete {
			s.droplets = append(s.droplets[:i], s.droplets[i+1:]...)
			answer(204, nil)
			return
		}
		// Active, with its addresses, from the first poll on; a Droplet
		// that is off keeps them.
		d := s.droplets[i]
		if d["status"] == "new" {
			d["status"] = "active"
		}
		d["networks"] = map[string]any{"v4": []any{
			map[string]any{"ip_address": "10.114.0.2", "netmask": "255.255.240.0", "gateway": "10.114.0.1", "type": "private"},
			map[string]any{"ip_address": "203.0.113.7", "netmask": "255.255.240.0", "gateway": "203.0.113.1", "type": "public"}}, "v6": []any{}}
		answer(200, map[string]any{"droplet": d})
	default:
		s.t.Errorf("the stub has no answer for %s?%s", key, r.URL.RawQuery)
		missing()
	}
}

// realDoctl starts the stub and returns a client that runs the real
// doctl against it. The token reaches doctl through the environment,
// which doctl reads for its default context; it is on no command line.
func realDoctl(t *testing.T) (*apiStub, *Client) {
	t.Helper()
	bin := os.Getenv("KEELBOX_TEST_DOCTL")
	if bin == "" {
		t.Skip("set KEELBOX_TEST_DOCTL to a doctl binary to check keelbox against the real CLI (docs/contributing/testing.md)")
	}
	old := poll
	poll = time.Millisecond
	t.Cleanup(func() { poll = old })
	s := &apiStub{t: t, token: "stub-token", next: 4241, tags: map[string]bool{}, bodies: map[string][]map[string]any{}, forbid: map[string]bool{},
		actions: map[string]map[string]any{}}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	// doctl's own config lives under a directory of the test's.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("DIGITALOCEAN_API_URL", srv.URL)
	t.Setenv("DIGITALOCEAN_ACCESS_TOKEN", s.token)
	t.Setenv("DIGITALOCEAN_CONTEXT", "")
	return s, &Client{Run: Exec{Path: bin}}
}

func TestRealCLICreateFindDestroy(t *testing.T) {
	s, cl := realDoctl(t)
	ctx := context.Background()
	c := boxConfig(t, ", tags: [\"team:web\"]")
	s.keys = []map[string]any{{"id": 9, "name": "laptop", "fingerprint": "aa:bb", "public_key": adminKey}}

	acct, err := cl.Preflight(ctx, "fra1", "s-2vcpu-4gb", "ubuntu-24-04-x64")
	if err != nil || acct.Describe() != "team Sam's team as sam@example.com" {
		t.Fatalf("preflight: %+v %v", acct, err)
	}
	if _, err := cl.Preflight(ctx, "nyc2", "", ""); err == nil || !strings.Contains(err.Error(), "set region to one of: fra1") {
		t.Errorf("unavailable region: %v", err)
	}
	d, err := Create(ctx, cl, c, testHostKey, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	if d.Name != "acme" || d.Status != "active" || d.PublicIPv4() != "203.0.113.7" || !d.Managed("acme") || d.Region.Slug != "fra1" {
		t.Errorf("droplet %+v", d)
	}
	// What doctl sent for the Droplet: the user data read from the file,
	// the tags as a list, the account key that is the admin's.
	sent := s.bodies["POST /v2/droplets"]
	if len(sent) != 1 {
		t.Fatalf("%d droplet creates", len(sent))
	}
	b := sent[0]
	if ud, _ := b["user_data"].(string); !strings.HasPrefix(ud, "#cloud-config\n") || !strings.Contains(ud, "DO-NOT-LEAK") {
		t.Errorf("user data: %q", b["user_data"])
	}
	if fmt.Sprint(b["tags"]) != "[keelbox keelbox-box-acme team:web]" || fmt.Sprint(b["ssh_keys"]) != "[9]" ||
		b["name"] != "acme" || b["region"] != "fra1" || b["size"] != "s-2vcpu-4gb" || b["image"] != "ubuntu-24-04-x64" {
		t.Errorf("create body %v", b)
	}
	// And for the firewall: rules as structures, applied to the tag.
	fw := s.bodies["POST /v2/firewalls"]
	if len(fw) != 1 {
		t.Fatalf("%d firewall creates", len(fw))
	}
	raw, _ := json.Marshal(fw[0])
	var sentFW Firewall
	if err := json.Unmarshal(raw, &sentFW); err != nil {
		t.Fatal(err)
	}
	if sentFW.Name != "keelbox-acme" || !sentFW.Targets("keelbox-box-acme") || len(sentFW.DropletIDs) != 0 ||
		!SameRules(sentFW.Inbound, InboundRules(true)) || !SameRules(sentFW.Outbound, OutboundRules()) {
		t.Errorf("firewall body %s", raw)
	}
	for _, r := range sentFW.Inbound {
		if strings.Join(r.Sources.Addresses, " ") != "0.0.0.0/0 ::/0" {
			t.Errorf("rule sources %+v", r.Sources)
		}
	}
	got, err := Find(ctx, cl, "acme")
	if err != nil || got.ID != d.ID {
		t.Fatalf("find %+v %v", got, err)
	}
	// The firewall as doctl prints it, then closed to SSH by an update
	// that keeps its tag.
	no := false
	f, changes, err := EnsureFirewall(ctx, cl, "acme", &no)
	if err != nil || len(changes) != 1 || HasPublicSSH(f) {
		t.Fatalf("%+v %q %v", f, changes, err)
	}
	f, changes, err = EnsureFirewall(ctx, cl, "acme", nil)
	if err != nil || len(changes) != 0 || HasPublicSSH(f) || !f.Targets("keelbox-box-acme") || !SameRules(f.Outbound, OutboundRules()) {
		t.Fatalf("after the update: %+v %q %v", f, changes, err)
	}
	if _, err := Create(ctx, cl, c, testHostKey, t.Logf); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("second create: %v", err)
	}
	done, err := Destroy(ctx, cl, got, "acme")
	if err != nil || len(done) != 2 {
		t.Fatalf("destroy %q %v", done, err)
	}
	if len(s.droplets) != 0 || len(s.firewalls) != 0 || s.tags["keelbox"] || s.tags["keelbox-box-acme"] || !s.tags["team:web"] {
		t.Errorf("left: %v %v %v", s.droplets, s.firewalls, s.tags)
	}
	if _, err := Find(ctx, cl, "acme"); err == nil || !strings.Contains(err.Error(), "keelbox up acme") {
		t.Errorf("find after destroy: %v", err)
	}
}

func TestRealCLIErrors(t *testing.T) {
	s, cl := realDoctl(t)
	ctx := context.Background()
	// A resource that is not there: the API's 404, as doctl prints it.
	if d, err := cl.Droplet(ctx, 9); d != nil || err != nil {
		t.Errorf("a missing Droplet: %+v %v", d, err)
	}
	if tag, err := cl.tag(ctx, "nope"); tag != nil || err != nil {
		t.Errorf("a missing tag: %+v %v", tag, err)
	}
	err := cl.compute(ctx, nil, "firewall", "delete", "fw-0", "--force")
	var de *Error
	if !errors.As(err, &de) || de.Status != 404 || de.Title != "Not Found" || de.Message != "The resource you were accessing could not be found." {
		t.Errorf("404: %+v", err)
	}
	// A token without the scope.
	s.forbid["GET /v2/account"] = true
	if _, err := cl.Account(ctx); err == nil || errors.Is(err, ErrNotSignedIn) || !strings.Contains(err.Error(), "account:read") {
		t.Errorf("403: %v", err)
	}
	s.forbid = map[string]bool{}
	// A token the API refuses.
	s.token = "another"
	if _, err := cl.Account(ctx); !errors.Is(err, ErrNotSignedIn) || !strings.Contains(err.Error(), "refused the token") {
		t.Errorf("401: %v", err)
	}
	// No token at all, and a context doctl does not have.
	t.Setenv("DIGITALOCEAN_ACCESS_TOKEN", "")
	if _, err := cl.Account(ctx); !errors.Is(err, ErrNotSignedIn) || !strings.Contains(err.Error(), "has no token") {
		t.Errorf("no token: %v", err)
	}
	cl.Context = "work"
	if _, err := cl.Account(ctx); !errors.Is(err, ErrNotSignedIn) || !strings.Contains(err.Error(), "doctl auth init --context work") {
		t.Errorf("unknown context: %v", err)
	}
	// A flag doctl does not know fails before any request, in plain text.
	err = cl.compute(ctx, nil, "droplet", "list", "--no-such-flag")
	if !errors.As(err, &de) || de.Status != 0 || !strings.Contains(de.Message, "unknown flag: --no-such-flag") {
		t.Errorf("unknown flag: %+v", err)
	}
}

// --context selects a token doctl has saved; the environment's token is
// for the default context only.
func TestRealCLIContext(t *testing.T) {
	s, cl := realDoctl(t)
	dir, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "doctl"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := "access-token: default-token\ncontext: default\nauth-contexts:\n  work: " + s.token + "\n"
	if err := os.WriteFile(filepath.Join(dir, "doctl", "config.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DIGITALOCEAN_ACCESS_TOKEN", "")
	if _, err := cl.Account(context.Background()); !errors.Is(err, ErrNotSignedIn) {
		t.Errorf("the default context's token is not the stub's: %v", err)
	}
	cl.Context = "work"
	if a, err := cl.Account(context.Background()); err != nil || a.Email != "sam@example.com" {
		t.Errorf("context work: %+v %v", a, err)
	}
}

// doctl follows the API's pages itself and prints one list.
func TestRealCLIFollowsPages(t *testing.T) {
	s, cl := realDoctl(t)
	s.pageSize = 1
	tags := []any{"keelbox", "keelbox-box-acme"}
	for i, name := range []string{"web", "db", "acme"} {
		s.droplets = append(s.droplets, map[string]any{"id": 100 + i, "name": name, "status": "active", "tags": tags,
			"networks": map[string]any{"v4": []any{}}})
	}
	ds, err := cl.DropletsTagged(context.Background(), "keelbox-box-acme")
	if err != nil || len(ds) != 3 {
		t.Fatalf("%d droplets, %v", len(ds), err)
	}
	if d, err := Find(context.Background(), cl, "acme"); err != nil || d.ID != 102 {
		t.Errorf("find on the last page: %+v %v", d, err)
	}
	if named, err := cl.DropletsNamed(context.Background(), "db"); err != nil || len(named) != 1 || named[0].ID != 101 {
		t.Errorf("by name: %+v %v", named, err)
	}
}

// Resize, park and resume through the real doctl: the commands and flags
// exist, and each sends the request keelbox means.
func TestRealCLIResizeParkResume(t *testing.T) {
	s, cl := realDoctl(t)
	ctx := context.Background()
	c := boxConfig(t, "")
	created, err := Create(ctx, cl, c, testHostKey, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	d, err := Find(ctx, cl, "acme")
	if err != nil || d.Disk != 80 {
		t.Fatalf("find %+v %v", d, err)
	}
	actions := func() []map[string]any { return s.bodies[fmt.Sprintf("POST /v2/droplets/%d/actions", created.ID)] }
	last := func() string {
		a := actions()
		raw, _ := json.Marshal(a[len(a)-1])
		return string(raw)
	}
	if err := Stop(ctx, cl, "acme", d, t.Logf); err != nil || last() != `{"type":"shutdown"}` {
		t.Fatalf("stop: %v, sent %s", err, last())
	}
	sizes, err := cl.Sizes(ctx)
	if err != nil || len(ResizeOffers(sizes, d)) != 2 {
		t.Fatalf("offers %+v %v", ResizeOffers(sizes, d), err)
	}
	// The resize asks for the size and not for the disk.
	d, err = Resize(ctx, cl, "acme", d, "s-4vcpu-8gb")
	if err != nil || d.SizeSlug != "s-4vcpu-8gb" || d.Disk != 80 || d.Status != "off" || last() != `{"disk":false,"size":"s-4vcpu-8gb","type":"resize"}` {
		t.Fatalf("resize: %+v %v, sent %s", d, err, last())
	}
	if d, err = Start(ctx, cl, "acme", d); err != nil || d.Status != "active" || d.PublicIPv4() != "203.0.113.7" || last() != `{"type":"power_on"}` {
		t.Fatalf("start: %+v %v, sent %s", d, err, last())
	}
	if err := Stop(ctx, cl, "acme", d, t.Logf); err != nil {
		t.Fatal(err)
	}
	// The snapshot is named, then tagged as an image, one tag a call.
	img, err := Snapshot(ctx, cl, c, d)
	if err != nil {
		t.Fatal(err)
	}
	if last() != `{"name":"keelbox-parked-acme","type":"snapshot"}` || !img.Parked("acme") || img.MinDiskSize != 80 || img.SizeGigabytes != 2.5 || img.Created().IsZero() {
		t.Fatalf("snapshot %+v, sent %s", img, last())
	}
	for _, tag := range []string{"keelbox", "keelbox-box-acme", "keelbox-parked"} {
		sent := s.bodies["POST /v2/tags/"+tag+"/resources"]
		raw, _ := json.Marshal(sent)
		if string(raw) != fmt.Sprintf(`[{"resources":[{"resource_id":"%d","resource_type":"image"}]}]`, img.ID) {
			t.Errorf("tag %s: sent %s", tag, raw)
		}
	}
	if err := Release(ctx, cl, d, "acme"); err != nil || len(s.droplets) != 0 || len(s.firewalls) != 1 || !s.tags["keelbox-box-acme"] {
		t.Fatalf("release: %v, left %v %v %v", err, s.droplets, s.firewalls, s.tags)
	}
	p, err := Parked(ctx, cl, "acme")
	if err != nil || p == nil || p.ID != img.ID {
		t.Fatalf("parked %+v %v", p, err)
	}
	got, err := Restore(ctx, cl, c, p, t.Logf)
	if err != nil || got.Status != "active" || !got.Managed("acme") {
		t.Fatalf("restore %+v %v", got, err)
	}
	// What doctl sent for the new Droplet: the snapshot as its image, the
	// box's tags, and user data that keeps what is on the disk.
	sent := s.bodies["POST /v2/droplets"]
	b := sent[len(sent)-1]
	if len(sent) != 2 || fmt.Sprint(b["image"]) != fmt.Sprint(img.ID) || fmt.Sprint(b["tags"]) != "[keelbox keelbox-box-acme]" || b["user_data"] != resumeUserData("acme") ||
		b["size"] != "s-2vcpu-4gb" || b["region"] != "fra1" || b["name"] != "acme" || len(b["ssh_keys"].([]any)) != 0 {
		t.Errorf("create body %v", b)
	}
	// The new Droplet was named in the firewall by id.
	raw, _ := json.Marshal(s.bodies[fmt.Sprintf("POST /v2/firewalls/%v/droplets", s.firewalls[0]["id"])])
	if string(raw) != fmt.Sprintf(`[{"droplet_ids":[%d]}]`, got.ID) {
		t.Errorf("firewall droplets: sent %s", raw)
	}
	// The snapshot goes, and its tags are taken off it, so that the parked
	// marker no longer counts it and goes too; the box's tags stay with its
	// Droplet and firewall.
	if err := DeleteSnapshot(ctx, cl, "acme", p); err != nil || len(s.images) != 0 || s.tags["keelbox-parked"] || !s.tags["keelbox"] || !s.tags["keelbox-box-acme"] {
		t.Fatalf("delete snapshot: %v, left %v %v", err, s.images, s.tags)
	}
	for _, tag := range []string{"keelbox", "keelbox-box-acme", "keelbox-parked"} {
		raw, _ := json.Marshal(s.bodies["DELETE /v2/tags/"+tag+"/resources"])
		if string(raw) != fmt.Sprintf(`[{"resources":[{"resource_id":"%d","resource_type":"image"}]}]`, img.ID) {
			t.Errorf("tag %s taken off: sent %s", tag, raw)
		}
	}
	if err := DeleteSnapshot(ctx, cl, "acme", p); err != nil {
		t.Errorf("a snapshot that is gone: %v", err)
	}
	if gone, err := SweepSnapshots(ctx, cl, "web"); err != nil || len(gone) != 0 {
		t.Errorf("sweep of a box with nothing parked: %v %v", gone, err)
	}
}
