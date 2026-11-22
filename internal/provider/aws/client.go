// Package aws creates, finds and destroys a box on AWS, as an Amazon
// Lightsail instance, by running the user's installed AWS CLI
// (docs/adr/0065-aws-through-the-aws-cli.md). keelbox holds no AWS
// credential and signs no request: the aws command uses whatever sign-in
// the user already has (access keys, single sign-on, aws login, MFA),
// and keelbox reads only its JSON output.
package aws

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// InstallURL is where the AWS CLI's install instructions are.
const InstallURL = "https://docs.aws.amazon.com/cli/latest/userguide/getting-started-install.html"

// Runner runs the aws command with arguments and returns what it printed
// and its exit code. err is set only when the command could not be run.
type Runner interface {
	Run(ctx context.Context, args []string) (stdout, stderr []byte, exit int, err error)
}

// Exec is the Runner that runs the aws command: the one at Path, or the
// one found on PATH when Path is empty.
type Exec struct {
	Path string
}

// ErrNoCLI is the AWS CLI missing from PATH.
var ErrNoCLI = errors.New("the AWS CLI (the aws command) is not installed or not on PATH; keelbox creates AWS servers through it. " +
	"Install it: " + InstallURL + ", then sign in: aws login")

// Installed reports whether the aws command is on PATH.
func Installed() bool {
	_, err := exec.LookPath("aws")
	return err == nil
}

// Run implements Runner.
func (e Exec) Run(ctx context.Context, args []string) ([]byte, []byte, int, error) {
	path := e.Path
	if path == "" {
		p, err := exec.LookPath("aws")
		if err != nil {
			return nil, nil, 0, ErrNoCLI
		}
		path = p
	}
	cmd := exec.CommandContext(ctx, path, args...)
	// No pager, and errors in the form "An error occurred (Code) when
	// calling the Op operation: message" whatever the user's own setting.
	cmd.Env = append(os.Environ(), "AWS_PAGER=", "AWS_CLI_ERROR_FORMAT=legacy")
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
	return nil, nil, 0, fmt.Errorf("run aws: %w", err)
}

// Client runs Lightsail calls in one region with one profile.
type Client struct {
	// Profile is the AWS CLI profile, passed as --profile; empty uses the
	// CLI's own default (AWS_PROFILE, then "default").
	Profile string
	// Region is the AWS region, passed as --region.
	Region string
	Run    Runner
}

// New returns a client that runs the aws command on PATH.
func New(profile, region string) *Client {
	return &Client{Profile: profile, Region: region, Run: Exec{}}
}

// Error is an error the AWS CLI reported.
type Error struct {
	// Code is the service's error code, such as NotFoundException; empty
	// when the CLI failed before a service answered.
	Code string
	// Message is the service's message, or the CLI's own error line.
	Message string
	// Exit is the CLI's exit code: 252 for arguments it refuses, 253 for
	// missing credentials or region, 254 for an error a service returned,
	// 255 for a profile it does not have or an endpoint that does not
	// answer.
	Exit int
	// Command is the service and operation: "lightsail get-instance".
	Command string
}

func (e *Error) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("aws %s: %s: %s", e.Command, e.Code, e.Message)
	}
	return fmt.Sprintf("aws %s: %s", e.Command, e.Message)
}

// serviceError matches the CLI's line for an error a service returned.
var serviceError = regexp.MustCompile(`An error occurred \(([A-Za-z0-9.]+)\)(?: when calling the [\w-]+ operation)?(?: \([^)]*\))?: (.*)`)

func parseError(command string, stderr []byte, exit int) *Error {
	text := strings.TrimSpace(string(stderr))
	e := &Error{Exit: exit, Command: command}
	if m := serviceError.FindStringSubmatch(text); m != nil {
		e.Code, e.Message = m[1], strings.TrimSpace(m[2])
		return e
	}
	// An error of the CLI's own is one marked line, with usage text after
	// or before it. Without a marked line, the last line.
	lines := strings.Split(text, "\n")
	e.Message = strings.TrimSpace(lines[len(lines)-1])
	for _, l := range lines {
		l = strings.TrimSpace(l)
		m, ok := strings.CutPrefix(l, "aws: [ERROR]: ")
		if !ok {
			m, ok = strings.CutPrefix(l, "aws: error: ")
		}
		if ok {
			e.Message = m
			break
		}
	}
	if e.Message == "" {
		e.Message = fmt.Sprintf("exit status %d", exit)
	}
	return e
}

