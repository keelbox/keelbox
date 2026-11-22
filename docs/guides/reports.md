# Reports

```sh
keelbox reports acme             # list the drafts the agent saved
keelbox reports acme --show 1    # read one
keelbox reports acme --send 1    # review it, then open it as a GitHub issue
keelbox reports acme --delete 1  # delete it
```

When keelbox gets in an agent's way, the agent can save the problem as a draft report for keelbox's issue tracker. A report leaves the box only when you send it.

## How a report is made

The primer, the file that tells agents how the box works, tells the agent to ask you first and to file a report only after your yes. The agent then runs `keelbox report "TITLE" --area AREA --detail "exact error"`.

The draft stays on the box, readable only by root, and no notification goes out. The box adds the keelbox and Ubuntu versions, the architecture, CPUs and memory, and the names of checks that failed at the last scheduled doctor run. It accepts at most 5 reports a day and keeps at most 30.

## Send one

`--send` needs a terminal. keelbox replaces tokens, keys, email addresses, Tailscale names and addresses, public IP addresses, the box name and home paths, and asks whether to keep project names (default no). It shows the whole text with what it replaced, offers to open it in `$VISUAL` or `$EDITOR`, and checks your edit again.

It then asks whether to open a GitHub issue (default no); issues on github.com/keelbox/keelbox are public. On a yes your browser opens a new-issue form filled with the text you saw, or its start if the link cannot hold it all. You submit it there or close the tab.

Reports in the `security` area open keelbox's private advisory form instead, for you to paste the text into. Without a browser, keelbox prints the link.

Read the text before you say yes. The agent's text is not checked, and a repository the agent works in can tell it to put a secret in a report. Redaction catches only the kinds of text listed above.

## Turn it off

```yaml
reports:
  enabled: false
```

After `keelbox apply acme`, the primer stops mentioning reports and `keelbox report` saves nothing.
