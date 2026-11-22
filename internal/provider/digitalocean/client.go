// Package digitalocean creates, finds and destroys a box on DigitalOcean,
// as a Droplet, by running the user's installed doctl CLI
// (docs/adr/0067-digitalocean-through-doctl.md). keelbox holds no
// DigitalOcean credential and sends no request itself: doctl uses the
// token of the authentication context the user already has, and keelbox
// reads only its JSON output.
package digitalocean

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// InstallURL is where doctl's install instructions are.
const InstallURL = "https://docs.digitalocean.com/reference/doctl/how-to/install/"

// Runner runs the doctl command with arguments and returns what it printed
// and its exit code. err is set only when the command could not be run.
type Runner interface {
	Run(ctx context.Context, args []string) (stdout, stderr []byte, exit int, err error)
}

// Exec is the Runner that runs doctl: the one at Path, or the one found on
// PATH when Path is empty.
type Exec struct {
	Path string
}

// ErrNoCLI is doctl missing from PATH.
var ErrNoCLI = errors.New("doctl (the DigitalOcean CLI) is not installed or not on PATH; keelbox creates DigitalOcean servers through it. " +
	"Install it: " + InstallURL + ", then sign in: " + loginCommand(Scopes))

// Installed reports whether doctl is on PATH.
func Installed() bool {
	_, err := exec.LookPath("doctl")
	return err == nil
}

// Run implements Runner.
func (e Exec) Run(ctx context.Context, args []string) ([]byte, []byte, int, error) {
	path := e.Path
	if path == "" {
		p, err := exec.LookPath("doctl")
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
	return nil, nil, 0, fmt.Errorf("run doctl: %w", err)
}

// Client runs doctl commands with one authentication context.
type Client struct {
	// Context is the doctl authentication context, passed as --context;
	// empty uses doctl's current one.
	Context string
	Run     Runner
}

// New returns a client that runs the doctl command on PATH.
func New(context string) *Client {
	return &Client{Context: context, Run: Exec{}}
}

// Error is an error doctl reported.
type Error struct {
	// Status is the HTTP status of the API's answer; 0 when doctl failed
	// before the API answered.
	Status int
	// Title is the API's short name for the error, such as "Not Found".
	Title string
	// Message is the API's message, or doctl's own.
	Message string
	// Command is the doctl command: "compute droplet get".
	Command string
}

func (e *Error) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("doctl %s: %d %s", e.Command, e.Status, e.Message)
	}
	return fmt.Sprintf("doctl %s: %s", e.Command, e.Message)
}

// parseError reads a failed command's output. With --output json doctl
// prints {"errors":[{"detail":...,"title":...,"reason":...,"status":...}]}
// on standard output and nothing on standard error; a failure before its
// own error handling (an unknown flag) is plain text on standard error.
func parseError(command string, stdout, stderr []byte, exit int) *Error {
	e := &Error{Command: command}
	var doc struct {
		Errors []struct {
			Detail string `json:"detail"`
			Title  string `json:"title"`
			Reason string `json:"reason"`
			Status int    `json:"status"`
		} `json:"errors"`
	}
	if json.Unmarshal(bytes.TrimSpace(stdout), &doc) == nil && len(doc.Errors) > 0 {
		d := doc.Errors[0]
		e.Status, e.Title, e.Message = d.Status, d.Title, d.Reason
		if e.Message == "" {
			e.Message = d.Detail
		}
	}
	if e.Message == "" {
		text := strings.TrimSpace(string(stderr))
		if text == "" {
			text = strings.TrimSpace(string(stdout))
		}
		// The first line says what failed; usage follows an unknown flag.
		e.Message = strings.TrimPrefix(strings.TrimSpace(strings.SplitN(text, "\n", 2)[0]), "Error: ")
	}
	if e.Message == "" {
		e.Message = fmt.Sprintf("exit status %d", exit)
	}
	return e
}

// IsStatus reports whether err is a doctl error with that HTTP status.
func IsStatus(err error, status int) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == status
}

// notFound reports the API's answer for a resource that does not exist.
func notFound(err error) bool { return IsStatus(err, 404) }

// call runs "doctl args... --output json" with the client's context and
// decodes its JSON output into out. command is how many leading
// arguments name the command; the rest are its arguments and flags.
func (c *Client) call(ctx context.Context, out any, command int, args ...string) error {
	// --interactive=false: doctl never asks a question, whatever its
	// config file says.
	full := append(append([]string(nil), args...), "--output", "json", "--interactive=false")
	if c.Context != "" {
		full = append(full, "--context", c.Context)
	}
	name := strings.Join(args[:command], " ")
	stdout, stderr, exit, err := c.Run.Run(ctx, full)
	if err != nil {
		return err
	}
	if exit != 0 {
		return c.refused(parseError(name, stdout, stderr, exit))
	}
	if out == nil || len(bytes.TrimSpace(stdout)) == 0 {
		return nil
	}
	if err := json.Unmarshal(stdout, out); err != nil {
		return fmt.Errorf("doctl %s: output is not the JSON keelbox expects: %w", name, err)
	}
	return nil
}

// compute runs "doctl compute <resource> <verb> args...".
func (c *Client) compute(ctx context.Context, out any, resource, verb string, args ...string) error {
	return c.call(ctx, out, 3, append([]string{"compute", resource, verb}, args...)...)
}

