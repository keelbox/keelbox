# 0082: The agent has a memory ceiling on every box

Date: 2026-11-16. Status: accepted. Amends [ADR 0048](0048-agent-limits.md).

## Context
ADR 0048 gave the agent limits only where the config set `users.agent.limits`, which `keelbox up` proposes on a machine that runs other services. On a box of its own the agent had none.

An agent on a box with 16 GB that runs three sub-agents, a Docker stack and a dev server can use all the memory. Before the kernel stops anything the whole box stalls: sshd and Tailscale have no memory to answer from, and the user's SSH session times out. A box of its own has other services too, the ones the user reaches it through.

## Decision
- **Without `users.agent.limits.memory` the ceiling is 90% of RAM.** That holds with no `limits` key and with limits that leave memory out. The kernel stops the agent's largest process at the ceiling, and the agent may use the same share of swap.
- **The default is a ceiling alone, with no slow-down mark.** ADR 0048 and 0052 put `MemoryHigh` at 90% of a ceiling. On a 4 GB box with the mark under the default ceiling, one process that keeps allocating reaches the mark and stays there: the kernel holds it back so hard that it does not reach the ceiling in five minutes, and a 50 MB allocation in another tmux pane takes 213 seconds. With the mark lifted the kernel stops that process within a second, and its neighbour in the pane keeps running. A logged-in admin answers in 0.2 seconds in both cases.
- **A ceiling the config sets keeps its mark** (ADR 0052).
- **Logins get the lower weight against system services** that any limits bring. There is no CPU cap.
- **`memory: none` lifts the ceiling.** With nothing else under `limits`, the agent has no limits at all and keelbox removes its drop-ins. `keelbox up --limits none` writes that line.
- **A percentage, not a reserve in bytes.** One number is what the config, the primer and `keelbox status` then all say.

## Consequences
- An apply that puts the ceiling on a box whose agent already uses more than 90% of RAM stops its largest process.
- On a small box the tenth is small: 100 MB of 1 GB. It is still more than an agent without a ceiling leaves the system.
- The agent has less memory than the machine: 14.4 GB of 16. The primer tells it the figure.
- Leaving `memory` out and writing `memory: 90%` differ: the second has the slow-down mark.
