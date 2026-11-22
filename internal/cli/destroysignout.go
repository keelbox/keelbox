package cli

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/keelbox/keelbox/internal/config"
)

// What a deleted server leaves valid elsewhere: before keelbox destroy
// deletes a server it can reach, the box signs out of Tailscale, which
// keelbox set up there; the sign-ins keelbox never handled are listed with
// where to revoke them.

// signOutTimeout bounds the sign-out: a box that does not answer must not
// hold the deletion up.
const signOutTimeout = 30 * time.Second

// signOutTailscale expires the box's Tailscale node key as the admin, over
// SSH. The logout runs detached a second later, so the SSH session, which
// may itself run over the tailnet, ends first. Tests replace it.
var signOutTailscale = func(ctx context.Context, cfg *config.Config, ip string, o options) error {
	ctx, cancel := context.WithTimeout(ctx, signOutTimeout)
	defer cancel()
	public := addr{ip, cfg.Target.SSH.Port}
	if name := cfg.Target.SSH.Host; name != "" {
		public = addr{name, cfg.Target.SSH.Port}
	}
	s := sshTarget(cfg, reach(ctx, cfg, public, io.Discard), o, io.Discard, io.Discard)
	s.ConfirmKeys = nil // a box about to be deleted gets no new host key trusted
	if o.sshUser != "" {
		s.User = o.sshUser
	}
	_, err := s.Output(ctx, "sudo -n true && (nohup sh -c 'sleep 1; sudo -n tailscale logout' </dev/null >/dev/null 2>&1 &)")
	if err == nil {
		// The logout starts a second after the session ends; the server must
		// still be there when it reaches Tailscale.
		signOutWait(ctx)
	}
	return err
}

// signOutWait gives the detached logout time to run; tests replace it.
var signOutWait = func(ctx context.Context) {
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
	}
}

// destroySignOut runs before the server goes. It says what it did, and
// never stops the deletion.
func destroySignOut(ctx context.Context, cfg *config.Config, ip string, o options, stdout io.Writer) (signedOut bool) {
	if !cfg.Tailscale() {
		return false
	}
	st := o.style
	if err := signOutTailscale(ctx, cfg, ip, o); err != nil {
		say(stdout, st, st.Bang(), fmt.Sprintf("could not sign %s out of Tailscale (%v); remove it in the Tailscale admin console", cfg.Box, firstLine(err.Error())))
		return false
	}
	say(stdout, st, st.Tick(), "asked "+cfg.Box+" to sign out of Tailscale")
	return true
}

// stillValid lists what outlives the server, with where to revoke each.
func stillValid(cfg *config.Config, signedOut bool, stdout io.Writer) {
	fmt.Fprintf(stdout, "\nStill valid after the server is gone; revoke what %s had:\n", cfg.Box)
	if cfg.Tailscale() {
		ts := "asked to sign out; check it shows as expired, and remove the machine"
		if !signedOut {
			ts = "remove the machine"
		}
		fmt.Fprintf(stdout, "  Tailscale  %s at https://login.tailscale.com/admin/machines\n", ts)
	}
	fmt.Fprintln(stdout, "  GitHub     the agent's token: https://github.com/settings/personal-access-tokens, or GitHub CLI at https://github.com/settings/applications")
	fmt.Fprintln(stdout, "  Claude     the box's sign-in, in your Claude account's settings")
	fmt.Fprintln(stdout, "  OpenAI     the box's Codex sign-in, in your ChatGPT or OpenAI account's settings")
	fmt.Fprintln(stdout, "  Other      API keys and deploy keys you gave the agent")
	fmt.Fprintf(stdout, "More: %s/guides/removing-a-box#what-stays-elsewhere\n", docsURL)
}
