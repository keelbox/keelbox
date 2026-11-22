package modules

import (
	"context"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/system"
)

// The agent's cron and at jobs run in cron's and atd's cgroups, outside
// users.agent.limits. apply denies it both, removes
// the jobs it already has (the daemons run a denied user's jobs all the
// same) and doctor fails until then.
func TestAgentCannotScheduleJobs(t *testing.T) {
	s := newSim(t)
	s.AddFile("/usr/bin/crontab", "", 0o755)
	s.AddFile("/usr/bin/at", "", 0o755)
	s.AddFile("/etc/at.deny", "daemon\nbin\n", 0o640)
	crontab, atJobs := true, true
	// The spool, not crontab -l -u: crontab refuses a denied account even
	// to root, so a crontab from before the deny line would stay hidden.
	s.OnFunc("find "+cronSpool+" -mindepth 1 -maxdepth 1 -name agent", func(c system.Cmd) (system.Result, error) {
		if !crontab {
			return res("")
		}
		return res(cronSpool + "/agent\n")
	})
	s.OnFunc("rm -f -- "+cronSpool+"/agent", func(c system.Cmd) (system.Result, error) { crontab = false; return res("") })
	s.OnFunc("atq", func(c system.Cmd) (system.Result, error) {
		out := "3\tSat Nov  7 15:00:00 2026 a admin\n"
		if atJobs {
			out += "4\tSat Nov  7 15:00:00 2026 a agent\n7\tSat Nov  7 16:00:00 2026 b agent\n"
		}
		return res(out)
	})
	s.OnFunc("atrm 4 7", func(c system.Cmd) (system.Result, error) { atJobs = false; return res("") })
	e := simEnv(t, s, testConfig(t, ""))
	ctx := context.Background()
	fails := func() []string {
		var out []string
		for _, f := range verifyAgentSchedulers(ctx, e) {
			if f.Level == engine.LevelFail {
				out = append(out, f.Check)
			}
		}
		return out
	}
	if f := fails(); len(f) != 2 {
		t.Errorf("doctor before apply: %v", f)
	}
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(s.Files["/etc/cron.deny"].Data), "agent\n") || string(s.Files["/etc/at.deny"].Data) != "daemon\nbin\nagent\n" {
		t.Errorf("deny files: %q %q", s.Files["/etc/cron.deny"].Data, s.Files["/etc/at.deny"].Data)
	}
	if crontab || atJobs || s.Ran("atrm 3") {
		t.Errorf("jobs left: crontab %v, at %v", crontab, atJobs)
	}
	if f := fails(); len(f) != 0 {
		t.Errorf("doctor after apply: %v", f)
	}
	// An allow file is the owner's: one that names the agent stops the
	// step with the fix, and doctor fails.
	s.AddFile("/etc/cron.allow", "admin\nagent\n", 0o640)
	res, err := engine.Apply(ctx, e, All())
	if err != nil || len(res.Failed) != 1 || !strings.Contains(res.Failed[0].Detail, "remove that line") || strings.Join(fails(), ",") != "agent-no-cron" {
		t.Errorf("cron.allow naming the agent: %+v", res.Failed)
	}
	s.AddFile("/etc/cron.allow", "admin\n", 0o640)
	if f := fails(); len(f) != 0 {
		t.Errorf("cron.allow without the agent: %v", f)
	}
}
