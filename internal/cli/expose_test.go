package cli

import (
	"bytes"
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/previews"
	"github.com/keelbox/keelbox/internal/procnet"
)

// --list reads whether a dev server listens now, from the socket tables,
// not from the service's last run: a server started after expose, on [::1]
// as Vite does, is listening.
func TestExposeListReadsListenersNow(t *testing.T) {
	old := listeners
	t.Cleanup(func() { listeners = old })
	listeners = func() []procnet.Socket {
		return []procnet.Socket{{Local: netip.MustParseAddrPort("[::1]:5173"), Listen: true, UID: 1002}}
	}
	f := previews.File{Version: 1, Previews: []previews.Preview{{Name: "web", Port: 5173, HTTPSPort: 8401}, {Name: "api", Port: 3000, HTTPSPort: 8402}}}
	tn := previews.Tailnet{Connected: true, HTTPS: true, Name: "box.tail1.ts.net"}
	st := previews.Status{Tailnet: tn}
	for _, p := range f.Previews {
		// The service ran before either server started.
		st.Previews = append(st.Previews, previews.Published{Preview: p, URL: previews.URL(tn, p), Listening: false})
	}
	var out bytes.Buffer
	printPreviews(&out, f, st)
	lines := strings.Split(out.String(), "\n")
	if !strings.Contains(lines[1], "web") || !strings.Contains(lines[1], "https://box.tail1.ts.net:8401/") || !strings.HasSuffix(strings.TrimSpace(lines[1]), "listening") {
		t.Errorf("web on [::1] not listening:\n%s", out.String())
	}
	if !strings.Contains(lines[2], "api") || !strings.Contains(lines[2], "nothing on the port") {
		t.Errorf("api with nothing listening:\n%s", out.String())
	}
}

// The first preview waits for Tailscale's first certificate, longer than
// later ones, and says why; a service that never answers ends with the
// wait it gave.
func TestPublishWaitsForTheService(t *testing.T) {
	file := filepath.Join(t.TempDir(), "previews.json")
	start := time.Now()
	_, err := publish(context.Background(), file, previews.File{Version: 1}, 300*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "within 300ms") {
		t.Fatalf("publish: %v", err)
	}
	if time.Since(start) < 300*time.Millisecond {
		t.Error("gave up before its wait")
	}
	if _, err := os.Stat(file); err != nil {
		t.Errorf("change not saved: %v", err)
	}
	if exposeFirstCertWait < time.Minute || exposeFirstCertWait <= exposeWait {
		t.Errorf("first certificate wait %s, later %s", exposeFirstCertWait, exposeWait)
	}
}
