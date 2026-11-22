package modules

import (
	"context"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/system"
)

func TestParseSubUIDs(t *testing.T) {
	const file = `ubuntu:100000:65536
agent:165536:65536
1001:400000:1000
# agent:1:1
agent:500000
agent:x:10
agent:600000:0
agent:4294967290:10
agent:4294967290:5
 agent:700000:1
agentx:800000:65536
`
	got := nftUIDs(parseSubUIDs(file, "agent", 1001))
	if want := "{ 165536-231071, 400000-400999, 4294967290-4294967294, 700000 }"; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
	if got := parseSubUIDs(file, "nobody", 65534); len(got) != 0 {
		t.Errorf("an account without a range got %v", got)
	}
	if got := parseSubUIDs("", "agent", 1001); len(got) != 0 {
		t.Errorf("an empty file gave %v", got)
	}
}

func TestMergeUIDsAndTheirNftForm(t *testing.T) {
	for _, tc := range []struct {
		in   []uidRange
		want string
	}{
		{[]uidRange{{1001, 1001}}, "1001"},
		{[]uidRange{{165536, 231071}, {1001, 1001}}, "{ 1001, 165536-231071 }"},
		{[]uidRange{{300000, 365535}, {1001, 1001}, {165536, 231071}}, "{ 1001, 165536-231071, 300000-365535 }"},
		// Overlapping and touching ranges become one, also with the uid.
		{[]uidRange{{1001, 1001}, {1002, 66537}}, "1001-66537"},
		{[]uidRange{{1001, 1001}, {1000, 2000}, {1500, 3000}, {3001, 3001}}, "1000-3001"},
		{[]uidRange{{1001, 1001}, {100000, 165535}, {100000, 165535}}, "{ 1001, 100000-165535 }"},
		{[]uidRange{{0, maxUID}, {5, 5}}, "0-4294967294"},
		{nil, "none"},
	} {
		if got := nftUIDs(mergeUIDs(tc.in)); got != tc.want {
			t.Errorf("%v: got %s, want %s", tc.in, got, tc.want)
		}
	}
}

// nft lists the match its own way; the agent's uids read back are the same.
// A reject that is not for named uids alone is not the agent's rule.
func TestListedAgentUIDs(t *testing.T) {
	x := system.NewFake()
	x.AddUser("agent", 1001)
	x.AddUser("my-agent", 1005)
	for line, want := range map[string]string{
		"meta skuid 1001 counter packets 0 bytes 0 reject":                                       "1001",
		"meta skuid { 1001, 165536-231071 } counter packets 3 bytes 180 reject comment \"a: b\"": "{ 1001, 165536-231071 }",
		"meta skuid { 165536-231071, 1001 } counter packets 0 bytes 0 reject":                    "{ 1001, 165536-231071 }",
		"meta skuid 1001-66537 counter packets 0 bytes 0 reject":                                 "1001-66537",
		"meta skuid { 1001-66537 } counter packets 0 bytes 0 reject":                             "1001-66537",
		"meta skuid \"agent\" counter packets 0 bytes 0 reject":                                  "1001",
		"meta skuid \"my-agent\" counter packets 0 bytes 0 reject":                               "1005",
		"meta skuid { \"agent\", 165536-231071 } counter packets 0 bytes 0 reject":               "{ 1001, 165536-231071 }",
		"meta skuid \"someone\" counter packets 0 bytes 0 reject":                                "none",
		"meta skuid { 1001, x } counter packets 0 bytes 0 reject":                                "none",
		"meta skuid 2000-1000 counter packets 0 bytes 0 reject":                                  "none",
		"meta skuid != 1001 counter packets 0 bytes 0 reject":                                    "none",
		"counter packets 0 bytes 0 reject":                                                       "none",
		"meta skuid 1001 udp dport 53 counter packets 0 bytes 0 reject":                          "none",
		"meta skuid 1001 counter packets 0 bytes 0 drop comment \"reject\"":                      "none",
	} {
		listing := "table inet keelbox_agent {\n\tchain linklocal {\n\t\t" + line + "\n\t}\n}\n"
		if got := nftUIDs(readLoadedNet(x, listing).agent); got != want {
			t.Errorf("%q: got %s, want %s", line, got, want)
		}
	}
	// The same rule in the output chain is not the link-local one.
	if got := readLoadedNet(x, "\tchain output {\n\t\tmeta skuid 1001 counter reject\n\t}\n").agent; got != nil {
		t.Errorf("a rule of the output chain read as the agent's link-local rule: %v", got)
	}
}