// IsCode reports whether err is an AWS error with one of the codes.
func IsCode(err error, codes ...string) bool {
	var e *Error
	if !errors.As(err, &e) {
		return false
	}
	for _, c := range codes {
		if e.Code == c {
			return true
		}
	}
	return false
}

func (c *Client) global(region string) []string {
	args := []string{"--output", "json", "--no-cli-pager", "--cli-connect-timeout", "20", "--cli-read-timeout", "60"}
	if region != "" {
		args = append(args, "--region", region)
	}
	if c.Profile != "" {
		args = append(args, "--profile", c.Profile)
	}
	return args
}

// call runs "aws <service> <operation> args..." in region and decodes its
// JSON output into out.
func (c *Client) call(ctx context.Context, region string, out any, service, operation string, args ...string) error {
	full := append([]string{service, operation}, args...)
	full = append(full, c.global(region)...)
	stdout, stderr, exit, err := c.Run.Run(ctx, full)
	if err != nil {
		return err
	}
	if exit != 0 {
		return parseError(service+" "+operation, stderr, exit)
	}
	if out == nil || len(bytes.TrimSpace(stdout)) == 0 {
		return nil
	}
	if err := json.Unmarshal(stdout, out); err != nil {
		return fmt.Errorf("aws %s %s: output is not the JSON keelbox expects: %w", service, operation, err)
	}
	return nil
}

// lightsail runs a Lightsail operation in the client's region.
func (c *Client) lightsail(ctx context.Context, out any, operation string, args ...string) error {
	return c.call(ctx, c.Region, out, "lightsail", operation, args...)
}

// profileFlag is " --profile NAME" for the fix an error names, or "".
func (c *Client) profileFlag() string {
	if c.Profile == "" {
		return ""
	}
	return " --profile " + c.Profile
}

// ConfiguredRegion is the region the AWS CLI itself uses when a command
// names none, or "" when it has none: AWS_REGION, then AWS_DEFAULT_REGION,
// then the profile's region. "aws configure get region" reads only the
// configuration file, so the environment is read here.
func (c *Client) ConfiguredRegion(ctx context.Context) string {
	for _, k := range []string{"AWS_REGION", "AWS_DEFAULT_REGION"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	args := []string{"configure", "get", "region"}
	if c.Profile != "" {
		args = append(args, "--profile", c.Profile)
	}
	out, _, exit, err := c.Run.Run(ctx, args)
	if err != nil || exit != 0 {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// Identity is who the AWS CLI is signed in as.
type Identity struct {
	Account string `json:"Account"`
	Arn     string `json:"Arn"`
	UserID  string `json:"UserId"`
}

// ErrNotSignedIn is the AWS CLI without working credentials.
var ErrNotSignedIn = errors.New("not signed in to AWS")

// Identity asks AWS who the CLI is signed in as (sts get-caller-identity,
// which needs no permission). A failure is ErrNotSignedIn with the CLI's
// reason and the commands that sign in.
func (c *Client) Identity(ctx context.Context) (Identity, error) {
	var id Identity
	region := c.Region
	if region == "" {
		region = catalogRegion // STS answers the same in every region
	}
	err := c.call(ctx, region, &id, "sts", "get-caller-identity")
	if err == nil {
		return id, nil
	}
	var ae *Error
	if !errors.As(err, &ae) {
		return id, err
	}
	who := "the default profile"
	if c.Profile != "" {
		who = "profile " + c.Profile
	}
	p := c.profileFlag()
	if strings.Contains(ae.Message, "could not be found") && c.Profile != "" {
		return id, fmt.Errorf("%w: the AWS CLI has no profile named %s (aws configure list-profiles lists them). Create it: aws configure sso%s, or aws configure%s",
			ErrNotSignedIn, c.Profile, p, p)
	}
	return id, fmt.Errorf("%w with %s (%s). Sign in with one of: aws login%s; aws sso login%s (after aws configure sso%s); aws configure%s (access keys)",
		ErrNotSignedIn, who, ae.Message, p, p, p, p)
}
