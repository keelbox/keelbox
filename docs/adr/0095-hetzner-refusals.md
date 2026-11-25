# 0095: A cloud's refusal to create a server leaves nothing behind and offers the ways on

Date: 2026-11-25. Status: accepted. Amends ADR 0025 (keelbox up) and ADR 0035 (answers kept).

## Context
Since June 2026 Hetzner limits new cloud servers for new customers and for some existing ones, per location, citing limited hardware. An affected account cannot create or rescale servers in that location until the limit lifts by itself. Hetzner does not document the API's error for it. Reports name `resource_unavailable` (412) for a type out of stock, `resource_limit_exceeded` (403) for a project limit and `placement_error` (422); none of that is verified. keelbox printed the API error as it came, with no fix. The firewall `create` had made stayed behind. `keelbox up` had written the box's config by then, so it ignored `--location`, `--size` and `--provider` on the next run: the only way to another cloud was `keelbox forget`, then every question again.

## Decision
- **A refusal is a 403, 412 or 422 from the order** (create, resume, change type) that is not about keelbox's own request or token: the three codes above, `forbidden`, and any code keelbox does not know. `invalid_input`, `token_readonly`, `server_not_stopped` and the like stay plain errors. A type that Hetzner's own list marks as not orderable in the location counts too, before anything is sent.
- **The message keeps Hetzner's code and message verbatim**, says that Hetzner may be limiting servers for the account or the location, and says what is left.
- **A refused create deletes the box's firewall.** The box has no server, so the firewall serves nothing; a box that moves to another cloud would leave it in the project for good. The next create makes it again. The project SSH key stays: it is the laptop's, and other boxes use it. A refused resume keeps the firewall, which the parked box keeps with its rules, and the snapshot.
- **Until a box's server exists, up keeps its answers in its progress file** (`Answers`, the flags that give them, no secrets), and `keelbox up BOX --provider OTHER` configures it on another cloud with them: only that cloud's questions and the go-ahead are asked again. On Hetzner, `--location` and `--size` change where the server is created. The progress file says no server exists only after a refusal or before the create started, so this never rewrites the config of a box that may have a server.
- **On a terminal, up asks after a refusal**: another location (then a size, if the one asked for is not sold there), another size in the location, another cloud, or stop. Without a terminal it prints the command for each.
- Resize and resume print the next step for their case: the box keeps its size, or stays parked, and the config keys or `keelbox resize` say how to try elsewhere or later.

## Consequences
- A user whom Hetzner turns away gets to a running box on another location or cloud in the same `keelbox up`, without answering the rest again.
- If Hetzner uses a code keelbox lists as not a refusal, the user sees the plain API error as before.
- The up progress file holds the answers until the server exists. They are the values of flags, which carry no secrets.
