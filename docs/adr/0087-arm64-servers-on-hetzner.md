# 0087: Arm64 servers are offered on Hetzner

Date: 2026-11-17. Status: accepted.

## Context
keelbox builds and pins everything for arm64: its own binary, Node, pnpm, Docker and the rest each have an arm64 checksum, and the Hetzner code picks the image and the resize and resume types by the server's architecture.

Hetzner's Arm64 line is the cheapest way to 4 GB after its cost-optimised x86 line, and the two sell out separately. A user who finds the cheap x86 types sold out needs an Arm64 type to pick.

## Decision
- **The size menu lists Arm64 types beside x86 ones**, each marked `Arm64`, cheapest first, x86 first at one price.
- **The preselected size is x86**: the cheapest with 2 vCPUs and 4 GB. Arm64 is the user's choice, because their own container images and binaries need an arm64 build there, and keelbox cannot know whether they have one. The hint above the menu says so. Where no x86 type of that size can be ordered, the cheapest Arm64 one is preselected.
- **`--size` takes an Arm64 type** as it takes any other the location can deliver.

## What is tested
- An arm64 box end to end over SSH, on a Google Cloud T2A VM with Ubuntu 24.04: `keelbox up` sets it up, doctor reports no failures, and Claude Code, Codex, Node, a container under the agent's rootless Docker, the bwrap sandbox and doctor's Codex sandbox check all run.
- Creating an Arm64 server on Hetzner is not tested end to end. The create call differs from the x86 one in the server type's name and the image's architecture, which the code chooses by type.

## Consequences
- A box on Arm64 runs arm64 images only. An image without an arm64 build fails to start with Docker's own message.
- Resize and resume keep to the server's architecture: a box does not move between x86 and Arm64.
