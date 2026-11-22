// Package gcp creates, finds and destroys a box on Google Cloud, as a
// Compute Engine VM, by running the user's installed gcloud CLI
// (docs/adr/0068-google-cloud-through-gcloud.md). keelbox holds no Google
// credential and sends no request itself: gcloud uses the sign-in the user
// already has, and keelbox reads only its JSON output.
package gcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// InstallURL is where gcloud's install instructions are.
const InstallURL = "https://cloud.google.com/sdk/docs/install"

// PricingURL is Google's price list for Compute Engine VMs. gcloud has no
// command that prints a price, so keelbox shows none and names this page.
const PricingURL = "https://cloud.google.com/compute/vm-instance-pricing"

// SnapshotPricingURL is Google's price list for disks and their snapshots.
const SnapshotPricingURL = "https://cloud.google.com/compute/disks-image-pricing"

// Runner runs the gcloud command with arguments and returns what it
// printed and its exit code. err is set only when the command could not be
// run.
type Runner interface {
	Run(ctx context.Context, args []string) (stdout, stderr []byte, exit int, err error)
}

// Exec is the Runner that runs gcloud: the one at Path, or the one found
// on PATH when Path is empty.
type Exec struct {
	Path string
}

// ErrNoCLI is gcloud missing from PATH.
var ErrNoCLI = errors.New("gcloud (the Google Cloud CLI) is not installed or not on PATH; keelbox creates Google Cloud servers through it. " +
	"Install it: " + InstallURL + ", then sign in: gcloud auth login")

// Installed reports whether gcloud is on PATH.
func Installed() bool {
	_, err := exec.LookPath("gcloud")
	return err == nil
}

// Run implements Runner.
func (e Exec) Run(ctx context.Context, args []string) ([]byte, []byte, int, error) {
	path := e.Path
	if path == "" {
		p, err := exec.LookPath("gcloud")
		if err != nil {
			return nil, nil, 0, ErrNoCLI
		}
		path = p
	}
	cmd := exec.CommandContext(ctx, path, args...)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return out.Bytes(), errOut.Bytes(), 0, nil
	case errors.As(err, &ee):
		return out.Bytes(), errOut.Bytes(), ee.ExitCode(), nil
	}
	return nil, nil, 0, fmt.Errorf("run gcloud: %w", err)
}

// Client runs gcloud commands in one project.
type Client struct {
	// Project is the Google Cloud project id, passed as --project. When
	// empty, Account fills it in from gcloud's own configuration.
	Project string
	Run     Runner
}

// New returns a client that runs the gcloud command on PATH.
func New(project string) *Client {
	return &Client{Project: project, Run: Exec{}}
}

// Error is an error gcloud reported. gcloud prints errors as text on
// standard error and gives no status code, so the kinds keelbox acts on
// are told apart by Google's wording (see the Is functions).
type Error struct {
	// Command is the gcloud command: "compute instances create".
	Command string
	// Message is gcloud's error, without its "ERROR: (gcloud...)" prefix.
	Message string
}

func (e *Error) Error() string { return fmt.Sprintf("gcloud %s: %s", e.Command, e.Message) }

// partial is how gcloud reports a list that one of its requests failed
// for. Some list commands (instances, addresses, machine-types) print it
// as a warning, print the rest of the list and exit 0; keelbox treats it
// as the error it is, so a refused request never reads as "nothing there".
const partial = "Some requests did not succeed"

