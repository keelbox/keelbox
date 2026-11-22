// Package projects reads the state of the agent's projects: each directory
// directly under its projects directory, and whether the work in it exists
// anywhere but on the box (docs/adr/0064-work-on-the-box.md). keelbox
// status, doctor, the status snapshot and keelbox destroy's check all read
// it here.
package projects

import (
	"context"
	"encoding/hex"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/plain"
	"github.com/keelbox/keelbox/internal/snapshot"
	"github.com/keelbox/keelbox/internal/system"
)

const (
	// Scanned is how many directories one probe reads with git; the rest
	// are counted only.
	Scanned = 50
	// budget is how long one probe may spend in git before it reports the
	// directories left as not checked, and each the limit for one git
	// command, in seconds: the watch run calls this every two minutes.
	budget, each = 8, 3
	// Timeout bounds the whole probe.
	Timeout = 30 * time.Second
	// nameMax is how much of a directory's name is kept.
	nameMax = 40
)

// script runs as the agent, never as root: the directories, their names
// and every git config and hook in them are the agent's to write, so git
// there gets the agent's rights and no more, and git's own ownership check
// (safe.directory) passes without an exception.
const script = `# keelbox: the state of each directory directly under the projects
# directory, read with git as the account that owns them. It changes
# nothing and uses no network: "not pushed" is against the remote-tracking
# refs already there. One line per directory, the name in hex:
#   P HEX STATE DIRTY AHEAD REMOTES
# STATE is repo, norepo or unknown (git failed or took too long), then
#   T TOTAL
# with every directory counted, the ones past $2 included.
dir=$1 max=$2 budget=$3 each=$4
cd -P -- "$dir" 2>/dev/null || { echo "T 0"; exit 0; }
export GIT_OPTIONAL_LOCKS=0 GIT_TERMINAL_PROMPT=0 LC_ALL=C
start=$(date +%s)
one='
cd -- "$1" || exit 1
changes=$(git status --porcelain 2>/dev/null) || exit 1
remotes=$(git remote 2>/dev/null) || exit 1
head=
git rev-parse -q --verify HEAD >/dev/null 2>&1 && head=HEAD
ahead=$(git rev-list --count $head --branches --not --remotes 2>/dev/null) || exit 1
echo "repo $(printf %s "$changes" | grep -c .) ${ahead:-0} $(printf %s "$remotes" | grep -c .)"
'
n=0
for d in *; do
	# A symlink is not followed: what it points at is not a project here.
	[ -d "./$d" ] && [ ! -L "./$d" ] || continue
	n=$((n + 1))
	[ "$n" -le "$max" ] || continue
	hex=$(printf '%s' "$d" | od -An -v -tx1 | tr -d ' \n')
	if [ ! -e "./$d/.git" ]; then
		echo "P $hex norepo 0 0 0"
		continue
	fi
	if [ $(($(date +%s) - start)) -ge "$budget" ]; then
		echo "P $hex unknown 0 0 0"
		continue
	fi
	# One timeout for the directory's git commands together: timeout itself
	# takes a tenth of a second on Ubuntu's newer coreutils.
	if r=$(timeout -k 1 "$each" sh -c "$one" sh "./$d"); then
		echo "P $hex $r"
		continue
	fi
	echo "P $hex unknown 0 0 0"
done
echo "T $n"
`

// Probe reads the projects of the config's agent. It runs one shell
// script as the agent, bounded in time, and returns nil when that could
// not run at all (no agent account yet, say).
func Probe(ctx context.Context, x system.Executor, c *config.Config) *snapshot.Projects {
	u, err := x.LookupUser(c.Users.Agent.Name)
	if err != nil || u.Home == "" {
		return nil
	}
	dir := path.Join(u.Home, c.ProjectsDir())
	cmd := system.As(u.Name, "sh", "-s", "--", dir, strconv.Itoa(Scanned), strconv.Itoa(budget), strconv.Itoa(each))
	cmd.Stdin = []byte(script)
	cmd.Timeout = Timeout
	out, err := system.Output(ctx, x, cmd)
	if err != nil {
		return nil
	}
	p, ok := Parse(out)
	if !ok {
		return nil
	}
	p.Dir = "~/" + c.ProjectsDir()
	return p
}

// Parse reads the script's output. Every name in it was written by the
// agent: it is decoded, made one plain line and cut to nameMax. The list
// has work that is only on the box first, then by name.
func Parse(out string) (*snapshot.Projects, bool) {
	p := &snapshot.Projects{}
	total := -1
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		switch {
		case len(f) == 2 && f[0] == "T":
			total, _ = strconv.Atoi(f[1])
		case len(f) == 6 && f[0] == "P":
			raw, err := hex.DecodeString(f[1])
			if err != nil || len(p.List) >= Scanned {
				continue
			}
			pr := snapshot.Project{Name: cut(plain.Line(string(raw)))}
			switch f[2] {
			case "repo":
				pr.Repo = true
				pr.Uncommitted, _ = strconv.Atoi(f[3])
				pr.Unpushed, _ = strconv.Atoi(f[4])
				n, _ := strconv.Atoi(f[5])
				pr.NoRemote = n == 0
			case "norepo":
			default:
				pr.Repo, pr.Unchecked = true, true
			}
			p.List = append(p.List, pr)
		}
	}
	if total < 0 {
		return nil, false
	}
	p.Total = max(total, len(p.List))
	sort.SliceStable(p.List, func(i, j int) bool {
		a, b := p.List[i], p.List[j]
		if a.OnlyHere() != b.OnlyHere() {
			return a.OnlyHere()
		}
		return a.Name < b.Name
	})
	return p, true
}

func cut(s string) string {
	if r := []rune(s); len(r) > nameMax {
		return string(r[:nameMax-1]) + "…"
	}
	return s
}
