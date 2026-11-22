package modules

import (
	"context"
	"fmt"
	"strings"

	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/system"
)

// cloudInitSudoers is where cloud-init grants the image's default user
// (ubuntu on most clouds and LXD) passwordless sudo.
const cloudInitSudoers = "/etc/sudoers.d/90-cloud-init-users"

// CloudUsers lists the accounts cloud-init gave passwordless sudo, other
// than the two keelbox manages and root. On a machine cloud-init never set up the
// file is absent and nothing is listed, so a hand-made account is never
// mistaken for a cloud default.
func CloudUsers(e *engine.Env) []string {
	data, err := e.X.ReadFile(cloudInitSudoers)
	if err != nil {
		return nil
	}
	var out []string
	for _, l := range strings.Split(string(data), "\n") {
		f := strings.Fields(l)
		if len(f) < 2 || strings.HasPrefix(f[0], "#") || strings.HasPrefix(f[0], "%") {
			continue
		}
		name := f[0]
		if name == e.Cfg.Users.Admin.Name || name == e.Cfg.Users.Agent.Name || contains(out, name) {
			continue
		}
		// root is the image's account on Hetzner. sshd already refuses it,
		// and an expired root breaks root's cron jobs and console login.
		if u, err := e.X.LookupUser(name); err == nil && u.UID != 0 {
			out = append(out, name)
		}
	}
	return out
}

// shadowLocked reports whether /etc/shadow has the account's password
// locked and the account expired, which is what usermod --lock
// --expiredate 1 leaves. An expired account fails PAM's account check, so
// both SSH (any key) and sudo refuse it.
func shadowLocked(e *engine.Env, user string) (bool, error) {
	data, err := e.X.ReadFile("/etc/shadow")
	if err != nil {
		return false, fmt.Errorf("read /etc/shadow: %w", err)
	}
	for _, l := range strings.Split(string(data), "\n") {
		f := strings.Split(l, ":")
		if len(f) >= 8 && f[0] == user {
			return strings.HasPrefix(f[1], "!") && f[7] == "1", nil
		}
	}
	return false, fmt.Errorf("%s is not in /etc/shadow", user)
}

// cloudUserLockStep locks the cloud image's default account, which keeps
// the user's SSH key and passwordless sudo after keelbox creates the admin.
// It defers while keelbox runs from that account's session: the CLI's
// bootstrap then applies again as the admin, so the lock only happens from a
// session that proves the admin can log in and use sudo. Undo with
// sudo usermod --unlock NAME && sudo chage -E -1 NAME.
func cloudUserLockStep() engine.Step {
	unlocked := func(e *engine.Env) ([]string, error) {
		var out []string
		for _, u := range CloudUsers(e) {
			locked, err := shadowLocked(e, u)
			if err != nil {
				return nil, err
			}
			if !locked {
				out = append(out, u)
			}
		}
		return out, nil
	}
	return engine.Step{
		Name: "cloud-user-locked",
		Check: func(ctx context.Context, e *engine.Env) (engine.Status, error) {
			u, err := unlocked(e)
			if err != nil || len(u) == 0 {
				return engine.Converged(), err
			}
			return engine.Pending("lock cloud default account %s (SSH and sudo)", strings.Join(u, ", ")), nil
		},
		Apply: func(ctx context.Context, e *engine.Env) error {
			users, err := unlocked(e)
			if err != nil {
				return err
			}
			for _, u := range users {
				if u == e.Facts.InvokingUser {
					return engine.Defer("keelbox is running as %s; the next apply as %s locks it", u, e.Cfg.Users.Admin.Name)
				}
			}
			for _, u := range users {
				if _, err := e.X.Run(ctx, system.C("usermod", "--lock", "--expiredate", "1", u)); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

// rootExpired reports whether root's account has an expiry date, which
// apply clears if one is there.
func rootExpired(e *engine.Env) (bool, error) {
	data, err := e.X.ReadFile("/etc/shadow")
	if err != nil {
		return false, fmt.Errorf("read /etc/shadow: %w", err)
	}
	for _, l := range strings.Split(string(data), "\n") {
		if f := strings.Split(l, ":"); len(f) >= 8 && f[0] == "root" {
			return f[7] != "", nil
		}
	}
	return false, nil
}

// rootNotExpiredStep clears an expiry date on root. An expired root fails
// PAM's account check, so cron refuses root's jobs. Root's password and
// sshd's PermitRootLogin are left as they are.
func rootNotExpiredStep() engine.Step {
	return engine.Step{
		Name: "root-not-expired",
		Check: func(ctx context.Context, e *engine.Env) (engine.Status, error) {
			expired, err := rootExpired(e)
			if err != nil || !expired {
				return engine.Converged(), err
			}
			return engine.Pending("clear the expiry date on root (cron refuses an expired root)"), nil
		},
		Apply: func(ctx context.Context, e *engine.Env) error {
			_, err := e.X.Run(ctx, system.C("chage", "--expiredate", "-1", "root"))
			return err
		},
	}
}
