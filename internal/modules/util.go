// Package modules holds keelbox's box modules. Each is an ordered list of
// idempotent steps plus verify checks; see internal/engine.
package modules

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/system"
)

// All returns every module in apply order. Access comes straight after the
// host, so a box is reachable over the tailnet even if a later module
// fails; previews and backups follow the logbook, whose step installs the
// keelbox their services run, as does the service that takes the agent's
// package requests (permissions, after watch, whose queue its events go
// to); the agent CLIs come last because their downloads change most often
// and nothing else depends on them: root's managed settings for them,
// then the vendors' installs and Remote Control.
func All() []engine.Module {
	return []engine.Module{Host{}, Access{}, Runtime{}, Sessions{}, Primer{}, Logbook{}, Previews{}, Backup{}, Notify{}, Doctor{}, Watch{}, Permissions{}, AgentPolicyModule{}, Agents{}}
}

func curl(url, out string) system.Cmd {
	return system.C("curl", "--proto", "=https", "--tlsv1.2", "-fsSL", "--retry", "3", "--retry-all-errors", "-o", out, url)
}

// tempDir makes a private temporary directory owned by user ("" for root).
// The same user removes it: a directory another account owns is never
// removed by root.
func tempDir(ctx context.Context, e *engine.Env, user string) (string, func(), error) {
	c := system.C("mktemp", "-d", "/tmp/keelbox.XXXXXXXX")
	c.User = user
	dir, err := system.Output(ctx, e.X, c)
	if err != nil {
		return "", func() {}, err
	}
	rm := system.C("rm", "-rf", "--", dir)
	rm.User = user
	return dir, func() { _, _ = e.X.Run(context.Background(), rm) }, nil
}

// keyFingerprint returns the primary key fingerprint of an OpenPGP key file.
func keyFingerprint(ctx context.Context, e *engine.Env, path string) (string, error) {
	out, err := system.Output(ctx, e.X, system.C("gpg", "--batch", "--quiet", "--show-keys", "--with-colons", path))
	if err != nil {
		return "", err
	}
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "fpr:") {
			f := strings.Split(l, ":")
			if len(f) > 9 {
				return f[9], nil
			}
		}
	}
	return "", fmt.Errorf("no fingerprint in %s", path)
}

// aptKeyStep installs a repository signing key after checking it against
// the fingerprint pinned in the release manifest, so a tampered key server
// response is refused rather than trusted.
func aptKeyStep(name, url, dest, fingerprint string) engine.Step {
	return engine.Step{
		Name: name,
		Check: func(ctx context.Context, e *engine.Env) (engine.Status, error) {
			if st, _ := e.X.Stat(dest); !st.Exists {
				return engine.Pending("install signing key %s", dest), nil
			}
			fpr, err := keyFingerprint(ctx, e, dest)
			if err != nil || fpr != fingerprint {
				return engine.Pending("replace signing key %s (fingerprint mismatch)", dest), nil
			}
			return engine.Converged(), nil
		},
		Apply: func(ctx context.Context, e *engine.Env) error {
			dir, cleanup, err := tempDir(ctx, e, "")
			if err != nil {
				return err
			}
			defer cleanup()
			tmp := dir + "/key"
			if _, err := e.X.Run(ctx, curl(url, tmp)); err != nil {
				return err
			}
			fpr, err := keyFingerprint(ctx, e, tmp)
			if err != nil {
				return err
			}
			if fpr != fingerprint {
				return fmt.Errorf("signing key from %s has fingerprint %s, expected %s; refusing to trust it", url, fpr, fingerprint)
			}
			if _, err := e.X.Run(ctx, system.C("install", "-D", "-m", "0644", tmp, dest)); err != nil {
				return err
			}
			e.AptStale = true
			return nil
		},
	}
}

// aptSourceStep writes an apt source file and marks package lists stale.
func aptSourceStep(name, path, content string) engine.Step {
	return engine.FileStep(name, engine.File{Path: path, Content: []byte(content), Mode: 0o644,
		OnChange: func(ctx context.Context, e *engine.Env) error {
			e.AptStale = true
			return nil
		}})
}

// finding helpers

func ok(module, check, detail string) engine.Finding {
	return engine.Finding{Module: module, Check: check, Level: engine.LevelOK, Detail: detail}
}

func fail(module, check, detail string) engine.Finding {
	return engine.Finding{Module: module, Check: check, Level: engine.LevelFail, Detail: detail}
}

func warn(module, check, detail string) engine.Finding {
	return engine.Finding{Module: module, Check: check, Level: engine.LevelWarn, Detail: detail}
}

// flaky marks a finding that depends on the network or another machine:
// scheduled doctor alerts on it only after two failing runs in a row.
func flaky(f engine.Finding) engine.Finding {
	f.Flaky = true
	return f
}

// versionOf runs `<bin> --version` as the agent and returns the first line.
func versionOf(ctx context.Context, e *engine.Env, bin string, args ...string) (string, error) {
	if len(args) == 0 {
		args = []string{"--version"}
	}
	c := system.As(e.Cfg.Users.Agent.Name, bin, args...)
	c.Timeout = time.Minute
	out, err := system.Output(ctx, e.X, c)
	if err != nil {
		return "", err
	}
	line, _, _ := strings.Cut(out, "\n")
	return strings.TrimSpace(line), nil
}
