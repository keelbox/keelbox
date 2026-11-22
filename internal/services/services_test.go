package services

import (
	"os"
	"strings"
	"testing"
)

// Every preset is well formed: ports in range, tcp or udp, a use and a
// vendor source, no SSH, and nothing listed twice.
func TestPresets(t *testing.T) {
	names := map[string]bool{}
	for _, s := range All() {
		if names[s.Name] || s.Title == "" || !strings.HasPrefix(s.Source, "https://") || len(s.Ports) == 0 {
			t.Errorf("%s: duplicate, or no title, source or ports", s.Name)
		}
		names[s.Name] = true
		seen := map[string]bool{}
		for _, p := range s.Ports {
			if p.First < 1 || p.Last > 65535 || p.First > p.Last || (p.Proto != "tcp" && p.Proto != "udp") || p.Use == "" {
				t.Errorf("%s: bad port %+v", s.Name, p)
			}
			if p.First <= 22 && p.Last >= 22 {
				t.Errorf("%s: includes SSH", s.Name)
			}
			if seen[p.Key()] {
				t.Errorf("%s: %s twice", s.Name, p.Key())
			}
			seen[p.Key()] = true
		}
	}
	for _, n := range []string{"unifi", "jellyfin", "plex", "home-assistant", "portainer", "samba", "syncthing", "pihole", "adguard"} {
		if !names[n] {
			t.Errorf("no preset %s", n)
		}
	}
}

func TestMatch(t *testing.T) {
	for _, c := range []struct{ process, unit, image, want string }{
		{"jellyfin", "", "", "jellyfin"},
		{"java", "unifi.service", "", "unifi"},
		{"java", "", "lscr.io/linuxserver/unifi-network-application:9.0.114", "unifi"},
		{"", "", "jacobalberty/unifi@sha256:abc", "unifi"},
		{"Plex Media Serv", "", "", "plex"},
		{"python3", "home-assistant@homeassistant.service", "", "home-assistant"},
		{"", "", "ghcr.io/home-assistant/home-assistant:stable", "home-assistant"},
		{"syncthing", "", "", "syncthing"},
		{"", "syncthing@sam.service", "", "syncthing"},
		{"pihole-FTL", "", "", "pihole"},
		{"", "", "adguard/adguardhome", "adguard"},
		{"smbd", "", "", "samba"},
		{"", "", "portainer/portainer-ce:2.21.4", "portainer"},
		{"python3", "kb.service", "nginx:alpine", ""},
		{"java", "", "", ""},
	} {
		s, ok := Match(c.process, c.unit, c.image)
		if (c.want == "" && ok) || s.Name != c.want {
			t.Errorf("Match(%q, %q, %q) = %q, want %q", c.process, c.unit, c.image, s.Name, c.want)
		}
	}
}

func TestSummaryAndDiscovery(t *testing.T) {
	u, _ := Lookup("unifi")
	if got := u.Summary(); got != "8080, 8443, 6789, 8880-8882, 8843/tcp; 3478, 10001, 1900/udp" {
		t.Errorf("summary: %s", got)
	}
	if got := u.DiscoveryNote(); got != "10001, 1900/udp are how devices and apps find it on the LAN" {
		t.Errorf("discovery: %s", got)
	}
	if !u.Has(8881, "tcp") || u.Has(8881, "udp") || u.Has(27117, "tcp") {
		t.Error("Has")
	}
}

// docs/reference/services.md lists every preset and port, and nothing else.
func TestReferenceListsEveryPort(t *testing.T) {
	data, err := os.ReadFile("../../docs/reference/services.md")
	if err != nil {
		t.Fatal(err)
	}
	sections := map[string]string{}
	for _, sec := range strings.Split(string(data), "\n## ")[1:] {
		name, body, _ := strings.Cut(sec, "\n")
		sections[name] = body
	}
	for _, s := range All() {
		body, ok := sections[s.Name]
		if !ok {
			t.Errorf("no section ## %s", s.Name)
			continue
		}
		delete(sections, s.Name)
		rows := 0
		for _, l := range strings.Split(body, "\n") {
			if strings.HasPrefix(l, "| ") && !strings.HasPrefix(l, "| Port") {
				rows++
			}
		}
		if rows != len(s.Ports) {
			t.Errorf("%s: %d rows, %d ports", s.Name, rows, len(s.Ports))
		}
		for _, p := range s.Ports {
			if !strings.Contains(body, "| "+strings.TrimSuffix(p.Key(), "/"+p.Proto)+" | "+p.Proto+" |") {
				t.Errorf("%s: no row for %s", s.Name, p.Key())
			}
		}
		if !strings.Contains(body, strings.Fields(s.Source)[0]) {
			t.Errorf("%s: source missing", s.Name)
		}
	}
	for n := range sections {
		t.Errorf("section ## %s is not a preset", n)
	}
}