// parseError reads gcloud's standard error. An error is
//
//	ERROR: (gcloud.compute.instances.create) Could not fetch resource:
//	 - The resource 'projects/p/zones/z/instances/x' was not found
//
// or one paragraph of plain text after the prefix, sometimes followed by
// advice. The message is the " - " lines when there are any, else the
// first paragraph.
func parseError(command string, stderr []byte, exit int) *Error {
	e := &Error{Command: command}
	lines := strings.Split(strings.ReplaceAll(string(stderr), "\r\n", "\n"), "\n")
	start := -1
	for i, l := range lines {
		if strings.HasPrefix(l, "ERROR:") || strings.Contains(l, partial) {
			start = i
			break
		}
	}
	if start < 0 {
		// Not gcloud's own format: the last line that says something.
		for i := len(lines) - 1; i >= 0; i-- {
			if l := strings.TrimSpace(lines[i]); l != "" {
				e.Message = l
				break
			}
		}
	} else {
		first := strings.TrimSpace(strings.TrimPrefix(lines[start], "ERROR:"))
		if strings.HasPrefix(first, "(gcloud.") {
			if i := strings.Index(first, ")"); i >= 0 {
				first = strings.TrimSpace(first[i+1:])
			}
		}
		para := []string{strings.TrimPrefix(first, "WARNING: ")}
		var bullets []string
		for _, l := range lines[start+1:] {
			t := strings.TrimSpace(l)
			if t == "" {
				break
			}
			if strings.HasPrefix(t, "- ") {
				bullets = append(bullets, strings.TrimPrefix(t, "- "))
				continue
			}
			if len(bullets) > 0 {
				bullets[len(bullets)-1] += " " + t // a wrapped bullet
				continue
			}
			para = append(para, t)
		}
		if len(bullets) > 0 {
			e.Message = strings.Join(bullets, "; ")
		} else {
			e.Message = strings.Join(para, " ")
		}
	}
	if e.Message == "" {
		e.Message = fmt.Sprintf("exit status %d", exit)
	}
	return e
}

func said(err error, phrases ...string) bool {
	var e *Error
	if !errors.As(err, &e) {
		return false
	}
	for _, p := range phrases {
		if strings.Contains(e.Message, p) {
			return true
		}
	}
	return false
}

// IsNotFound reports the API's answer for a resource that does not exist.
func IsNotFound(err error) bool { return said(err, "was not found", "notFound") }

// isSignedOut reports gcloud without an account, or with a sign-in Google
// no longer accepts.
func isSignedOut(err error) bool {
	return said(err, "do not currently have an active account", "gcloud auth login", "Reauthentication", "refreshing your current auth tokens",
		"invalid_grant", "invalid authentication credentials")
}

// isAPIDisabled reports a project where the Compute Engine API is off.
func isAPIDisabled(err error) bool {
	return said(err, "has not been used in project", "it is disabled", "accessNotConfigured", "SERVICE_DISABLED", "not enabled on project")
}

// isBillingOff reports a project without a billing account.
func isBillingOff(err error) bool {
	return said(err, "billing to be enabled", "Billing account for project", "billingNotEnabled")
}

// isNoProject reports a command that had no project to run in.
func isNoProject(err error) bool { return said(err, "property [project] is not currently set") }

// call runs "gcloud args... --format=json --quiet" and decodes its JSON
// output into out. command is how many leading arguments name the
// command; the rest are its arguments and flags. inProject adds the
// client's project.
func (c *Client) call(ctx context.Context, out any, inProject bool, command int, args ...string) error {
	// --quiet: gcloud never asks a question; a prompt takes its default,
	// and one without a default is an error.
	full := append(append([]string(nil), args...), "--format=json", "--quiet")
	if inProject && c.Project != "" {
		full = append(full, "--project", c.Project)
	}
	name := strings.Join(args[:command], " ")
	stdout, stderr, exit, err := c.Run.Run(ctx, full)
	if err != nil {
		return err
	}
	if exit != 0 || bytes.Contains(stderr, []byte(partial)) {
		if cerr := ctx.Err(); cerr != nil {
			// gcloud was stopped with the context: that is the error, not
			// whatever a killed command printed.
			return fmt.Errorf("gcloud %s: %w", name, cerr)
		}
		return parseError(name, stderr, exit)
	}
	if out == nil || len(bytes.TrimSpace(stdout)) == 0 {
		return nil
	}
	if err := json.Unmarshal(stdout, out); err != nil {
		return fmt.Errorf("gcloud %s: output is not the JSON keelbox expects: %w", name, err)
	}
	return nil
}

// compute runs "gcloud compute <resource> <verb> args..." in the project.
func (c *Client) compute(ctx context.Context, out any, resource, verb string, args ...string) error {
	return c.call(ctx, out, true, 3, append([]string{"compute", resource, verb}, args...)...)
}

// projectFlag is " --project ID" for the fix an error names, or "".
func (c *Client) projectFlag() string {
	if c.Project == "" {
		return ""
	}
	return " --project " + c.Project
}

