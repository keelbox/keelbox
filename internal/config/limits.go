package config

import (
	"fmt"
	"strconv"
	"strings"
)

// users.agent.limits: what the agent account's processes may use together,
// its SSH sessions, tmux, Remote Control and rootless Docker alike
// (docs/adr/0048-agent-limits.md). Memory has a ceiling on every box unless
// the config lifts it (DefaultMemoryLimit, ADR 0082); CPU has none unless
// the config sets one, as on a machine shared with other services.

// AgentAccount is the agent: an account, and the limits on it.
type AgentAccount struct {
	Account `yaml:",inline"`
	// Permissions is how much the agent may change the box: manual, auto
	// (the default) or full (permissions.go).
	Permissions string       `yaml:"permissions,omitempty"`
	Limits      *AgentLimits `yaml:"limits,omitempty"`
	// Git is the name and email the agent's commits carry (git.go).
	Git *GitIdentity `yaml:"git,omitempty"`
}

// AgentLimits is users.agent.limits as written.
type AgentLimits struct {
	// CPU is a share of all the box's CPUs, such as 75%.
	CPU string `yaml:"cpu,omitempty"`
	// Memory is the ceiling, a share of RAM (70%) or a size (12G). The
	// agent slows down at 90% of it (MemoryHigh) and the kernel stops its
	// largest process at it (MemoryMax). Absent is DefaultMemoryLimit, a
	// ceiling without the slow-down; none lifts it.
	Memory string `yaml:"memory,omitempty"`
	// CPUWeight and IOWeight rank logins, the agent's most of all, against
	// system services under contention: 1 to 10000, systemd's default is
	// 100; default 50.
	CPUWeight int `yaml:"cpu_weight,omitempty"`
	IOWeight  int `yaml:"io_weight,omitempty"`
}

// Limits is users.agent.limits resolved.
type Limits struct {
	CPUPercent    int   // of all CPUs; 0 is no cap
	MemoryPercent int   // of RAM; 0 when MemoryBytes is set or no cap
	MemoryBytes   int64 // a fixed ceiling
	// SlowDown is whether the agent's services are held back from 90% of
	// the ceiling (MemoryHigh). The config's own ceiling has the mark; the
	// default ceiling has none (ADR 0082).
	SlowDown  bool
	CPUWeight int
	IOWeight  int
}

// DefaultLimitWeight is the weight logins get when limits are set: half
// of a system service's 100.
const DefaultLimitWeight = 50

// SuggestedLimit is what keelbox up proposes on a machine that runs other
// services: the agent gets at most 75% of CPU and memory, leaving a
// quarter to them.
const SuggestedLimit = 75

// DefaultMemoryLimit is the agent's memory ceiling where the config sets
// none: the tenth it leaves is what SSH, Tailscale and the admin answer
// from when the agent's work has used the rest (ADR 0082). It is a ceiling
// alone, where the kernel stops the agent's largest process, with no
// slow-down mark below it: at the mark one runaway process holds back all
// the agent's work for minutes and is not stopped.
const DefaultMemoryLimit = "90%"

// NoMemoryLimit is users.agent.limits.memory written to lift the ceiling.
const NoMemoryLimit = "none"

// AgentLimits resolves users.agent.limits; ok is false without limits,
// which is memory: none and nothing else set.
func (c *Config) AgentLimits() (Limits, bool) {
	l := c.Users.Agent.Limits
	if l == nil {
		l = &AgentLimits{}
	}
	mem := strings.TrimSpace(l.Memory)
	slow := mem != ""
	switch {
	case mem == "":
		mem = DefaultMemoryLimit
	case strings.EqualFold(mem, NoMemoryLimit):
		if l.CPU == "" && l.CPUWeight == 0 && l.IOWeight == 0 {
			return Limits{}, false
		}
		mem = ""
	}
	l = &AgentLimits{CPU: l.CPU, Memory: mem, CPUWeight: l.CPUWeight, IOWeight: l.IOWeight}
	out := Limits{CPUWeight: l.CPUWeight, IOWeight: l.IOWeight, SlowDown: slow && mem != ""}
	if out.CPUWeight == 0 {
		out.CPUWeight = DefaultLimitWeight
	}
	if out.IOWeight == 0 {
		out.IOWeight = DefaultLimitWeight
	}
	out.CPUPercent, _ = parsePercent(l.CPU)
	if p, err := parsePercent(l.Memory); err == nil {
		out.MemoryPercent = p
	} else if n, err := parseSize(l.Memory); err == nil {
		out.MemoryBytes = n
	}
	return out, true
}

// MemoryHighPercent is where the agent slows down: 90% of the ceiling.
func (l Limits) MemoryHighPercent() int { return l.MemoryPercent * 9 / 10 }

// MemoryHighBytes is where the agent slows down, for a fixed ceiling.
func (l Limits) MemoryHighBytes() int64 { return l.MemoryBytes / 10 * 9 }

func parsePercent(s string) (int, error) {
	v, ok := strings.CutSuffix(strings.TrimSpace(s), "%")
	if !ok {
		return 0, fmt.Errorf("not a percentage")
	}
	return strconv.Atoi(strings.TrimSpace(v))
}

func (c *Config) validateLimits(add func(string, ...any)) {
	l := c.Users.Agent.Limits
	if l == nil {
		return
	}
	const key = "users.agent.limits"
	if l.CPU != "" {
		if p, err := parsePercent(l.CPU); err != nil || p < 5 || p > 100 {
			add("%s.cpu: %q: use a share of all CPUs from 5%% to 100%%, for example cpu: 75%%", key, l.CPU)
		}
	}
	if l.Memory != "" && !strings.EqualFold(strings.TrimSpace(l.Memory), NoMemoryLimit) {
		p, perr := parsePercent(l.Memory)
		n, serr := parseSize(l.Memory)
		switch {
		case perr == nil && (p < 10 || p > 95):
			add("%s.memory: %q: use a share of RAM from 10%% to 95%%, a size such as 8G, or none for no ceiling, for example memory: 75%%", key, l.Memory)
		case perr != nil && serr != nil:
			add("%s.memory: %q: use a share of RAM such as 75%%, a size such as 8G, or none for no ceiling", key, l.Memory)
		case perr != nil && n < 256<<20:
			add("%s.memory: %s is too little for an agent; use 256M or more, or a share such as 75%%", key, l.Memory)
		}
	}
	for name, w := range map[string]int{"cpu_weight": l.CPUWeight, "io_weight": l.IOWeight} {
		if w != 0 && (w < 1 || w > 10000) {
			add("%s.%s: %d: use 1 to 10000 (system services have 100), for example %s: 50", key, name, w, name)
		}
	}
}