// loadedSkuid is the uids the loaded table refuses first at link-local
// addresses: the agent's.
func loadedSkuid(t *testing.T, s *sim) string {
	t.Helper()
	out, _ := s.Run(context.Background(), cmdNftList())
	return nftUIDs(readLoadedNet(s, out.Stdout).agent)
}

// An agent account has subordinate uids for rootless Docker, and a process
// of its own can take one in a user namespace while staying on the box's
// network. The loaded table matches them from the first apply, also when
// the account only gets its range later in that apply.
func TestAgentNetMatchesSubordinateUIDs(t *testing.T) {
	s := newSim(t)
	e := simEnv(t, s, testConfig(t, ""))
	ctx := context.Background()
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatal(err)
	}
	u, _ := s.LookupUser("agent")
	uids := simAgentUIDs(t, s)
	if len(uids) != 2 || uids[0] != (uidRange{uint32(u.UID), uint32(u.UID)}) || uids[1] != (uidRange{100000, 165535}) {
		t.Fatalf("the agent's uids: %v (subuid: %q)", uids, s.content(subUIDPath))
	}
	want := nftUIDs(uids)
	if got := loadedSkuid(t, s); got != want {
		t.Errorf("loaded table has %q, want %q", got, want)
	}
	if f := agentNetFinding(t, ctx, e); f.Level != engine.LevelOK || !strings.Contains(f.Detail, "(the agent and its subordinate uids included") {
		t.Errorf("doctor: %+v", f)
	}
	second, err := engine.Apply(ctx, e, All())
	if err != nil || len(second.Changes) != 0 {
		t.Errorf("second apply: %v %v", second.Changes, err)
	}

	// A range added later (usermod --add-subuids): doctor fails until the
	// next apply loads it.
	s.AddFile(subUIDPath, s.content(subUIDPath)+"ubuntu:300000:65536\nagent:400000:1000\n", 0o644)
	f := agentNetFinding(t, ctx, e)
	if f.Level != engine.LevelFail || !strings.Contains(f.Detail, "400000-400999") || !strings.HasSuffix(f.Detail, ": keelbox apply testbox") {
		t.Errorf("doctor with a range the table lacks: %+v", f)
	}
	if d, pending := stepPending(t, ctx, e, "agent-net-rules"); !pending {
		t.Errorf("plan does not show the rules changing: %q", d)
	}
	res, err := engine.Apply(ctx, e, All())
	if err != nil || len(res.Changes) != 1 || res.Changes[0].Step != "agent-net-rules" {
		t.Fatalf("apply after the range was added: %v %v", res.Changes, err)
	}
	if got := loadedSkuid(t, s); !strings.HasSuffix(got, ", 100000-165535, 400000-400999 }") || strings.Contains(got, "300000") {
		t.Errorf("loaded table after the range was added: %q", got)
	}
	if f := agentNetFinding(t, ctx, e); f.Level != engine.LevelOK {
		t.Errorf("doctor after apply: %+v", f)
	}
}

// Without rootless Docker nothing gives the account a range, and an
// account that has one from useradd is still matched with it.
func TestAgentNetWithoutDocker(t *testing.T) {
	ctx := context.Background()
	s := newSim(t)
	e := simEnv(t, s, testConfig(t, "runtime:\n  docker: false\n"))
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatal(err)
	}
	u, _ := s.LookupUser("agent")
	if got, want := loadedSkuid(t, s), nftUIDs([]uidRange{{uint32(u.UID), uint32(u.UID)}}); got != want {
		t.Errorf("loaded table has %q, want %q", got, want)
	}
	if f := agentNetFinding(t, ctx, e); f.Level != engine.LevelOK || !strings.Contains(f.Detail, "(the agent included") {
		t.Errorf("doctor: %+v", f)
	}

	s = newSim(t)
	s.AddFile(subUIDPath, "ubuntu:100000:65536\nagent:165536:65536\n", 0o644) // as useradd leaves it
	e = simEnv(t, s, testConfig(t, "runtime:\n  docker: false\n"))
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatal(err)
	}
	if got := loadedSkuid(t, s); !strings.HasSuffix(got, ", 165536-231071 }") {
		t.Errorf("loaded table has %q", got)
	}
}

// Before the first apply there is no agent account: plan shows the rules
// as to be created instead of failing.
func TestAgentNetPlanOnAFreshBox(t *testing.T) {
	s := newSim(t)
	e := simEnv(t, s, testConfig(t, ""))
	if d, pending := stepPending(t, context.Background(), e, "agent-net-rules"); !pending || d != "create "+agentNetRules {
		t.Errorf("plan on a fresh box: %q %v", d, pending)
	}
}