// contextFlag is " --context NAME" for the fix an error names, or "".
func (c *Client) contextFlag() string {
	if c.Context == "" {
		return ""
	}
	return " --context " + c.Context
}

// Account is who doctl is signed in as.
type Account struct {
	Email        string `json:"email"`
	UUID         string `json:"uuid"`
	Status       string `json:"status"`
	DropletLimit int    `json:"droplet_limit"`
	Team         struct {
		Name string `json:"name"`
		UUID string `json:"uuid"`
	} `json:"team"`
}

// Describe names the account: the team when it has one, and the email.
func (a Account) Describe() string {
	switch {
	case a.Team.Name != "" && a.Email != "":
		return fmt.Sprintf("team %s as %s", a.Team.Name, a.Email)
	case a.Email != "":
		return a.Email
	case a.Team.Name != "":
		return "team " + a.Team.Name
	}
	return "an account doctl does not name"
}

// ErrNotSignedIn is doctl without a working token.
var ErrNotSignedIn = errors.New("not signed in to DigitalOcean")

// Account asks DigitalOcean who doctl is signed in as (doctl account
// get). Without a token, or with one the API refuses, the error is
// ErrNotSignedIn with doctl's reason and the command that signs in.
func (c *Client) Account(ctx context.Context) (Account, error) {
	var a Account
	err := c.call(ctx, &a, 2, "account", "get")
	if err == nil {
		return a, nil
	}
	var de *Error
	if !errors.As(err, &de) {
		return a, err
	}
	who := c.who()
	switch {
	case de.Status == 0 && strings.Contains(de.Message, "access token is required"):
		// doctl says the same for a context it does not have.
		return a, fmt.Errorf("%w: doctl has no token for %s (doctl auth list shows the contexts it has). %s", ErrNotSignedIn, who, c.signIn("Sign in", Scopes))
	case de.Status == 401:
		return a, fmt.Errorf("%w: DigitalOcean refused the token of %s (%s); it was deleted or has expired. %s", ErrNotSignedIn, who, de.Message, c.signIn("Sign in again", Scopes))
	case de.Status == 403:
		return a, fmt.Errorf("the sign-in of %s may not read the account (%s). keelbox needs these scopes: %s. %s",
			who, de.Message, Scopes, c.signIn("Sign in again with them", Scopes))
	}
	return a, err
}

// ScopeError is the API's 403 for a call: the token has custom scopes and
// lacks the one this call needs. Its message ends with the scopes keelbox
// needs and how to save a token that has them. The API's own answer is
// underneath, so IsStatus and errors.As still see it.
type ScopeError struct {
	Err *Error
	// Fix is what to do about it.
	Fix string
}

func (e *ScopeError) Error() string { return e.Err.Error() + ". " + e.Fix }
func (e *ScopeError) Unwrap() error { return e.Err }

// who names the doctl sign-in in use.
func (c *Client) who() string {
	if c.Context != "" {
		return "the doctl context " + c.Context
	}
	return "doctl's current context"
}

// refused turns the API's 403 into a ScopeError that names every scope
// keelbox needs: DigitalOcean does not say which one was missing, and a
// token's scopes cannot be read back.
func (c *Client) refused(err error) error {
	var de *Error
	if !errors.As(err, &de) || de.Status != 403 {
		return err
	}
	return &ScopeError{Err: de, Fix: fmt.Sprintf("The sign-in of %s lacks a scope this call needs. keelbox needs these scopes: %s. %s",
		c.who(), Scopes, c.signIn("Sign in again with them", Scopes))}
}

// loginCommand is doctl's browser sign-in, asking for scopes (a
// comma-separated list, as Scopes is). Without --scopes the permissions are
// chosen on DigitalOcean's page, where it is easy to grant too few.
func loginCommand(scopes string) string {
	return `doctl auth login --scopes "` + strings.ReplaceAll(scopes, ", ", " ") + `"`
}

// signIn is the fix for a sign-in that is missing or lacks scopes: doctl's
// browser sign-in with the scopes named, and for a doctl that has no
// auth login, a token made by hand. lead is how the sentence starts.
func (c *Client) signIn(lead, scopes string) string {
	f := c.contextFlag()
	return fmt.Sprintf("%s: %s%s. With a doctl that has no auth login, create a token with those scopes at %s and save it: doctl auth init%s",
		lead, loginCommand(scopes), f, TokensURL, f)
}

// TokensURL is where DigitalOcean API tokens are created.
const TokensURL = "https://cloud.digitalocean.com/account/api/tokens" //nolint:gosec // G101: a page, not a credential

// Scopes are the custom token scopes that cover every call keelbox makes
// through doctl: the ones a box needs from create to destroy, then
// ParkScopes.
const Scopes = "account:read, regions:read, sizes:read, image:read, droplet:create, droplet:read, droplet:delete, " +
	"firewall:create, firewall:read, firewall:update, firewall:delete, tag:create, tag:read, tag:delete, " + ParkScopes

// ParkScopes are the scopes only keelbox resize, park and resume need: a
// Droplet is shut down, resized and started with droplet:update, a
// snapshot is an image that is created, tagged and deleted.
const ParkScopes = "droplet:update, image:create, image:update, image:delete"
