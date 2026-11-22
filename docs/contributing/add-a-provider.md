# Add a provider

A provider is how keelbox gets to a box. Most machines need none: anything reachable over SSH already works with `provider: ssh`. A new provider is worth it when keelbox should **create and destroy** servers through a cloud's API, as it does for Hetzner, AWS, DigitalOcean and Google Cloud.

Open a provider request issue first (there is a form). [CONTEXT.md](../../CONTEXT.md) says what is out of scope.

The worked example below adds `examplecloud`, a made-up API. It compiles and its tests pass; it was verified on a scratch branch and is not part of keelbox. Use [`internal/provider/hetzner/`](../../internal/provider/hetzner/) and `hetznerCloud` in [`internal/cli/hetzner.go`](../../internal/cli/hetzner.go) as the real reference. [`internal/provider/aws/`](../../internal/provider/aws/) is the second: it has no API client of its own and runs the user's AWS CLI instead, behind a `Runner` its tests replace ([ADR 0065](../adr/0065-aws-through-the-aws-cli.md)). [`internal/provider/digitalocean/`](../../internal/provider/digitalocean/) is the third, built the same way on doctl ([ADR 0067](../adr/0067-digitalocean-through-doctl.md)); read it for a cloud whose tags are single strings and whose firewall is a resource of its own. [`internal/provider/gcp/`](../../internal/provider/gcp/) is the fourth, on gcloud ([ADR 0068](../adr/0068-google-cloud-through-gcloud.md)); read it for a CLI that reports errors as text and failed lists as warnings, and for a cloud whose own agent on the image would replace the injected host key.

## What a provider must guarantee

These are the rules reviewers check. They come from [ADR 0005](../adr/0005-security-defaults.md), [0007](../adr/0007-host-key-pinning.md) and AGENTS.md.

1. **Verified first contact.** The server's first host key is generated on the laptop (`provider.GenerateHostKey`), pinned before the server exists, and injected through the cloud's user data. After the first verified connection, `RotateHostKeys` replaces it with keys generated on the server, because user data is readable by any process on most clouds. If a cloud cannot inject a host key, the provider must require `target.ssh.host_key` or the interactive prompt, never `accept-new`.
2. **Only labelled servers are destroyed.** Every server keelbox creates carries a managed label (`keelbox=true`) and the box name. `Find` and `Destroy` refuse anything without them. Where tags are single strings, as on DigitalOcean, the two become the tags `keelbox` and `keelbox-box-<box>`. A resource that cannot carry a label (a DigitalOcean firewall, a Lightsail static IP, a Google Cloud firewall rule or address) is known by its name and by what it is attached to or a description keelbox writes, and both are checked.
3. **The credential never leaks.** Read it from a file refused when group- or world-readable (`hetzner.TokenFromFile` shows how), or an environment variable. Keep it out of errors, output, command lines and the logbook. The API client holds it in an unexported field. Better still is to hold none: a provider that runs the cloud's own CLI, as `aws`, `digitalocean` and `gcp` do, leaves the sign-in with that CLI.
4. **User data holds nothing secret** apart from the single-use host key. keelbox and the config arrive over SSH afterwards.
5. **`create` fails if the server exists**, and on a failure after the server exists says so and points at `apply` or `destroy`.
6. **Everything after create is the ssh provider.** The provider finds the server's address; `cloudCreate` and `runCloud` in [`internal/cli/cloud.go`](../../internal/cli/cloud.go) then run the same `runSSH` and `bootstrapApply` for every cloud.

## 1. Config

In [`internal/config/config.go`](../../internal/config/config.go), add a provider constant, a `target.<provider>` block, defaults and validation:

```go
type Target struct {
	Provider     string       `yaml:"provider"`
	SSH          SSH          `yaml:"ssh,omitempty"`
	Hetzner      Hetzner      `yaml:"hetzner,omitempty"`
	ExampleCloud ExampleCloud `yaml:"examplecloud,omitempty"`
}

// ExampleCloud configures server creation on examplecloud.
type ExampleCloud struct {
	Size      string `yaml:"size,omitempty"`
	Region    string `yaml:"region,omitempty"`
	TokenFile string `yaml:"token_file,omitempty"`
}

const ProviderExample = "examplecloud"

// in ApplyDefaults
	if c.Target.Provider == ProviderExample && c.Target.ExampleCloud.TokenFile == "" {
		c.Target.ExampleCloud.TokenFile = "~/.config/keelbox/examplecloud.token"
	}

// in Validate's provider switch
	case ProviderExample:
		if c.Target.ExampleCloud.Size == "" {
			add("target.examplecloud.size: required")
		}
		if c.Target.ExampleCloud.Region == "" {
			add("target.examplecloud.region: required")
		}
	default:
		add("target.provider: %q is not one of local, ssh, hetzner, examplecloud", c.Target.Provider)
```

