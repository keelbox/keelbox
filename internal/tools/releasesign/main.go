// Command releasesign signs and verifies a release's SHA256SUMS with a
// keelbox release key (docs/adr/0016-release-signatures.md).
//
//	releasesign sign VERSION SHA256SUMS      key from $KEELBOX_RELEASE_SIGNING_KEY (PKCS#8 PEM)
//	releasesign verify VERSION SHA256SUMS    against the keys embedded in keelbox
//	releasesign testkey DIR                  throwaway key for tests (private.pem, keys.pem, keys.ssh)
//
// sign writes SHA256SUMS.sig (raw Ed25519 over release.SignedMessage: a
// "keelbox-release VERSION" line, then the file) and SHA256SUMS.sshsig (the
// same key's ssh-keygen -Y signature of that message) beside it, and
// refuses a key that keelbox does not trust, so a wrong secret fails the
// release instead of publishing signatures no client accepts. With
// KEELBOX_RELEASE_KEYS (a keys.pem from testkey), sign and verify trust
// those keys instead: for tests only. The private key is never printed.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/keelbox/keelbox/internal/release"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "releasesign:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	usage := errors.New("usage: releasesign sign|verify VERSION SHA256SUMS | releasesign testkey DIR")
	if len(args) == 2 && args[0] == "testkey" {
		return testKey(args[1])
	}
	if len(args) != 3 {
		return usage
	}
	cmd, version, sums := args[0], args[1], args[2]
	switch cmd {
	case "sign":
		return sign(version, sums)
	case "verify":
		return verify(version, sums)
	}
	return usage
}

func trusted() ([]ed25519.PublicKey, error) {
	p := os.Getenv("KEELBOX_RELEASE_KEYS")
	if p == "" {
		return release.TrustedKeys(), nil
	}
	data, err := os.ReadFile(p) //nolint:gosec // G703: a maintainer's tool reads the file it is pointed at
	if err != nil {
		return nil, err
	}
	var keys []ed25519.PublicKey
	for {
		var block *pem.Block
		block, data = pem.Decode(data)
		if block == nil {
			break
		}
		k, err := release.ParsePublicKey(pem.EncodeToMemory(block))
		if err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	fmt.Fprintf(os.Stderr, "releasesign: trusting %d test key(s) from %s, not keelbox's release keys\n", len(keys), p)
	return keys, nil
}

func sign(version, sumsPath string) error {
	keyPEM := os.Getenv("KEELBOX_RELEASE_SIGNING_KEY")
	if keyPEM == "" {
		return errors.New("KEELBOX_RELEASE_SIGNING_KEY is not set")
	}
	block, _ := pem.Decode([]byte(keyPEM))
	if block == nil || block.Type != "PRIVATE KEY" {
		return errors.New("KEELBOX_RELEASE_SIGNING_KEY is not a PKCS#8 PEM private key")
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return errors.New("KEELBOX_RELEASE_SIGNING_KEY does not parse as PKCS#8")
	}
	priv, ok := k.(ed25519.PrivateKey)
	if !ok {
		return errors.New("KEELBOX_RELEASE_SIGNING_KEY is not an Ed25519 key")
	}
	sums, err := os.ReadFile(sumsPath) //nolint:gosec // G703: a maintainer's tool reads the file it is pointed at
	if err != nil {
		return err
	}
	msg := release.SignedMessage(version, sums)
	sig := ed25519.Sign(priv, msg)
	keys, err := trusted()
	if err != nil {
		return err
	}
	if err := release.VerifySums(version, sums, sig, keys); err != nil {
		return errors.New("the signing key is not one keelbox trusts; add its public key to internal/release/keys and get/install.sh first")
	}
	if err := os.WriteFile(sumsPath+".sig", sig, 0o644); err != nil { //nolint:gosec // G306: a public signature
		return err
	}
	if err := os.WriteFile(sumsPath+".sshsig", release.SignSSH(priv, msg), 0o644); err != nil { //nolint:gosec // G306: a public signature
		return err
	}
	fmt.Printf("signed %s for %s with %s\n", sumsPath, version, release.SSHPublicKey(priv.Public().(ed25519.PublicKey)))
	return nil
}

func verify(version, sumsPath string) error {
	sums, err := os.ReadFile(sumsPath) //nolint:gosec // G703: a maintainer's tool reads the file it is pointed at
	if err != nil {
		return err
	}
	sig, err := os.ReadFile(sumsPath + ".sig") //nolint:gosec // G703: a maintainer's tool uses the paths it is given
	if err != nil {
		return err
	}
	keys, err := trusted()
	if err != nil {
		return err
	}
	if err := release.VerifySums(version, sums, sig, keys); err != nil {
		return err
	}
	fmt.Printf("%s: release key signature for %s verified\n", sumsPath, version)
	return nil
}

// testKey writes a throwaway key pair for test releases.
func testKey(dir string) error {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return err
	}
	pubDER, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil { //nolint:gosec // G703: a maintainer's tool uses the paths it is given
		return err
	}
	files := map[string][]byte{
		"private.pem": pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}),
		"keys.pem":    pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER}),
		"keys.ssh":    []byte(release.SSHPublicKey(pub) + "\n"),
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil { //nolint:gosec // G703: a maintainer's tool uses the paths it is given
			return err
		}
	}
	return nil
}
