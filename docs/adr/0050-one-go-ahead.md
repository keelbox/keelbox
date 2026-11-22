# 0050: One go-ahead in keelbox up, and key logins are not cut off

Date: 2026-11-07. Status: accepted. Amends ADR 0038 (how `up` asks) and ADR 0035 (the draft after a no).

## Context
On a machine in use, asking twice at the end of `keelbox up`, "Accept the items marked *?" and then "Go ahead?", would ask for one decision two times, and if a no to the first threw every answer away, an owner who stopped to keep a service would have to start over. An impact report that marked the account `up` logs in as, with a key, as cut off because it has a password and sshd takes passwords would be wrong too: turning passwords off cuts nothing off for an account that uses a key.

## Decision
- **One question.** When items marked `*` are open (not ports `up` asked about, and no `--adopt`), the go-ahead says what it accepts: `Go ahead, accepting the 2 items marked *? [y/N]`, default no. Otherwise it is `Go ahead? [Y/n]`. A yes runs the first apply with `--adopt`. Without a terminal, `--adopt --yes` is the answer.
- **No keeps the draft.** A no at the go-ahead changes nothing and keeps the answers (`up-draft.json`, ADR 0035), the LAN ports included, and says how to continue: `keelbox up BOX` asks only the go-ahead again. A kept LAN answer that no longer fits what listens there is asked again.
- **The report's hint inside up.** `up` does not point at `keelbox plan BOX --impact`, which needs a config `up` has not written.
- **Key logins.** The inventory reads each account's authorized keys files as sshd would (`sshd -T`'s `authorizedkeysfile` and `pubkeyauthentication`), as the account. An account with a password that also has a key there, or the account keelbox runs as over SSH (which logs in with a key), is listed under Changes ("logs in over SSH with a key; keelbox turns SSH password logins off"), not cut off, and does not gate.

## Consequences
- A key in `authorized_keys` is not proof the owner uses it, or that it is not restricted by options such as `from=`. keelbox counts any key line; an owner whose only working login is a password and whose file holds a stale key is not warned. The password still works at the console and for sudo.
- `AuthorizedKeysCommand` is not read: an account whose keys come only from it is still marked.
