package release

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha512"
	"crypto/x509"
	"embed"
	"encoding/base64"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
)

// The release keys sign every release's SHA256SUMS (SHA256SUMS.sig, raw
// Ed25519 over SignedMessage: the version, then the file's bytes, so a
// signed SHA256SUMS cannot be served as another version's). Each keelbox binary and get/install.sh
// carry the public halves; a signature by any one of them is accepted, so a
// release signed by the current key can introduce the next one
// (docs/adr/0016-release-signatures.md). The private key is the release
// workflow's KEELBOX_RELEASE_SIGNING_KEY secret.
//
//go:embed keys/*.pem
var keyFiles embed.FS

// SSHNamespace is the ssh-keygen -Y namespace of SHA256SUMS.sshsig, the
// same signature in the form ssh-keygen verifies (for machines whose openssl
// lacks Ed25519, such as macOS).
const SSHNamespace = "keelbox-release"

// TrustedKeys returns the embedded release public keys, in file name order.
func TrustedKeys() []ed25519.PublicKey {
	names, err := keyFiles.ReadDir("keys")
	if err != nil {
		panic(err)
	}
	var out []ed25519.PublicKey
	for _, n := range names {
		data, err := keyFiles.ReadFile(path.Join("keys", n.Name()))
		if err != nil {
			panic(err)
		}
		k, err := ParsePublicKey(data)
		if err != nil {
			panic(fmt.Sprintf("embedded release key %s: %v", n.Name(), err))
		}
		out = append(out, k)
	}
	return out
}

// TrustedKeyPEMs returns the embedded key files, for checks that the
// installer carries the same keys.
func TrustedKeyPEMs() []string {
	names, _ := keyFiles.ReadDir("keys")
	var out []string
	for _, n := range names {
		data, _ := keyFiles.ReadFile(path.Join("keys", n.Name()))
		out = append(out, string(data))
	}
	sort.Strings(out)
	return out
}

// ParsePublicKey reads a PEM "PUBLIC KEY" (SubjectPublicKeyInfo) holding an
// Ed25519 key, as `openssl pkey -pubout` writes it.
func ParsePublicKey(data []byte) (ed25519.PublicKey, error) {
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "PUBLIC KEY" {
		return nil, errors.New("not a PEM public key")
	}
	k, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	ek, ok := k.(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("not an Ed25519 key (%T)", k)
	}
	return ek, nil
}

// SignedMessage is what a release signature covers: a line naming the
// version, then SHA256SUMS exactly.
func SignedMessage(version string, sums []byte) []byte {
	return append([]byte("keelbox-release "+version+"\n"), sums...)
}

// ErrBadSignature is returned when no trusted key signed SHA256SUMS.
var ErrBadSignature = errors.New("SHA256SUMS is not signed by a keelbox release key")

// VerifySums checks sig, a raw Ed25519 signature over
// SignedMessage(version, sums), against keys.
func VerifySums(version string, sums, sig []byte, keys []ed25519.PublicKey) error {
	msg := SignedMessage(version, sums)
	if len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("%w: signature is %d bytes, not %d", ErrBadSignature, len(sig), ed25519.SignatureSize)
	}
	for _, k := range keys {
		if ed25519.Verify(k, msg, sig) {
			return nil
		}
	}
	return ErrBadSignature
}

// SSHPublicKey renders k as an OpenSSH public key ("ssh-ed25519 AAAA...").
func SSHPublicKey(k ed25519.PublicKey) string {
	return "ssh-ed25519 " + base64.StdEncoding.EncodeToString(sshKeyBlob(k))
}

func sshString(b *bytes.Buffer, s []byte) {
	_ = binary.Write(b, binary.BigEndian, uint32(len(s))) //nolint:gosec // G115: keys, hashes and signatures are short
	b.Write(s)
}

func sshKeyBlob(k ed25519.PublicKey) []byte {
	var b bytes.Buffer
	sshString(&b, []byte("ssh-ed25519"))
	sshString(&b, k)
	return b.Bytes()
}

// SignSSH returns an armoured SSH signature (PROTOCOL.sshsig) of msg in
// SSHNamespace, the format `ssh-keygen -Y verify` reads.
func SignSSH(priv ed25519.PrivateKey, msg []byte) []byte {
	h := sha512.Sum512(msg)
	var signed bytes.Buffer
	signed.WriteString("SSHSIG")
	sshString(&signed, []byte(SSHNamespace))
	sshString(&signed, nil) // reserved
	sshString(&signed, []byte("sha512"))
	sshString(&signed, h[:])
	var sig bytes.Buffer
	sshString(&sig, []byte("ssh-ed25519"))
	sshString(&sig, ed25519.Sign(priv, signed.Bytes()))

	var blob bytes.Buffer
	blob.WriteString("SSHSIG")
	_ = binary.Write(&blob, binary.BigEndian, uint32(1))
	sshString(&blob, sshKeyBlob(priv.Public().(ed25519.PublicKey)))
	sshString(&blob, []byte(SSHNamespace))
	sshString(&blob, nil)
	sshString(&blob, []byte("sha512"))
	sshString(&blob, sig.Bytes())

	enc := base64.StdEncoding.EncodeToString(blob.Bytes())
	var out strings.Builder
	out.WriteString("-----BEGIN SSH SIGNATURE-----\n")
	for len(enc) > 70 {
		out.WriteString(enc[:70] + "\n")
		enc = enc[70:]
	}
	out.WriteString(enc + "\n-----END SSH SIGNATURE-----\n")
	return []byte(out.String())
}