// Settings are the values of gcloud's active configuration that keelbox
// reads: who is signed in, and the project and zone gcloud would use.
type Settings struct {
	Account string
	Project string
	Zone    string
}

// Settings reads gcloud's active configuration (gcloud config list). It
// reads only what is on this computer.
func (c *Client) Settings(ctx context.Context) (Settings, error) {
	var doc struct {
		Core struct {
			Account string `json:"account"`
			Project string `json:"project"`
		} `json:"core"`
		Compute struct {
			Zone string `json:"zone"`
		} `json:"compute"`
	}
	if err := c.call(ctx, &doc, false, 2, "config", "list"); err != nil {
		return Settings{}, err
	}
	return Settings{Account: doc.Core.Account, Project: doc.Core.Project, Zone: doc.Compute.Zone}, nil
}

// Account is the project keelbox works in and who gcloud is signed in as.
type Account struct {
	// Email is the account gcloud's configuration names; empty when gcloud
	// is signed in another way (an access token file, say).
	Email   string
	Project string
}

// Describe names the project and the account.
func (a Account) Describe() string {
	if a.Email == "" {
		return "project " + a.Project
	}
	return fmt.Sprintf("project %s as %s", a.Project, a.Email)
}

// Errors Account returns, each with its fix in the message.
var (
	// ErrNotSignedIn is gcloud without a working sign-in.
	ErrNotSignedIn = errors.New("not signed in to Google Cloud")
	// ErrNoProject is no project in the config and none set in gcloud.
	ErrNoProject = errors.New("no Google Cloud project")
	// ErrAPIDisabled is a project where the Compute Engine API is off.
	ErrAPIDisabled = errors.New("the Compute Engine API is not enabled")
)

// Account resolves the project (the client's, else the one gcloud is
// configured for, which it then keeps) and asks Compute Engine for it
// (gcloud compute project-info describe). That one call shows that gcloud
// is signed in, that the project exists and that its Compute Engine API
// is on; each failure is an error that names the fix.
func (c *Client) Account(ctx context.Context) (Account, error) {
	set, err := c.Settings(ctx)
	if err != nil {
		return Account{}, err
	}
	if c.Project == "" {
		c.Project = set.Project
	}
	a := Account{Email: set.Account, Project: c.Project}
	if c.Project == "" {
		return a, fmt.Errorf("%w: target.gcp.project is not set and gcloud has no project of its own (gcloud projects list shows yours). Set one: gcloud config set project PROJECT-ID", ErrNoProject)
	}
	var info struct {
		Name string `json:"name"`
	}
	err = c.compute(ctx, &info, "project-info", "describe")
	var ge *Error
	switch {
	case err == nil:
		return a, nil
	case !errors.As(err, &ge):
		return a, err
	case isSignedOut(err):
		// gcloud's own advice follows its first sentence; keelbox gives its own.
		why := strings.TrimSuffix(strings.TrimSpace(strings.TrimSuffix(ge.Message, "Please run:")), ".")
		return a, fmt.Errorf("%w: %s. Sign in: gcloud auth login", ErrNotSignedIn, why)
	case isNoProject(err):
		return a, fmt.Errorf("%w: gcloud has no project set. Set one: gcloud config set project PROJECT-ID", ErrNoProject)
	case isAPIDisabled(err):
		return a, fmt.Errorf("%w in project %s. Enable it: gcloud services enable compute.googleapis.com%s", ErrAPIDisabled, c.Project, c.projectFlag())
	case isBillingOff(err):
		return a, fmt.Errorf("project %s has no billing account, which Compute Engine needs (%s). Link one: https://console.cloud.google.com/billing/linkedaccount?project=%s",
			c.Project, ge.Message, c.Project)
	case IsNotFound(err) || said(err, "permission", "PERMISSION_DENIED", "forbidden", "Forbidden"):
		return a, fmt.Errorf("gcloud's sign-in%s cannot use Compute Engine in project %s (%s). Check the project id (gcloud projects list), "+
			"or ask for the role Compute Admin (roles/compute.admin) on it, then run the command again", as(set.Account), c.Project, ge.Message)
	}
	return a, err
}

func as(account string) string {
	if account == "" {
		return ""
	}
	return " (" + account + ")"
}
