# 0085: attach gives a silent connection up sooner

Date: 2026-11-16. Status: accepted. Amends [ADR 0084](0084-attach-reopens-a-lost-session.md).

## Context
ADR 0084 has `keelbox attach` open a session again after a connection that was lost. ssh decides when that is. With the keepalive every other keelbox connection has, one each 15 seconds and giving up after three go unanswered, a laptop that loses its network would look at a frozen session for 45 seconds or more before keelbox says anything.

45 seconds is right for a connection that cannot be had again: a shell from `keelbox ssh`, a sign-in, an apply being followed. A network that stalls for 20 seconds and recovers should not end those.

## Decision
- **`keelbox attach` on a terminal gives the connection up after about 10 seconds without an answer**: keepalives 5 seconds apart, two unanswered. The session is opened again as ADR 0084 has it, so taking a slow network for a lost one costs a redraw of the screen and nothing on the box.
- **Every other connection keeps 15 seconds and three.**
- The options go before the others on ssh's command line, since ssh keeps the first value it is given.

## Consequences
- With a VM frozen while attached, keelbox says the connection is lost about 14 seconds after the box stops answering, and is back in the session 3 seconds after it answers again.
- A link that drops packets for ten seconds at a time makes attach redraw where it would have ridden it out.
