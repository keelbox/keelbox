package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Reusing the hcloud CLI's sign-in (docs/adr/0066-hcloud-cli-token.md).
// Someone who already uses Hetzner's own CLI has a project token in one of
// its contexts. With target.hetzner.hcloud_context set, keelbox asks hcloud
// for that context's token each time it needs one and keeps no copy: not
// in its credential store, a file or the config. The token arrives on
// hcloud's standard output and goes nowhere but the API client.

// hcloudMinVersion is the first hcloud with "config get".
const hcloudMinVersion = "1.44.0"

// runHcloud runs the hcloud command for one context and returns what it
// printed. HCLOUD_TOKEN is taken out of its environment: hcloud would
// print that instead of the context's token. Tests replace it.
var runHcloud = execHcloud

func execHcloud(ctx context.Context, hcloudContext string, args ...string) (stdout, stderr string, err error) {
	path, err := exec.LookPath("hcloud")
	if err != nil {
		return "", "", errNoHcloud
	}
	cmd := exec.CommandContext(ctx, path, args...)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "HCLOUD_TOKEN=") || (hcloudContext != "" && strings.HasPrefix(kv, "HCLOUD_CONTEXT=")) {
			continue
		}
		cmd.Env = append(cmd.Env, kv)
	}
	if hcloudContext != "" {
		cmd.Env = append(cmd.Env, "HCLOUD_CONTEXT="+hcloudContext)
	}
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err = cmd.Run()
	return out.String(), errOut.String(), err
}

var errNoHcloud = errors.New("the hcloud command is not installed or not on PATH")

// hcloudActiveContext is the name of the hcloud CLI's active context, or
// "" when hcloud is not installed or has none. Tests replace it.
var hcloudActiveContext = activeHcloudContext

func activeHcloudContext(ctx context.Context) string {
	out, _, err := runHcloud(ctx, "", "context", "active")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// hcloudToken asks the hcloud CLI for the token of one of its contexts
// (hcloud config get token --allow-sensitive). The token is never part of
// an error: only hcloud's own message is, and never its standard output.
func hcloudToken(ctx context.Context, name, box string) (string, error) {
	fix := fmt.Sprintf("List the contexts: hcloud context list; create it: hcloud context create %s; or remove target.hetzner.hcloud_context from the config and run keelbox up %s to store a token with keelbox", name, box)
	out, errOut, err := runHcloud(ctx, name, "config", "get", "token", "--allow-sensitive")
	token := strings.TrimSpace(out)
	switch {
	case errors.Is(err, errNoHcloud):
		return "", fmt.Errorf("target.hetzner.hcloud_context is %q, but %w. Install hcloud %s or newer, or remove hcloud_context from the config and run keelbox up %s to store a token with keelbox", name, err, hcloudMinVersion, box)
	case err != nil:
		why := lastLine(errOut)
		if why == "" || (token != "" && strings.Contains(why, token)) {
			why = err.Error()
		}
		return "", fmt.Errorf("hcloud gave no token for its context %q (%s; this needs hcloud %s or newer). %s", name, why, hcloudMinVersion, fix)
	case token == "" || strings.ContainsAny(token, " \t\n"):
		return "", fmt.Errorf("the hcloud CLI has no token for a context named %q. %s", name, fix)
	}
	return token, nil
}