Add a row per key to [`docs/reference/config.md`](../reference/config.md); `TestConfigReferenceListsEveryKey` fails until you do.

## 2. The provider package

`internal/provider/examplecloud/examplecloud.go`: a small client over `net/http` (no SDKs: keelbox has one dependency) and `Create`, `Find`, `Destroy`.

```go
// Package examplecloud is a minimal provider for a made-up cloud API: just
// the calls keelbox needs to create, find and destroy a box.
package examplecloud

// Client talks to the API. The token stays in memory and never appears in
// errors or output.
type Client struct {
	Endpoint string
	token    string
	HTTP     *http.Client
}

type Server struct {
	ID     int64             `json:"id"`
	Name   string            `json:"name"`
	IPv4   string            `json:"ipv4"`
	Labels map[string]string `json:"labels"`
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	// JSON request with "Authorization: Bearer <token>"; errors name the
	// method, path and status, never the token.
}

// Create makes the server for a box, with the laptop-generated host key in
// its cloud-init user data, and returns it once it has an address.
func Create(ctx context.Context, cl *Client, c *config.Config, hk provider.HostKeyPair) (*Server, error) {
	if s, err := Find(ctx, cl, c.Box); err == nil && s != nil {
		return nil, fmt.Errorf("a server named %s already exists", c.Box)
	}
	// The cloud-init document every cloud boots a server with.
	userData, err := provider.UserData(c.Box, c.Host.Hostname, hk)
	if err != nil {
		return nil, err
	}
	req := map[string]any{
		"name": c.Box, "size": c.Target.ExampleCloud.Size, "region": c.Target.ExampleCloud.Region,
		"image": "ubuntu-24.04", "user_data": userData,
		"labels": provider.ManagedLabels(c.Box, nil), // keelbox=true, keelbox-box=<box>
	}
	var r struct{ Server Server }
	if err := cl.do(ctx, "POST", "/servers", req, &r); err != nil {
		return nil, err
	}
	return &r.Server, nil
}

// Find returns the keelbox-managed server for a box.
func Find(ctx context.Context, cl *Client, box string) (*Server, error) {
	var r struct{ Servers []Server }
	if err := cl.do(ctx, "GET", "/servers?name="+url.QueryEscape(box), nil, &r); err != nil {
		return nil, err
	}
	if len(r.Servers) == 0 {
		return nil, fmt.Errorf("no server named %s in the account; %s", box, provider.NoServerFix(box))
	}
	if s := r.Servers[0]; !provider.Managed(s.Labels, box) {
		return nil, fmt.Errorf("server %s was not created by keelbox; not touching it", box)
	}
	return &r.Servers[0], nil
}

// Destroy deletes a keelbox-managed server.
func Destroy(ctx context.Context, cl *Client, s *Server) error {
	if s.Labels[provider.LabelManaged] != "true" {
		return fmt.Errorf("refusing to delete server %d: not labelled %s=true", s.ID, provider.LabelManaged)
	}
	return cl.do(ctx, "DELETE", "/servers/"+strconv.FormatInt(s.ID, 10), nil, nil)
}
```

