package cli

import (
	"regexp"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/prompt"
)

// Where "This machine" is the answer Enter takes, its row says why.
func TestThisMachinePreselectedSaysWhy(t *testing.T) {
	isolate(t)
	defer func(f func() string) { localWhy = f }(localWhy)
	localWhy = func() string { return "this is Ubuntu without a desktop session" }
	ask := func(likely bool) string {
		var b strings.Builder
		w := &wizard{p: prompt.New(strings.NewReader("\n"), &b, true), out: &b}
		defer func(f func() bool) { localLikely = f }(localLikely)
		localLikely = func() bool { return likely }
		var a answers
		_ = w.askWhere(t.Context(), &a)
		if want := map[bool]string{true: viaLocal, false: viaSSH}[likely]; a.via != want {
			t.Errorf("likely %v: Enter took %q, want %q", likely, a.via, want)
		}
		return b.String()
	}
	got := ask(true)
	if !strings.Contains(got, "2) This machine") || !strings.Contains(got, "set up the Ubuntu machine you are on, with sudo; preselected because this is Ubuntu without a desktop session") ||
		!strings.Contains(got, "Choose 1-4 [2]: ") {
		t.Errorf("preselected without the reason:\n%s", got)
	}
	if got := ask(false); strings.Contains(got, "preselected") || !strings.Contains(got, "Choose 1-4 [1]: ") {
		t.Errorf("not preselected:\n%s", got)
	}
}

// The size keelbox preselects is the recommended one, marked as such, and
// a size too small for agents is never the one. The
// go-ahead that follows the card with the price still takes Enter as yes.
func TestRecommendedSizeIsPreselectedAndMarked(t *testing.T) {
	home := isolate(t)
	laptopKey(t, home)
	fakeHetzner(t)
	code, out := upWith(t, &fakeRunner{}, script("3", "1", "acme", goodToken, "", "", "", "", "", "", "n"), "up")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	rec := regexp.MustCompile(`(?m)^ +(\d+)\) cx23 +2 vCPU \(shared\), 4 GB, 40 GB disk, EUR 3\.79/month.*; recommended$`).FindStringSubmatch(out)
	if rec == nil {
		t.Fatalf("no size is marked as recommended:\n%s", out)
	}
	if !strings.Contains(out, "Which size?") || !strings.Contains(out, "Choose 1-") || !regexp.MustCompile(`Which size\?\n(.*\n)+?Choose 1-\d+ \[`+rec[1]+`\]: `).MatchString(out) {
		t.Errorf("the recommended size, row %s, is not the one Enter takes:\n%s", rec[1], out)
	}
	if strings.Count(out, "; recommended") != 1 {
		t.Errorf("more than one size is recommended:\n%s", out)
	}
	if !strings.Contains(out, "2 vCPU and 4 GB are enough for one or two agents.") {
		t.Errorf("the hint does not say what the recommended size is enough for:\n%s", out)
	}
	if !strings.Contains(out, "Create the server (about EUR 3.79 a month until you destroy it)? [Y/n] ") {
		t.Errorf("the go-ahead after the card:\n%s", out)
	}
	// The token card gives the fact a choice of project rests on, and
	// leaves the choice.
	flat := strings.Join(strings.Fields(out), " ")
	if !strings.Contains(flat, "A token can reach every server in its project and nothing outside it: in a project used only for keelbox, your other servers are out of its reach.") ||
		strings.Contains(flat, "Use a project only for keelbox") {
		t.Errorf("the token card:\n%s", out)
	}
	// A list with no size that is enough for agents has no recommended one.
	small := []prompt.Choice{{Label: "nano", Detail: "1 vCPU, 1 GB; small for agents"}}
	recommend(small, 0)
	if strings.Contains(small[0].Detail, "recommended") {
		t.Errorf("a size too small for agents is recommended: %q", small[0].Detail)
	}
}

// On a Mac, "This machine" is not offered: it can never be the box, and
// a kept answer would make the next keelbox up fail the same way.
func TestThisMachineNotOfferedOffLinux(t *testing.T) {
	isolate(t)
	defer func(s string) { hostOS = s }(hostOS)
	hostOS = "darwin"
	defer func(f func() bool) { localLikely = f }(localLikely)
	localLikely = func() bool { return true }

	var b strings.Builder
	w := &wizard{p: prompt.New(strings.NewReader("\n"), &b, true), out: &b, draft: &upDraft{Answers: map[string]string{}}}
	var a answers
	if err := w.askWhere(t.Context(), &a); err != nil {
		t.Fatal(err)
	}
	got := b.String()
	if strings.Contains(got, "This machine") || !strings.Contains(got, "Choose 1-3 [1]: ") || a.via != viaSSH {
		t.Errorf("this machine offered on macOS, or Enter took %q:\n%s", a.via, got)
	}

	// --provider local is refused before it is kept, with the routes that work.
	forgetDraft()
	w = &wizard{p: prompt.New(strings.NewReader(""), &b, true), out: &b, draft: &upDraft{Answers: map[string]string{}}}
	w.o.provider = viaLocal
	err := w.askWhere(t.Context(), &answers{})
	if err == nil || !strings.Contains(err.Error(), "this machine runs macOS") || !strings.Contains(err.Error(), "keelbox up --provider ssh") {
		t.Errorf("--provider local on macOS: %v", err)
	}
	if _, ok := loadDraft(); ok {
		t.Error("the refused answer was kept")
	}

	// A draft an older keelbox kept with this machine is not carried on.
	d := upDraft{Answers: map[string]string{"provider": viaLocal}}
	if err := d.save(); err != nil {
		t.Fatal(err)
	}
	if _, ok := loadDraft(); ok {
		t.Error("a kept this-machine answer is still offered on macOS")
	}
	d.Answers["name"] = "acme"
	if err := d.save(); err != nil {
		t.Fatal(err)
	}
	if got, ok := loadDraft(); !ok || got.Answers["provider"] != "" || got.Answers["name"] != "acme" {
		t.Errorf("draft with a name: %v %+v", ok, got)
	}
}
