package hetzner

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/provider"
)

const token = "test-token-DO-NOT-LEAK"

type api struct {
	mu        sync.Mutex
	servers   map[int64]map[string]any
	created   []CreateServerRequest
	deleted   []int64
	nextID    int64
	firewalls map[int64]*Firewall
	fwDeleted []int64
	// inUse makes the next firewall delete answer resource_in_use, as
	// Hetzner does shortly after a server is deleted.
	inUse int
}

func matchLabels(sel string, labels map[string]string) bool {
	for _, kv := range strings.Split(sel, ",") {
		k, v, _ := strings.Cut(kv, "=")
		if k != "" && labels[k] != v {
			return false
		}
	}
	return true
}

func (a *api) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		defer a.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(401)
			_, _ = io.WriteString(w, `{"error":{"code":"unauthorized","message":"unable to authenticate"}}`)
			return
		}
		send := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		p := r.URL.Path
		switch {
		case p == "/server_types":
			send(map[string]any{"server_types": []map[string]any{
				{"id": 1, "name": "cx23", "cpu_type": "shared", "architecture": "x86", "prices": []map[string]any{{"location": "fsn1", "price_hourly": map[string]string{"gross": "0.0060"}}, {"location": "ash", "price_hourly": map[string]string{"gross": "0.0050"}}}},
				{"id": 2, "name": "cax11", "cpu_type": "shared", "architecture": "arm", "prices": []map[string]any{{"location": "fsn1", "price_hourly": map[string]string{"gross": "0.0040"}}}},
				{"id": 3, "name": "ccx13", "cpu_type": "dedicated", "architecture": "x86", "prices": []map[string]any{{"location": "fsn1", "price_hourly": map[string]string{"gross": "0.0200"}}}},
				{"id": 4, "name": "cx11", "deprecated": true, "cpu_type": "shared", "architecture": "x86", "prices": []map[string]any{{"location": "fsn1", "price_hourly": map[string]string{"gross": "0.0010"}}}},
			}, "meta": map[string]any{"pagination": map[string]any{"next_page": nil}}})
		case p == "/images":
			if r.URL.Query().Get("name") == "ubuntu-26.04" {
				send(map[string]any{"images": []map[string]any{{"id": 9, "name": "ubuntu-26.04", "architecture": r.URL.Query().Get("architecture")}}})
			} else {
				send(map[string]any{"images": []any{}})
			}
		case p == "/ssh_keys":
			if r.URL.Query().Get("name") == "laptop" {
				send(map[string]any{"ssh_keys": []map[string]any{{"id": 7, "name": "laptop"}}})
			} else {
				send(map[string]any{"ssh_keys": []any{}})
			}
		case p == "/servers" && r.Method == "GET":
			var out []any
			for _, s := range a.servers {
				if n := r.URL.Query().Get("name"); n == "" || s["name"] == n {
					out = append(out, s)
				}
			}
			send(map[string]any{"servers": out, "meta": map[string]any{"pagination": map[string]any{"next_page": nil}}})
		case p == "/servers" && r.Method == "POST":
			var req CreateServerRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			a.created = append(a.created, req)
			a.nextID++
			s := map[string]any{"id": a.nextID, "name": req.Name, "status": "running", "labels": req.Labels,
				"public_net": map[string]any{"ipv4": map[string]string{"ip": "192.0.2.10"}}}
			a.servers[a.nextID] = s
			send(map[string]any{"server": s, "action": map[string]any{"id": 100, "status": "running"}})
		case strings.HasPrefix(p, "/servers/") && r.Method == "GET":
			for _, s := range a.servers {
				send(map[string]any{"server": s})
				return
			}
		case strings.HasPrefix(p, "/servers/") && r.Method == "DELETE":
			var id int64
			_ = json.Unmarshal([]byte(strings.TrimPrefix(p, "/servers/")), &id)
			a.deleted = append(a.deleted, id)
			delete(a.servers, id)
			for _, f := range a.firewalls {
				var keep []FirewallResource
				for _, r := range f.AppliedTo {
					if r.Server == nil || r.Server.ID != id {
						keep = append(keep, r)
					}
				}
				f.AppliedTo = keep
			}
			send(map[string]any{"action": map[string]any{"id": 101}})
		case strings.HasPrefix(p, "/actions/"):
			send(map[string]any{"action": map[string]any{"status": "success"}})
		case p == "/firewalls" && r.Method == "GET":
			out := []*Firewall{}
			for _, f := range a.firewalls {
				q := r.URL.Query()
				if (q.Get("name") == "" || f.Name == q.Get("name")) && matchLabels(q.Get("label_selector"), f.Labels) {
					out = append(out, f)
				}
			}
			send(map[string]any{"firewalls": out, "meta": map[string]any{"pagination": map[string]any{"next_page": nil}}})
		case p == "/firewalls" && r.Method == "POST":
			var req struct {
				Name    string             `json:"name"`
				Labels  map[string]string  `json:"labels"`
				Rules   []FirewallRule     `json:"rules"`
				ApplyTo []FirewallResource `json:"apply_to"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			a.nextID++
			f := &Firewall{ID: 1000 + a.nextID, Name: req.Name, Labels: req.Labels, Rules: req.Rules, AppliedTo: req.ApplyTo}
			a.firewalls[f.ID] = f
			send(map[string]any{"firewall": f, "actions": []map[string]any{{"id": 200}}})
		case strings.HasPrefix(p, "/firewalls/"):
			var id int64
			rest := strings.TrimPrefix(p, "/firewalls/")
			idStr, action, _ := strings.Cut(rest, "/actions/")
			_ = json.Unmarshal([]byte(idStr), &id)
			f := a.firewalls[id]
			if f == nil {
				w.WriteHeader(404)
				_, _ = io.WriteString(w, `{"error":{"code":"not_found","message":"firewall not found"}}`)
				return
			}
			var body struct {
				Rules      []FirewallRule     `json:"rules"`
				ApplyTo    []FirewallResource `json:"apply_to"`
				RemoveFrom []FirewallResource `json:"remove_from"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			switch {
			case r.Method == "DELETE":
				if a.inUse > 0 || len(f.AppliedTo) > 0 {
					a.inUse--
					w.WriteHeader(409)
					_, _ = io.WriteString(w, `{"error":{"code":"resource_in_use","message":"firewall in use"}}`)
					return
				}
				delete(a.firewalls, id)
				a.fwDeleted = append(a.fwDeleted, id)
				w.WriteHeader(204)
				return
			case action == "set_rules":
				f.Rules = body.Rules
			case action == "apply_to_resources":
				f.AppliedTo = append(f.AppliedTo, body.ApplyTo...)
			case action == "remove_from_resources":
				f.AppliedTo = nil
			}
			send(map[string]any{"actions": []map[string]any{{"id": 201}}})
		default:
			t.Errorf("unexpected %s %s", r.Method, p)
			w.WriteHeader(404)
		}
	})
}

