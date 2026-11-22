package modules

import (
	"context"
	"fmt"

	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/system"
)

// lingerStep keeps the agent's user manager running without a login, so its
// user services (the tmux server, rootless Docker) survive logout and start
// at boot.
func lingerStep(user string) engine.Step {
	return engine.Step{
		Name: "linger",
		Check: func(ctx context.Context, e *engine.Env) (engine.Status, error) {
			if st, _ := e.X.Stat("/var/lib/systemd/linger/" + user); st.Exists {
				return engine.Converged(), nil
			}
			return engine.Pending("enable lingering for %s", user), nil
		},
		Apply: func(ctx context.Context, e *engine.Env) error {
			_, err := e.X.Run(ctx, system.C("loginctl", "enable-linger", user))
			return err
		},
	}
}

// userctl is systemctl against a user's own service manager.
func userctl(user string, args ...string) system.Cmd {
	return system.C("systemctl", append([]string{"--user", "-M", user + "@"}, args...)...)
}

// startUserManager starts user@UID.service. Lingering starts it at boot;
// starting it here makes user units work in the run that enabled lingering.
func startUserManager(ctx context.Context, e *engine.Env, u system.User) error {
	usr, err := e.X.LookupUser(u.Name)
	if err != nil {
		return err
	}
	_, err = e.X.Run(ctx, system.C("systemctl", "start", fmt.Sprintf("user@%d.service", usr.UID)))
	return err
}

// userUnitFile is a managed unit in the user's ~/.config/systemd/user. A
// change reloads the user manager and restarts the unit if it is running.
func userUnitFile(step string, u system.User, unit, content string) engine.Step {
	return engine.FileStep(step, engine.File{
		Path: u.Home + "/.config/systemd/user/" + unit, Content: []byte(content), Mode: 0o644, Owner: u.Name,
		OnChange: func(ctx context.Context, e *engine.Env) error {
			return restartUserUnit(ctx, e, u, unit, true)
		},
	})
}

// restartUserUnit restarts a user unit if it is running, after reloading
// unit files when reload is set. A unit that is not running yet (or whose
// user manager is not up) is left alone: enabling it later starts it with
// the new state.
func restartUserUnit(ctx context.Context, e *engine.Env, u system.User, unit string, reload bool) error {
	usr, err := e.X.LookupUser(u.Name)
	if err != nil {
		return err
	}
	if !system.Succeeds(ctx, e.X, system.C("systemctl", "is-active", "--quiet", fmt.Sprintf("user@%d.service", usr.UID))) {
		return nil
	}
	if reload {
		if _, err := e.X.Run(ctx, userctl(u.Name, "daemon-reload")); err != nil {
			return err
		}
	}
	if !system.Succeeds(ctx, e.X, userctl(u.Name, "is-active", "--quiet", unit)) {
		return nil // not loaded or not running yet
	}
	_, err = e.X.Run(ctx, userctl(u.Name, "restart", unit))
	return err
}

// reloadUserManager reloads the user's unit files when the user manager
// runs, and restarts nothing.
func reloadUserManager(ctx context.Context, e *engine.Env, u system.User) error {
	usr, err := e.X.LookupUser(u.Name)
	if err != nil {
		return err
	}
	if !system.Succeeds(ctx, e.X, system.C("systemctl", "is-active", "--quiet", fmt.Sprintf("user@%d.service", usr.UID))) {
		return nil
	}
	_, err = e.X.Run(ctx, userctl(u.Name, "daemon-reload"))
	return err
}

// userUnitStep enables and starts a user unit.
func userUnitStep(step string, u system.User, unit string) engine.Step {
	link := u.Home + "/.config/systemd/user/default.target.wants/" + unit
	return engine.Step{
		Name: step,
		User: u.Name,
		Check: func(ctx context.Context, e *engine.Env) (engine.Status, error) {
			if st, _ := e.FS(u.Name).Stat(link); !st.Exists {
				return engine.Pending("enable %s for %s", unit, u.Name), nil
			}
			if !system.Succeeds(ctx, e.X, userctl(u.Name, "is-active", "--quiet", unit)) {
				return engine.Pending("start %s for %s", unit, u.Name), nil
			}
			return engine.Converged(), nil
		},
		Apply: func(ctx context.Context, e *engine.Env) error {
			if err := startUserManager(ctx, e, u); err != nil {
				return err
			}
			for _, c := range []system.Cmd{userctl(u.Name, "daemon-reload"), userctl(u.Name, "enable", "--now", unit)} {
				if _, err := e.X.Run(ctx, c); err != nil {
					return err
				}
			}
			return nil
		},
	}
}
