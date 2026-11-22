package previews

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/system"
)

// agentFileMax bounds the agent's previews file: a few hundred entries fit
// in a small fraction of it.
const agentFileMax = 1 << 20

// Sync makes Caddy serve what the agent's file asks for, if the box can:
// it reads the file as the agent (Env.FS), keeps the valid entries, fetches
// the Tailscale certificate, renders and installs the Caddy config,
// restarts Caddy when anything changed (or stops it when nothing is to be
// served), and writes the status file. Run as root by keelbox-previews.service.
func Sync(ctx context.Context, e *engine.Env) (Status, error) {
	agent := e.AgentUser()
	lo, hi := e.Cfg.PreviewPorts()
	skip := e.Cfg.PreviewSkipped()
	st := Status{Agent: agent.Name, Ports: [2]int{lo, hi}, Skip: skip, Previews: []Published{}, Render: Render, Updated: time.Now().UTC()}

	data, err := system.ReadData(e.FS(agent.Name), agent.Home+"/"+AgentFile, agentFileMax)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		data = nil
	case err != nil:
		st.Problems = append(st.Problems, "cannot read "+agent.Home+"/"+AgentFile+" as "+agent.Name+": "+err.Error())
		data = nil
	}
	st.Source = Hash(data)
	if c, err := e.X.Stat(CertPath); err == nil && c.Exists {
		st.Certificate = true
	}
	want, problems := ParseFile(data, lo, hi, skip...)
	st.Problems = append(st.Problems, problems...)

	out, _ := system.Output(ctx, e.X, system.C("tailscale", "status", "--json"))
	st.Tailnet = ParseTailscaleStatus(out)
	switch {
	case !st.Tailnet.Connected:
		st.Error = NotOnTailnet
	case len(st.Tailnet.IPs) == 0:
		st.Error = "tailscale reports no tailnet address for this box"
	case !st.Tailnet.HTTPS:
		st.Error = NoHTTPS
	}
	if st.Error != "" || len(want) == 0 {
		if err := stopCaddy(ctx, e); err != nil {
			return st, writeStatus(e, st, err)
		}
		return st, writeStatus(e, st, nil)
	}

	certChanged, err := refreshCert(ctx, e, st.Tailnet.Name)
	if err != nil {
		st.Error = "could not get the Tailscale certificate for " + st.Tailnet.Name + ": " + err.Error()
		_ = stopCaddy(ctx, e)
		return st, writeStatus(e, st, err)
	}
	st.Certificate = true
	cfg, err := RenderCaddy(st.Tailnet, want)
	if err != nil {
		st.Error = err.Error()
		_ = stopCaddy(ctx, e)
		return st, writeStatus(e, st, err)
	}
	cur, _ := e.X.ReadFile(CaddyConfig)
	cfgChanged := !bytes.Equal(cur, cfg)
	if cfgChanged {
		if err := e.X.WriteFile(CaddyConfig, cfg, 0o640, "root", ServiceUser); err != nil {
			return st, writeStatus(e, st, err)
		}
	}
	active := system.Succeeds(ctx, e.X, system.C("systemctl", "is-active", "--quiet", CaddyUnit))
	if cfgChanged || certChanged || !active {
		if _, err := e.X.Run(ctx, system.C("systemctl", "restart", CaddyUnit)); err != nil {
			st.Error = "Caddy did not start (journalctl -u " + CaddyUnit + "): " + err.Error()
			return st, writeStatus(e, st, err)
		}
	}
	ss, _ := system.Output(ctx, e.X, system.C("ss", "-Hltn"))
	ls := Listeners(ss)
	for _, p := range want {
		st.Previews = append(st.Previews, Published{Preview: p, URL: URL(st.Tailnet, p), Listening: LoopbackListening(ls, p.Port)})
	}
	return st, writeStatus(e, st, nil)
}

// stopCaddy stops Caddy, so nothing listens while there is nothing to serve.
func stopCaddy(ctx context.Context, e *engine.Env) error {
	if !system.Succeeds(ctx, e.X, system.C("systemctl", "is-active", "--quiet", CaddyUnit)) {
		return nil
	}
	_, err := e.X.Run(ctx, system.C("systemctl", "stop", CaddyUnit))
	return err
}

// refreshCert asks tailscaled for the box's certificate (it renews before
// expiry and otherwise returns its cached copy) and installs it for Caddy
// when it changed. The key stays readable only by root and Caddy's user.
func refreshCert(ctx context.Context, e *engine.Env, name string) (bool, error) {
	newCert, newKey := CertPath+".new", KeyPath+".new"
	defer func() { _ = e.X.Remove(newCert); _ = e.X.Remove(newKey) }()
	if _, err := e.X.Run(ctx, system.C("tailscale", "cert", "--cert-file", newCert, "--key-file", newKey, name)); err != nil {
		return false, err
	}
	cert, err := e.X.ReadFile(newCert)
	if err != nil {
		return false, err
	}
	key, err := e.X.ReadFile(newKey)
	if err != nil {
		return false, err
	}
	curCert, _ := e.X.ReadFile(CertPath)
	curKey, _ := e.X.ReadFile(KeyPath)
	if bytes.Equal(cert, curCert) && bytes.Equal(key, curKey) {
		return false, nil
	}
	if err := e.X.WriteFile(KeyPath, key, 0o640, "root", ServiceUser); err != nil {
		return false, err
	}
	return true, e.X.WriteFile(CertPath, cert, 0o644, "root", ServiceUser)
}

func writeStatus(e *engine.Env, st Status, cause error) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	if werr := e.X.WriteFile(StatusPath, append(b, '\n'), 0o644, "root", "root"); werr != nil {
		return werr
	}
	if cause != nil {
		return fmt.Errorf("previews: %w", cause)
	}
	return nil
}

// ReadStatus reads the status file through root's file access.
func ReadStatus(e *engine.Env) (Status, bool) {
	data, err := e.X.ReadFile(StatusPath)
	if err != nil || len(data) == 0 {
		return Status{}, false
	}
	s, err := ParseStatus(data)
	return s, err == nil
}