A real cloud also needs: waiting for the create action and a public address, checking the size and image exist in the region before creating (Hetzner's `Create` shows how, with useful errors), the provider's own SSH key mechanism for the bootstrap login (or none: `provider.AdminUserData` boots the server with the admin account, as on DigitalOcean and Google Cloud), and pagination. Hetzner's client is about 400 lines.

## 3. Test against a fake API

`examplecloud_test.go` runs the client against `httptest.NewServer`, as [`hetzner_test.go`](../../internal/provider/hetzner/hetzner_test.go) does. At minimum:

```go
func TestCreateFindDestroy(t *testing.T) {
	srv, servers := fakeAPI(t, "tok") // in-memory server list, bearer-token check,
	cl := New(srv.URL, "tok")         // and a check that user data carries the host key
	c, _ := config.Parse([]byte("version: 1\nbox: acme\ntarget: {provider: examplecloud, examplecloud: {size: small, region: eu1}}\nusers: {admin: {ssh_authorized_keys: [\"ssh-ed25519 AAAA a@b\"]}}\n"), "t")
	ctx := context.Background()
	s, err := Create(ctx, cl, c, provider.HostKeyPair{Private: "priv", Public: "ssh-ed25519 AAAA host"})
	// create works; a second create with the same name fails; Find returns it;
	// Destroy removes it; Destroy refuses an unlabelled server.
}
```

Also test, as Hetzner does: the token never appears in an error (`TestTokenNeverInErrors`), servers without the labels are never touched (`TestNeverTouchesUnmanagedServers`), and the API's validation errors are clear (`TestCreateValidatesAgainstAPI`).

## 4. Wire it into the CLI

A cloud is the `cloud` interface in [`internal/cli/cloud.go`](../../internal/cli/cloud.go). The commands are written once against it: `cloudCreate` (generate and pin the host key, wait for SSH, rotate the host keys, first apply), `cloudDestroy` (the work check of ADR 0064, the confirmation, forgetting the pinned keys), `cloudPublic` (the address, and `target.ssh.host` with `CopyPins`), and `runCloud` for everything else. A provider implements the interface in its own file, as `hetznerCloud` does in [`hetzner.go`](../../internal/cli/hetzner.go):

```go
// exampleCloud is examplecloud as a cloud.
type exampleCloud struct {
	cl  *examplecloud.Client
	cfg *config.Config
}

func (e *exampleCloud) title() string         { return "examplecloud" }
func (e *exampleCloud) bootstrapUser() string { return "root" } // the image's own account

func (e *exampleCloud) create(ctx context.Context, hk provider.HostKeyPair, logf func(string, ...any)) (*cloudServer, error) {
	srv, err := examplecloud.Create(ctx, e.cl, e.cfg, hk)
	return exampleServer(srv), err // a server returned with an error may exist
}

func (e *exampleCloud) find(ctx context.Context) (*cloudServer, error) {
	srv, err := examplecloud.Find(ctx, e.cl, e.cfg.Box)
	if err != nil {
		return nil, err
	}
	return exampleServer(srv), nil
}

func (e *exampleCloud) destroy(ctx context.Context, srv *cloudServer, logf func(string, ...any)) error {
	// The server, then everything create made with it (a firewall, an address).
	return examplecloud.Destroy(ctx, e.cl, srv.raw.(*examplecloud.Server))
}

func exampleServer(s *examplecloud.Server) *cloudServer {
	if s == nil {
		return nil
	}
	return &cloudServer{ref: fmt.Sprintf("server %d", s.ID), about: fmt.Sprintf("server %s (id %d, %s)", s.Name, s.ID, s.IPv4), ip: s.IPv4, raw: s}
}
```

The interface has three more methods: `sweep` removes what an interrupted create left when there is no server, `firewall` keeps the cloud's own firewall in step with the box (SSH admitted until the box has closed public SSH, [ADR 0022](../adr/0022-hetzner-cloud-firewall.md)), and `doctor` checks it from the laptop. A cloud without a firewall of its own returns no changes and prints no rows.

### Resize, park and resume

The rest of the interface is what `keelbox resize`, `park` and `resume` need ([ADR 0071](../adr/0071-resize-park-resume.md)). The commands themselves are written once, in [`resize.go`](../../internal/cli/resize.go) and [`park.go`](../../internal/cli/park.go): they own the order of the calls, the questions, the messages, the pinned host keys and the laptop's note. A provider supplies the calls to its cloud, in a file of its own beside its cloud file, as [`hetznerpark.go`](../../internal/cli/hetznerpark.go), [`awspark.go`](../../internal/cli/awspark.go), [`digitaloceanpark.go`](../../internal/cli/digitaloceanpark.go) and [`gcppark.go`](../../internal/cli/gcppark.go) do, with the cloud calls themselves in the provider package's `park.go`. Each call that changes or deletes something checks keelbox's labels for the box first.

| Method | What it must guarantee |
|---|---|
| `sizeKey() string` | The config key that holds the size, as a dotted path (`target.hetzner.server_type`). resize rewrites that one value in the file. |
| `sizes(ctx, srv)` | The sizes `srv` can change to without losing its disk, cheapest first, the current one included and marked; and a line about the prices, which ends with `sizeAdvice`. A size is a `cloudSize`, in parts and not as a sentence: its cores, its memory, what else it comes with, its price a month with the currency (0 when the cloud gives none), whether it is small for agents, and `disk`, the size of **the box's** disk at that size. Where a resize leaves the disk alone that is the disk the server has now, the same for every size, and the commands then name it once, under what stays; give a size's own disk only where the box gets it (Lightsail). Where the cloud can change a server back down, list only sizes that leave that possible (Hetzner never grows the disk); a cloud that cannot go back says so in the line about the prices. The shared code makes the menu's columns, the `from` and `to` rows of the card before resize's question and the rows of the card it ends on from these parts. |
| `stop(ctx, srv, logf)` | Shuts down through the operating system and returns once the cloud reports the server stopped. A server already stopped is not an error. |
| `start(ctx, srv)` | Powers on and returns the server once it runs and has an address. A server already running is not an error. |
| `resize(ctx, srv, size, logf)` | Changes a stopped server to one of the listed sizes, keeping its disk, and returns the server, still stopped: the same one, or the one that replaced it on a cloud that resizes by replacing (Lightsail's comes back running, which `start` takes as it is). After an error the box still has a server of its old size, stopped; a cloud that resizes by replacing and could create no server again returns `errLeftParked` instead (below). |
| `parkCost(ctx, srv)` | A `parkBill`: what parking saves, the server's price a month and a day (`serverPrice`), and what stays billed, the snapshot's price and the most it can come to where it is billed for what the disk holds (`snapshotAtMost`). They are the `saves` and `still billed` rows of the card before park's question, where the first amount a month is shown in bold and a URL as a link, and `server` is the price again for the card park ends on. Where the cloud gives no price, say so in the row and name its price list; a price that cannot be read is left out and never fails the park. It is the first call `park` makes that can fail, before the question, so it is also where a provider refuses a server park could not keep whole (Google Cloud: a VM with a second disk). |
| `typical()` | How long a resize, a park and a resume take on this cloud, each as it reads after "typically" (`1 to 2 minutes`): what the commands took in keelbox's own runs on a box with little on its disk, from [testing](testing.md#testresizeparkresume). The cards say them before the question, so a range that a run falls outside of is a bug in the docs and here. |
| `snapshot(ctx, srv, logf)` | Saves a stopped server's disk as the box's parked snapshot, labelled with `provider.ParkedLabels`, and returns **only once the cloud reports it complete and usable** to create a server from. After an error no snapshot of this call is left. `park` deletes the server on the strength of this return value. |
| `release(ctx, srv, logf)` | Deletes the server and gives up its address, with whatever else the cloud bills without a server (Lightsail's static IP, Google Cloud's disk and reserved address), and returns once the server is gone. Keeps the snapshot and whatever `restore` attaches the new server to (the firewall). |
| `parked(ctx)` | The newest complete snapshot with the box's parked labels, or nil. It reads the cloud only, so it answers the same from any computer. |
| `restore(ctx, p, logf)` | Creates the server from the snapshot with the name, size, place, labels and firewall of the config, and `provider.ResumeUserData` as user data, so the SSH host keys on the disk are kept and cloud-init creates no account. The firewall is in force before the first boot where the cloud takes it at creation or applies it by label (Hetzner, Google Cloud); where it cannot (DigitalOcean, Lightsail), `restore` puts it on as soon as the cloud accepts it and returns only once it is in force. Adds no login the disk does not have. Returns the server once it runs and has an address; fails when the box has a server. |
| `discard(ctx, p, logf)` | Deletes the parked snapshot, and refuses anything without the box's parked labels. |

`find` also sets `cloudServer.stopped` when the cloud reports the server shut down: `resume` on a box that is not parked starts such a server, which is the way back after a park or resize that could not start it again.

The calls that take `logf` get the view's, not a printer: what a provider logs is a line of the log off a terminal and a line under the running step on one ([`parksteps.go`](../../internal/cli/parksteps.go)), so log what was done in the past tense, one fact a line, and start a line with `warning` if it must stand out. A deletion's first line names its step ("deleted acme (id 42)").

What the shared code does with them: `resize` is `stop`, `resize`, `start`, then the normal apply, and `start` again at the old size if the change fails. `park` is `stop`, `snapshot`, `release`, and `start` again if anything before `release` fails. `resume` is `parked`, `restore` (or `start`, when a server is already there), a wait for SSH, the normal apply, and `discard` only after that apply passed. `destroy` on a parked box is `discard` and `sweep`, so `sweep` should also remove any snapshot with the box's parked labels that is not complete.

Three things a cloud may need beyond these calls are optional. The shared code handles each once, so a provider only says that it applies:

- `hostKeyKeeper` ([`park.go`](../../internal/cli/park.go)): for a cloud whose user data cannot tell a new server to keep the SSH host keys. `hostKeysFile()` names the file that must be on the box before its disk boots on another server, and why. `resize` and `park` check over SSH that the box has it and stop before any change if it does not, with `keelbox apply` as the fix. AWS implements it: Lightsail's user data is a shell script, so `ResumeUserData` cannot be sent there and a cloud-init file on the disk says the same.
- `resizeWarner` ([`resize.go`](../../internal/cli/resize.go)): for a cloud whose resize does more than change the size. `resizeWarning()` returns a sentence that is the `warning` row of the card before resize's question, and a `note:` with `--yes`. The card of such a cloud has no `change back` row, since no resize changes the box back. AWS implements it, to say that the instance is replaced, the disk grows and the box cannot be made smaller again.
- `errLeftParked` ([`cloud.go`](../../internal/cli/cloud.go)): the error `resize` returns on a cloud that resizes by replacing the server, when the old server is deleted and none could be created from its disk again. It carries the parked snapshot that holds the disk; the shared code notes the box as parked and names `keelbox resume`. For that to be true the provider must mark its resize snapshot with the parked labels before it deletes the server, as `aws.Resize` does.

For the end-to-end test, add `SizeKey`, `Size`, `BiggerSize` and `Holds` to the cloud's e2e target (the harness's `parker` interface) so that `TestResizeParkResume` runs for it, and make its sweep delete parked snapshots. A target whose cloud cannot change a server back to a smaller size also implements the optional `oneWay` interface (`CannotShrink`): the test then checks that the smaller size is refused instead of resizing down ([testing](testing.md)).

Then:

- [`cloud.go`](../../internal/cli/cloud.go): a case in `openCloud` that builds the provider with its credential, and one entry in `cloudEntries`, which also makes it a cloud for `isCloud`. That is all `create`, `destroy`, `apply`, `doctor`, `login`, `attach`, `sessions` and `ssh` need.
- [`cli.go`](../../internal/cli/cli.go), `Main`: the error messages of `create` and `destroy` for providers that have no servers to create.
- [`statusall.go`](../../internal/cli/statusall.go), `statusAllPublic`: find the address without a prompt, for `keelbox ls`.
- The `create` and `destroy` lines of the usage text, and [`docs/reference/cli.md`](../reference/cli.md) with them (`TestCLIReferenceMatchesHelp`).
- `keelbox up`: the `cloudEntries` entry is the provider's row in the "Which cloud?" question and the `--provider` value. Its `name` is the row's label; `needs` returns a few words on what the cloud needs on the user's computer and whether that is there, without a network call (a CLI on `PATH`, a stored token), and decides the default; `ask` puts the provider's own questions. A provider whose CLI is missing stays in the list, and `ask` stops with a `needsYouError` that says how to install it. Add its steps to `routeSteps` and its lines to the summary in `confirmPlan` ([`up.go`](../../internal/cli/up.go)).

## 5. End to end

Add a target to [`test/e2e/targets_test.go`](../../test/e2e/targets_test.go) implementing `target` (`Create`, `CreateApplies`, `Destroy`, `Spend`): pick the cheapest suitable size, label servers so a sweep can delete leftovers, refuse to run when leftovers exist, and report spend. Select it with `KEELBOX_E2E_TARGET`, add a `make e2e-<provider>` line to the [Makefile](../../Makefile) and `scripts/e2e.sh`. Run it once before the pull request and paste the summary. A provider that runs a cloud's CLI also gets a contract test against the real binary and a stub API, skipped unless an environment variable names the binary ([testing](testing.md)): the fake only imitates the CLI, and this checks the commands, flags and output against the real one without an account.

## 6. Docs

- [Getting started](../getting-started.md): a path for the new cloud, verified by following it.
- [Config reference](../reference/config.md), [command reference](../reference/cli.md).
- An ADR if the cloud forces a different trust decision (for example, no way to inject a host key).

Commit as `feat(provider): add examplecloud`.
