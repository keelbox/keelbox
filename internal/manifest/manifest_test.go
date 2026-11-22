package manifest

import (
	"strings"
	"testing"
)

func TestStableIsValid(t *testing.T) {
	m, err := ForChannel("stable")
	if err != nil {
		t.Fatal(err)
	}
	if !m.Supports("26.04", "amd64") || !m.Supports("24.04", "arm64") || m.Supports("22.04", "amd64") || m.Supports("26.04", "riscv64") {
		t.Error("support matrix wrong")
	}
	if got := m.MiseURL("amd64"); !strings.HasSuffix(got, "/v"+m.Mise.Version+"/mise-v"+m.Mise.Version+"-linux-x64.tar.gz") {
		t.Errorf("mise url %s", got)
	}
}

func TestUnknownChannel(t *testing.T) {
	if _, err := ForChannel("edge"); err == nil {
		t.Error("edge accepted")
	}
}

func TestInvalidPinsRejected(t *testing.T) {
	bad := strings.Replace(string(stable), "node: ", "node: v", 1)
	bad = strings.Replace(bad, "series: \"29\"", "series: latest", 1)
	_, err := Parse([]byte(bad))
	if err == nil || !strings.Contains(err.Error(), "node") || !strings.Contains(err.Error(), "docker.series") {
		t.Fatalf("got %v", err)
	}
	if _, err := Parse([]byte(string(stable) + "\nextra: 1\n")); err == nil {
		t.Error("unknown field accepted")
	}
}

func TestCompareReleases(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"2026.11.0", "2026.11.0", 0},
		{"2026.11.0", "2026.11.1", -1},
		{"2026.12.0", "2026.11.9", 1},
		{"2026.9.0", "2026.10.0", -1},
		{"2026.11", "2026.11.1", -1},
	} {
		if got := CompareReleases(c.a, c.b); got != c.want {
			t.Errorf("CompareReleases(%s, %s) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
