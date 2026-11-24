package provider

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func genKey(t *testing.T) (pub, fingerprint string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "k")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", p).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v %s", err, out)
	}
	data, _ := os.ReadFile(p + ".pub")
	out, err := exec.Command("ssh-keygen", "-l", "-E", "sha256", "-f", p+".pub").Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(data), strings.Fields(string(out))[1]
}

func TestFingerprintMatchesSSHKeygen(t *testing.T) {
	pub, want := genKey(t)
	keys := ParseHostKeys(pub)
	if len(keys) != 1 || keys[0].Fingerprint() != want {
		t.Fatalf("got %v, want %s", keys, want)
	}
}

func TestParseHostKeysForms(t *testing.T) {
	text := "# 192.0.2.1:22 SSH-2.0-OpenSSH\n192.0.2.1 ssh-ed25519 AAAAC3Nz\n[h]:2222 ecdsa-sha2-nistp256 AAAAE2Vj\nssh-rsa AAAAB3Nz root@box\ngarbage\n"
	keys := ParseHostKeys(text)
	if len(keys) != 3 || keys[0].Type != "ssh-ed25519" || keys[1].Blob != "AAAAE2Vj" || keys[2].Type != "ssh-rsa" {
		t.Errorf("%+v", keys)
	}
}

func TestPinReplacesEntriesAndIsKnown(t *testing.T) {
	pub, _ := genKey(t)
	s := &SSH{Host: "192.0.2.7", Port: 2222, KnownHosts: filepath.Join(t.TempDir(), "kh")}
	if s.IsKnown() {
		t.Fatal("known before pinning")
	}
	if err := s.Pin([]HostKey{{Type: "ssh-ed25519", Blob: "AAAAold"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Pin(ParseHostKeys(pub)); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(s.KnownHosts)
	if strings.Contains(string(data), "AAAAold") || !strings.HasPrefix(string(data), "[192.0.2.7]:2222 ssh-ed25519 ") {
		t.Errorf("known_hosts:\n%s", data)
	}
	if !s.IsKnown() {
		t.Error("pinned host not known")
	}
}

func TestChooseNeverTrustsSilently(t *testing.T) {
	pub, fp := genKey(t)
	offered := ParseHostKeys(pub)
	s := &SSH{Host: "box", KnownHosts: "kh"}
	var hk *HostKeyError
	if _, err := s.choose(offered); err == nil || !strings.Contains(err.Error(), fp) {
		t.Errorf("no terminal, no pin: %v", err)
	} else if !errors.As(err, &hk) || hk.Host != "box" || hk.Want != "" || hk.Fingerprints() != fp+" (ssh-ed25519)" {
		t.Errorf("no terminal, no pin: not a host key error a caller can word itself: %+v", hk)
	}
	s.HostKey = fp
	if keys, err := s.choose(offered); err != nil || len(keys) != 1 {
		t.Errorf("pinned: %v %v", keys, err)
	}
	s.HostKey = "SHA256:" + strings.Repeat("A", 43)
	_, err := s.choose(offered)
	if err == nil || !strings.Contains(err.Error(), "Refusing") {
		t.Errorf("wrong pin accepted: %v", err)
	} else if !errors.As(err, &hk) || hk.Want != s.HostKey || len(hk.Offered) != 1 {
		t.Errorf("wrong pin: the error does not carry what was asked for and offered: %+v", hk)
	}
	if hk != nil && (hk.Fingerprints() != fp+" (ssh-ed25519)" || !strings.Contains(err.Error(), "it offers "+fp+" (ssh-ed25519).")) {
		t.Errorf("wrong pin: offered keys not listed fingerprint first: %v", err)
	}
	s.HostKey = ""
	var asked []HostKey
	s.ConfirmKeys = func(host string, keys []HostKey) bool { asked = keys; return host != "box" }
	if _, err := s.choose(offered); !errors.Is(err, ErrHostKeyUnconfirmed) || len(asked) != 1 || asked[0].Fingerprint() != fp {
		t.Errorf("declined: %v, asked about %v", err, asked)
	}
	s.ConfirmKeys = func(string, []HostKey) bool { return true }
	if keys, err := s.choose(offered); err != nil || len(keys) != 1 {
		t.Errorf("confirmed: %v", err)
	}
}

func TestCopyPinsCarriesTrustToAnotherName(t *testing.T) {
	pub, _ := genKey(t)
	kh := filepath.Join(t.TempDir(), "kh")
	ip := &SSH{Host: "192.0.2.9", Port: 22, KnownHosts: kh}
	name := &SSH{Host: "acme.tail1234.ts.net", Port: 22, KnownHosts: kh}
	if err := name.CopyPins(ip.Host, 22); err != nil || name.IsKnown() {
		t.Fatalf("nothing to copy: known=%v err=%v", name.IsKnown(), err)
	}
	if err := ip.Pin(ParseHostKeys(pub)); err != nil {
		t.Fatal(err)
	}
	if err := name.CopyPins(ip.Host, 22); err != nil || !name.IsKnown() {
		t.Fatalf("copy: known=%v err=%v", name.IsKnown(), err)
	}
	data, _ := os.ReadFile(kh)
	if strings.Count(string(data), strings.Fields(pub)[1]) != 2 {
		t.Errorf("known_hosts:\n%s", data)
	}
	// A tunnel to the same machine on another local port inherits the pins
	// of the public address on port 22.
	tunnel := &SSH{Host: "127.0.0.1", Port: 2222, KnownHosts: kh}
	if err := tunnel.CopyPins(ip.Host, 22); err != nil || !tunnel.IsKnown() {
		t.Fatalf("copy to another port: known=%v err=%v", tunnel.IsKnown(), err)
	}
	// The box is recreated: the public address gets new keys, and the
	// tailnet name follows them instead of keeping the old ones.
	pub2, _ := genKey(t)
	if err := ip.Pin(ParseHostKeys(pub2)); err != nil {
		t.Fatal(err)
	}
	if err := name.CopyPins(ip.Host, 22); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(kh)
	if strings.Count(string(data), strings.Fields(pub)[1]) != 1 { // the tunnel's, not yet refreshed
		t.Errorf("old keys kept for the tailnet name:\n%s", data)
	}
	if strings.Count(string(data), strings.Fields(pub2)[1]) != 2 {
		t.Errorf("new keys not copied:\n%s", data)
	}
}
