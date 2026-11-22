package modules

import (
	"context"
	"strings"

	"github.com/keelbox/keelbox/internal/engine"
)

// A bootstrap sudoers file is a passwordless sudo rule a person added only
// so keelbox could set the machine up, at
// /etc/sudoers.d/90-keelbox-bootstrap. Once the admin account works it is a
// second way to root without a password, so apply removes it and doctor
// fails while it is there (docs/adr/0049-password-sudo-over-ssh.md).

// BootstrapSudoers are the bootstrap sudoers files keelbox knows.
var BootstrapSudoers = []string{"/etc/sudoers.d/90-keelbox-bootstrap"}

// BootstrapSudoersStep is the step that removes them.
const BootstrapSudoersStep = "bootstrap-sudoers"

// bootstrapPresent lists the bootstrap sudoers files there now, with the
// accounts each names.
func bootstrapPresent(e *engine.Env) (paths, users []string) {
	for _, p := range BootstrapSudoers {
		if st, err := e.X.Stat(p); err != nil || !st.Exists {
			continue
		}
		paths = append(paths, p)
		data, _ := e.X.ReadFile(p)
		for _, l := range strings.Split(string(data), "\n") {
			f := strings.Fields(l)
			if len(f) >= 2 && !strings.HasPrefix(f[0], "#") && !strings.HasPrefix(f[0], "Defaults") && !contains(users, f[0]) {
				users = append(users, f[0])
			}
		}
	}
	return paths, users
}

// bootstrapDetail is what the files do, in words.
func bootstrapDetail(paths, users []string) string {
	who := "an account"
	if len(users) > 0 {
		who = strings.Join(users, ", ")
	}
	return strings.Join(paths, ", ") + ", which gives " + who + " sudo without a password"
}

// bootstrapSudoersStep removes the files. Over SSH, while keelbox runs as
// the account a file may serve (not the admin, not root), it defers: that
// session's own clean-up still needs the account's sudo, and the admin's
// apply that keelbox up and the CLI's bootstrap run next removes it. On the
// machine itself, where sudo can ask for a password, it goes at once.
func bootstrapSudoersStep() engine.Step {
	return engine.Step{
		Name: BootstrapSudoersStep,
		Check: func(ctx context.Context, e *engine.Env) (engine.Status, error) {
			paths, users := bootstrapPresent(e)
			if len(paths) == 0 {
				return engine.Converged(), nil
			}
			return engine.Pending("remove %s", bootstrapDetail(paths, users)), nil
		},
		Apply: func(ctx context.Context, e *engine.Env) error {
			paths, _ := bootstrapPresent(e)
			if u := e.Facts.InvokingUser; e.SSHConnection != "" && u != "" && u != "root" && u != e.Cfg.Users.Admin.Name {
				return engine.Defer("keelbox is running as %s over SSH; the next apply as %s removes it", u, e.Cfg.Users.Admin.Name)
			}
			for _, p := range paths {
				if err := e.X.Remove(p); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

// verifyBootstrapSudoers fails while a bootstrap sudoers file is there.
func verifyBootstrapSudoers(e *engine.Env) engine.Finding {
	paths, users := bootstrapPresent(e)
	if len(paths) == 0 {
		return ok("host", "bootstrap-sudo", "no passwordless sudo left from setting the box up")
	}
	return fail("host", "bootstrap-sudo", bootstrapDetail(paths, users)+"; keelbox apply "+e.Cfg.Box+" removes it (the admin account has sudo)")
}