func setup(t *testing.T) (*Client, *api) {
	a := &api{servers: map[int64]map[string]any{}, firewalls: map[int64]*Firewall{}}
	srv := httptest.NewServer(a.handler(t))
	t.Cleanup(srv.Close)
	c := New(token)
	c.Endpoint = srv.URL
	return c, a
}

func boxConfig(t *testing.T, image string) *config.Config {
	t.Helper()
	c, err := config.Parse([]byte(`version: 1
box: acme
target:
  provider: hetzner
  hetzner: {server_type: cx23, location: fsn1, image: `+image+`, ssh_keys: [laptop], labels: {keelbox-e2e: "true"}}
users:
  admin:
    ssh_authorized_keys: ["ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKey you@laptop"]
`), "t")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCreateFindDestroy(t *testing.T) {
	cl, a := setup(t)
	ctx := context.Background()
	cfg := boxConfig(t, "ubuntu-26.04")
	s, err := Create(ctx, cl, cfg, testHostKey, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	if s.PublicNet.IPv4.IP != "192.0.2.10" {
		t.Errorf("ip %s", s.PublicNet.IPv4.IP)
	}
	req := a.created[0]
	if req.Labels[LabelManaged] != "true" || req.Labels[LabelBox] != "acme" || req.Labels["keelbox-e2e"] != "true" {
		t.Errorf("labels %v", req.Labels)
	}
	if len(req.SSHKeys) != 1 || req.SSHKeys[0] != 7 || !strings.HasPrefix(req.UserData, "#cloud-config") {
		t.Errorf("request %+v", req)
	}
	if _, err := Create(ctx, cl, cfg, testHostKey, t.Logf); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("duplicate create: %v", err)
	}
	found, err := Find(ctx, cl, "acme")
	if err != nil {
		t.Fatal(err)
	}
	if err := Destroy(ctx, cl, found); err != nil {
		t.Fatal(err)
	}
	if len(a.deleted) != 1 {
		t.Error("not deleted")
	}
}

var testHostKey = HostKey{
	Private: "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAA\n-----END OPENSSH PRIVATE KEY-----\n",
	Public:  "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHost acme\n",
}

func TestUserDataInstallsTheLaptopHostKey(t *testing.T) {
	c := boxConfig(t, "ubuntu-26.04")
	ud, err := provider.UserData(c.Box, c.Host.Hostname, testHostKey)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Hostname       string            `yaml:"hostname"`
		SSHDeleteKeys  bool              `yaml:"ssh_deletekeys"`
		SSHGenKeyTypes []string          `yaml:"ssh_genkeytypes"`
		SSHKeys        map[string]string `yaml:"ssh_keys"`
	}
	if !strings.HasPrefix(ud, "#cloud-config\n") {
		t.Fatalf("not cloud-config:\n%s", ud)
	}
	if err := yaml.Unmarshal([]byte(ud), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Hostname != "acme" || !doc.SSHDeleteKeys || doc.SSHGenKeyTypes == nil || len(doc.SSHGenKeyTypes) != 0 {
		t.Errorf("doc %+v", doc)
	}
	if doc.SSHKeys["ed25519_private"] != testHostKey.Private || doc.SSHKeys["ed25519_public"] != "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHost acme" {
		t.Errorf("keys %v", doc.SSHKeys)
	}
}

func TestNeverTouchesUnmanagedServers(t *testing.T) {
	cl, a := setup(t)
	a.servers[5] = map[string]any{"id": 5, "name": "acme", "labels": map[string]string{}}
	if _, err := Find(context.Background(), cl, "acme"); err == nil || !strings.Contains(err.Error(), "not created by keelbox") {
		t.Fatalf("got %v", err)
	}
	if err := Destroy(context.Background(), cl, &Server{ID: 5}); err == nil {
		t.Fatal("deleted an unlabelled server")
	}
	if len(a.deleted) != 0 {
		t.Error("delete sent")
	}
}

func TestCreateValidatesAgainstAPI(t *testing.T) {
	cl, _ := setup(t)
	if _, err := Create(context.Background(), cl, boxConfig(t, "ubuntu-20.04"), testHostKey, t.Logf); err == nil || !strings.Contains(err.Error(), "image") {
		t.Errorf("missing image: %v", err)
	}
	c := boxConfig(t, "ubuntu-26.04")
	c.Target.Hetzner.SSHKeys = []string{"nope"}
	if _, err := Create(context.Background(), cl, c, testHostKey, t.Logf); err == nil || !strings.Contains(err.Error(), `no key named "nope"`) {
		t.Errorf("missing key: %v", err)
	}
}

func TestTokenNeverInErrors(t *testing.T) {
	cl, _ := setup(t)
	bad := New("wrong-" + token)
	bad.Endpoint = cl.Endpoint
	_, err := bad.ServerTypes(context.Background())
	if err == nil || strings.Contains(err.Error(), token) || !strings.Contains(err.Error(), "401") {
		t.Fatalf("got %v", err)
	}
}

func TestCheapestShared(t *testing.T) {
	cl, _ := setup(t)
	types, err := cl.ServerTypes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	st, loc, price, err := CheapestShared(types, "x86", []string{"fsn1", "nbg1"})
	if err != nil || st.Name != "cx23" || loc != "fsn1" || price != 0.006 {
		t.Errorf("got %s %s %v %v", st.Name, loc, price, err)
	}
}

func TestTokenFromFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "tok")
	if err := os.WriteFile(f, []byte(token+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := TokenFromFile(f); err == nil || strings.Contains(err.Error(), token) {
		t.Errorf("world-readable token accepted or leaked: %v", err)
	}
	_ = os.Chmod(f, 0o600)
	if got, err := TokenFromFile(f); err != nil || got != token {
		t.Errorf("got %q %v", got, err)
	}
}

func TestTypesNoLongerSoldInALocationAreSkipped(t *testing.T) {
	price := func(loc, gross string) Price {
		p := Price{Location: loc}
		p.PriceHourly.Gross = gross
		return p
	}
	old := ServerType{Name: "cpx11", CPUType: "shared", Architecture: "x86",
		Prices:    []Price{price("fsn1", "0.0088"), price("ash", "0.0088")},
		Locations: []TypeLocation{{Name: "fsn1", Available: false}, {Name: "ash", Available: true}}}
	cur := ServerType{Name: "cpx12", CPUType: "shared", Architecture: "x86",
		Prices:    []Price{price("fsn1", "0.0100")},
		Locations: []TypeLocation{{Name: "fsn1", Available: true}}}
	st, loc, _, err := CheapestShared([]ServerType{old, cur}, "x86", []string{"fsn1"})
	if err != nil || st.Name != "cpx12" || loc != "fsn1" {
		t.Errorf("got %s %s %v", st.Name, loc, err)
	}
	if got := strings.Join(old.OrderableIn(), ","); got != "ash" {
		t.Errorf("orderable in %s", got)
	}
}
