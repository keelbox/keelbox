package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The index scripts/channel-index.sh writes passes; one with a broken or
// unshowable notice does not.
func TestChannelIndexScript(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("no bash")
	}
	dir := t.TempDir()
	sums := filepath.Join(dir, "SHA256SUMS")
	var lines []string
	for _, p := range []string{"linux-amd64", "linux-arm64", "darwin-amd64", "darwin-arm64"} {
		lines = append(lines, strings.Repeat("a", 64)+"  keelbox-"+p)
	}
	if err := os.WriteFile(sums, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("bash", "../../../scripts/channel-index.sh", "v0.3.0", sums).Output()
	if err != nil {
		t.Fatal(err)
	}
	good := filepath.Join(dir, "stable.json")
	if err := os.WriteFile(good, out, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := check(good); err != nil {
		t.Errorf("the script's index: %v", err)
	}
	for name, body := range map[string]string{
		"trailing comma": strings.Replace(string(out), "\n}", ",\n}", 1),
		"bad notice":     strings.Replace(string(out), "\n}\n", ",\n  \"security\": [{\"fixed\": \"v0.2.3\", \"summary\": \"s\", \"url\": \"https://evil.example.com/\"}]\n}\n", 1),
	} {
		p := filepath.Join(dir, "bad.json")
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if check(p) == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
