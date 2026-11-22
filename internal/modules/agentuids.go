package modules

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/keelbox/keelbox/internal/system"
)

// The agent can run as more uids than its own. /etc/subuid gives an account
// subordinate uids, and newuidmap (setuid root, part of the uidmap package
// rootless Docker needs) maps them into a user namespace the account
// creates. A user namespace is not a network namespace: a process there
// that switches to a mapped uid still opens sockets on the box's own
// network, owned by that subordinate uid (rootlesskit --net=host, or
// unshare --map-auto, then setpriv --reuid=1). A rule that matches only the
// agent's own uid lets those through, so the agent's table matches the
// agent's uid and every range /etc/subuid gives it
// (docs/adr/0070-agent-net-subordinate-uids.md).
//
// The gid does not matter: the rules match the socket's uid alone, and no
// uid outside these is one the agent can take.
const subUIDPath = "/etc/subuid"

// maxUID is the highest uid there is; 4294967295 is "no uid".
const maxUID = 1<<32 - 2

// uidRange is first to last, both included.
type uidRange struct{ first, last uint32 }

// parseSubUIDs reads the ranges /etc/subuid gives an account, which the
// file names by login name or by uid. Lines that are not a range are
// skipped, as newuidmap skips them.
func parseSubUIDs(data, name string, uid int) []uidRange {
	var out []uidRange
	for _, l := range strings.Split(data, "\n") {
		f := strings.Split(strings.TrimSpace(l), ":")
		if len(f) != 3 || (f[0] != name && f[0] != strconv.Itoa(uid)) {
			continue
		}
		start, err1 := strconv.ParseUint(f[1], 10, 32)
		count, err2 := strconv.ParseUint(f[2], 10, 32)
		if err1 != nil || err2 != nil || count == 0 || start+count-1 > maxUID {
			continue
		}
		out = append(out, uidRange{uint32(start), uint32(start + count - 1)})
	}
	return out
}

// mergeUIDs sorts ranges and joins those that overlap or touch, so the set
// is written one way whatever order the file has.
func mergeUIDs(in []uidRange) []uidRange {
	rs := append([]uidRange(nil), in...)
	sort.Slice(rs, func(i, j int) bool { return rs[i].first < rs[j].first })
	var out []uidRange
	for _, r := range rs {
		if n := len(out); n > 0 && uint64(r.first) <= uint64(out[n-1].last)+1 {
			if r.last > out[n-1].last {
				out[n-1].last = r.last
			}
			continue
		}
		out = append(out, r)
	}
	return out
}

// accountUID is an account's uid as nftables takes it.
func accountUID(u system.User) (uint32, error) {
	if u.UID < 0 || int64(u.UID) > maxUID {
		return 0, fmt.Errorf("account %s has uid %d", u.Name, u.UID)
	}
	return uint32(u.UID), nil //nolint:gosec // G115: in range, checked above
}

// agentUIDs is every uid a process of the agent's can run as: its own and
// its subordinate ranges. An account without a range has only its own.
func agentUIDs(x system.Executor, agent string) ([]uidRange, error) {
	u, err := x.LookupUser(agent)
	if err != nil {
		return nil, err
	}
	own, err := accountUID(u)
	if err != nil {
		return nil, err
	}
	uids := []uidRange{{own, own}}
	st, err := x.Stat(subUIDPath)
	if err != nil {
		return nil, err
	}
	if st.Exists {
		data, err := x.ReadFile(subUIDPath)
		if err != nil {
			return nil, err
		}
		uids = append(uids, parseSubUIDs(string(data), agent, u.UID)...)
	}
	return mergeUIDs(uids), nil
}

// nftUIDs writes ranges as nftables takes them after "meta skuid !=": one
// uid or range bare, several as a set.
func nftUIDs(uids []uidRange) string {
	var l []string
	for _, r := range uids {
		if r.first == r.last {
			l = append(l, strconv.FormatUint(uint64(r.first), 10))
		} else {
			l = append(l, strconv.FormatUint(uint64(r.first), 10)+"-"+strconv.FormatUint(uint64(r.last), 10))
		}
	}
	switch len(l) {
	case 0:
		return "none"
	case 1:
		return l[0]
	}
	return "{ " + strings.Join(l, ", ") + " }"
}

// parseListedUIDs reads the uids of a "meta skuid" match as nft lists
// them. nft prints a set its own way (a single range with or without
// braces, an account by uid or by quoted name), so the ranges are compared,
// not the text. A set with an element that is not a uid or a range reads as
// no uids at all.
func parseListedUIDs(x system.Executor, set string) []uidRange {
	var out []uidRange
	for _, el := range strings.Split(strings.Trim(strings.TrimSpace(set), "{}"), ",") {
		el = strings.TrimSpace(el)
		first, last, isRange := strings.Cut(el, "-")
		if !isRange || (strings.Count(el, `"`) == 2 && strings.HasPrefix(el, `"`) && strings.HasSuffix(el, `"`)) {
			// One uid, or one name, which may itself hold a hyphen.
			first, last = el, el
		}
		a, ok1 := listedUID(x, first)
		b, ok2 := listedUID(x, last)
		if !ok1 || !ok2 || a > b {
			return nil
		}
		out = append(out, uidRange{a, b})
	}
	return mergeUIDs(out)
}

func listedUID(x system.Executor, s string) (uint32, bool) {
	s = strings.TrimSpace(s)
	if name := strings.Trim(s, `"`); name != s {
		u, err := x.LookupUser(name)
		if err != nil {
			return 0, false
		}
		uid, err := accountUID(u)
		return uid, err == nil
	}
	n, err := strconv.ParseUint(s, 10, 32)
	return uint32(n), err == nil
}
