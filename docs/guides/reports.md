# Reports

```sh
keelbox reports acme             # list the drafts the agent saved
keelbox reports acme --show 1    # read one
keelbox reports acme --send 1    # review it, then open it as a GitHub issue
```

When keelbox gets in an agent's way, the agent can save the problem as a draft for keelbox's issue tracker. A report leaves the box only when you send it.

## How a report is made

The primer is the file keelbox writes on the box to tell agents how the box works. It tells the agent to ask you first, and to file a report only after your yes. The agent then runs:

```sh
keelbox report "expose: says nothing listens" --area expose --detail "exact error"
```

The draft is saved on the box in `/var/lib/keelbox/reports/`, which only root reads, and no notification goes out. The box adds four facts:

- the keelbox version and release
- the Ubuntu version and architecture
- the machine's CPUs and memory
- the names of the checks that failed at the last scheduled `keelbox doctor`

The box takes at most 5 reports a day and keeps at most 30. `keelbox reports acme --delete 1` removes one.

## Send one

`--send` needs a terminal, and goes in five steps.

1. If the report names projects under `~/projects`, keelbox asks whether to keep the names. No is preselected: the names are replaced, and the paths under them.
2. keelbox replaces tokens, keys, email addresses, Tailscale names and addresses, public IP addresses, the box name and home paths. It shows the whole text and what it replaced.
3. It offers to open the text in `$VISUAL` or `$EDITOR`, and checks your edit again.
4. It asks whether to open a GitHub issue with this text. No is preselected. Issues on github.com/keelbox/keelbox are public.
5. On a yes your browser opens GitHub's new-issue form, filled in. You submit it there, or close the tab.

A report in the `security` area opens keelbox's private advisory form instead, and you paste the text in. Without a browser, keelbox prints the link.

The agent's text is not checked. A repository the agent works in can tell it to file a report that carries a secret. The redaction matches the shapes in step 2 and no others. The form receives the text shown before your yes, or its start when a link cannot hold all of it.

## Turn it off

```yaml
reports:
  enabled: false
```

After `keelbox apply acme` the primer says nothing about reports, and `keelbox report` saves nothing.
