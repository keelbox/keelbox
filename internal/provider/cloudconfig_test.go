package provider

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestCloudConfig(t *testing.T) {
	key := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKey you@laptop"
	out, err := CloudConfig("acme", "admin", []string{key}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "#cloud-config\n") {
		t.Fatalf("no #cloud-config header:\n%s", out)
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	users := doc["users"].([]any)
	u := users[0].(map[string]any)
	if u["name"] != "admin" || u["primary_group"] != "admin" || u["sudo"] != "ALL=(ALL) NOPASSWD:ALL" || u["lock_passwd"] != true || u["ssh_authorized_keys"].([]any)[0] != key {
		t.Errorf("user %v", u)
	}
	// No "default" entry: the image's own account is not created.
	if len(users) != 1 || doc["ssh_pwauth"] != false || doc["disable_root"] != true {
		t.Errorf("doc %v", doc)
	}
	if _, ok := doc["ssh_keys"]; ok || strings.Contains(out, "PRIVATE") {
		t.Error("host key without asking")
	}
	if _, err := CloudConfig("acme", "admin", nil, nil); err == nil {
		t.Error("no keys accepted")
	}
}

func TestCloudConfigWithHostKey(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("no ssh-keygen")
	}
	priv, pub, err := GenerateHostKey(context.Background(), "acme")
	if err != nil {
		t.Fatal(err)
	}
	out, err := CloudConfig("acme", "admin", []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKey you@laptop"}, &HostKeyPair{Private: priv, Public: pub})
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		SSHDeleteKeys bool              `yaml:"ssh_deletekeys"`
		SSHKeys       map[string]string `yaml:"ssh_keys"`
	}
	if err := yaml.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	if !doc.SSHDeleteKeys || strings.Contains(out, "ssh_genkeytypes") || doc.SSHKeys["ed25519_private"] != priv || doc.SSHKeys["ed25519_public"] != strings.TrimSpace(pub) {
		t.Errorf("host key not installed as generated:\n%s", out)
	}
	if !strings.Contains(out, "single-use") {
		t.Error("snippet does not say the host key is single-use")
	}
}

// The user data of a resumed box tells cloud-init to keep what is on the
// disk and nothing else: no account, no key, no host key.
func TestResumeUserDataKeepsWhatIsOnTheDisk(t *testing.T) {
	ud := ResumeUserData("acme")
	if !strings.HasPrefix(ud, "#cloud-config\n") {
		t.Fatalf("not cloud-config:\n%s", ud)
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(ud), &doc); err != nil {
		t.Fatal(err)
	}
	users, ok := doc["users"].([]any)
	if len(doc) != 3 || doc["ssh_deletekeys"] != false || doc["preserve_hostname"] != true || !ok || len(users) != 0 {
		t.Errorf("user data sets %v", doc)
	}
	labels := ParkedLabels("acme", map[string]string{"team": "x", LabelBox: "other"})
	if !Parked(labels, "acme") || labels["team"] != "x" || Parked(ManagedLabels("acme", nil), "acme") || Parked(labels, "other") {
		t.Errorf("parked labels %v", labels)
	}
}
