package runs

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// The scripts a laptop runs on the box over SSH, as the account it logs in
// as. They read the logs directly when that account is in the sudo group,
// and through sudo -n otherwise (root needs neither).

// readFn defines rd FILE COMMAND...: the command runs directly when FILE
// is readable, else under sudo -n.
const readFn = `rd() { r=$1; shift; if [ -r "$r" ]; then "$@"; else sudo -n "$@"; fi; }` + "\n"

// StateScript prints the box's clock (Unix seconds), the newest run's id
// and its unit's state from systemctl is-active: "1762696862 ID active".
// The id and state are "-" when no run was ever started, or when the
// account can read neither the logs nor them through sudo -n. It changes
// nothing.
func StateScript() string {
	return readFn + fmt.Sprintf(`now=$(date +%%s)
f=%s
id=-
[ -e %s ] && id=$(rd "$f" cat "$f" 2>/dev/null)
case "$id" in ""|*[!0-9a-f-]*) id=-;; esac
st=-
[ "$id" = - ] || st=$(systemctl is-active %s"$id".service 2>/dev/null)
# A run that wrote its end record is over, though its unit lingers a moment.
if [ "$st" = active ]; then l=%s/"$id".jsonl; rd "$l" tail -n 1 "$l" 2>/dev/null | grep -q '"kind":"end"' && st=ended; fi
printf '%%s %%s %%s\n' "$now" "$id" "${st:--}"
`, CurrentPath, Dir, UnitPrefix, Dir)
}

// ParseState reads StateScript's output.
func ParseState(out string) (boxNow time.Time, id, state string, err error) {
	f := strings.Fields(out)
	if len(f) != 3 {
		return time.Time{}, "", "", fmt.Errorf("unexpected run state %q", strings.TrimSpace(out))
	}
	sec, err := strconv.ParseInt(f[0], 10, 64)
	if err != nil {
		return time.Time{}, "", "", fmt.Errorf("unexpected run state %q", strings.TrimSpace(out))
	}
	if f[1] != "-" && !ValidID(f[1]) {
		return time.Time{}, "", "", fmt.Errorf("unexpected run id %q", f[1])
	}
	return time.Unix(sec, 0), f[1], f[2], nil
}

// Running reports whether a state from systemctl is-active is a unit
// still running.
func Running(state string) bool {
	return state == "active" || state == "activating" || state == "deactivating" || state == "reloading"
}

// FollowScript prints a run's log from byte offset on and, while the run's
// unit has a main process, waits for more (tail --pid ends once that
// process has gone and its last lines are out). It changes nothing.
func FollowScript(id string, offset int64) (string, error) {
	if !ValidID(id) {
		return "", fmt.Errorf("run id %q is not valid", id)
	}
	return readFn + fmt.Sprintf(`f=%s
pid=$(systemctl show -p MainPID --value %s 2>/dev/null)
case "$pid" in ''|*[!0-9]*) pid=0;; esac
if [ "$pid" -gt 0 ]; then rd "$f" tail -c +%d --pid="$pid" -f "$f"; exit; fi
rd "$f" tail -c +%d "$f"
`, Path(id), Unit(id), offset+1, offset+1), nil
}

// StopCommand asks a run to stop after its current step: SIGINT to its
// main process, which apply takes as Ctrl-C. It needs root.
func StopCommand(id string) (string, error) {
	if !ValidID(id) {
		return "", fmt.Errorf("run id %q is not valid", id)
	}
	return "systemctl kill --kill-whom=main --signal=SIGINT " + Unit(id), nil
}

// Watched reports whether a process other than this one has the run's log
// open: a laptop following it (tail, under its SSH session). A finished run
// nobody watched notifies the owner.
func Watched(id string) bool {
	target := Path(id)
	self := strconv.Itoa(os.Getpid())
	procs, _ := os.ReadDir("/proc")
	for _, p := range procs {
		if _, err := strconv.Atoi(p.Name()); err != nil || p.Name() == self {
			continue
		}
		fds, _ := os.ReadDir(filepath.Join("/proc", p.Name(), "fd"))
		for _, fd := range fds {
			if l, err := os.Readlink(filepath.Join("/proc", p.Name(), "fd", fd.Name())); err == nil && strings.TrimSuffix(l, " (deleted)") == target {
				return true
			}
		}
	}
	return false
}
