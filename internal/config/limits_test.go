package config

import (
	"strings"
	"testing"
)

func TestAgentLimits(t *testing.T) {
	c, err := Parse([]byte(minimal("  agent:\n    limits: {cpu: 75%, memory: 70%}\n")), "t")
	if err != nil {
		t.Fatal(err)
	}
	l, ok := c.AgentLimits()
	if !ok || l.CPUPercent != 75 || l.MemoryPercent != 70 || l.MemoryHighPercent() != 63 || !l.SlowDown || l.CPUWeight != 50 || l.IOWeight != 50 || c.Users.Agent.Name != "agent" {
		t.Errorf("%+v %v", l, ok)
	}
	c, _ = Parse([]byte(minimal("  agent:\n    limits: {memory: 8G, cpu_weight: 20, io_weight: 30}\n")), "t")
	if l, _ := c.AgentLimits(); l.MemoryBytes != 8<<30 || l.MemoryHighBytes() != 8<<30/10*9 || l.CPUPercent != 0 || l.CPUWeight != 20 || l.IOWeight != 30 {
		t.Errorf("size: %+v", l)
	}
	// Without the key: a memory ceiling and nothing else (ADR 0082). The
	// same holds for limits that leave memory out.
	c, _ = Parse([]byte(minimal("")), "t")
	if l, ok := c.AgentLimits(); !ok || l.MemoryPercent != 90 || l.SlowDown || l.CPUPercent != 0 || l.CPUWeight != 50 {
		t.Errorf("by default: %+v %v", l, ok)
	}
	c, _ = Parse([]byte(minimal("  agent:\n    limits: {cpu: 60%}\n")), "t")
	if l, ok := c.AgentLimits(); !ok || l.MemoryPercent != 90 || l.SlowDown || l.CPUPercent != 60 {
		t.Errorf("cpu alone: %+v %v", l, ok)
	}
	// none lifts the ceiling: with nothing else set there are no limits.
	c, err = Parse([]byte(minimal("  agent:\n    limits: {memory: none}\n")), "t")
	if _, ok := c.AgentLimits(); err != nil || ok {
		t.Errorf("memory: none: %v %v", err, ok)
	}
	c, _ = Parse([]byte(minimal("  agent:\n    limits: {cpu: 60%, memory: none}\n")), "t")
	if l, ok := c.AgentLimits(); !ok || l.MemoryPercent != 0 || l.MemoryBytes != 0 || l.CPUPercent != 60 {
		t.Errorf("cpu with memory: none: %+v %v", l, ok)
	}
}

func TestAgentLimitsRejected(t *testing.T) {
	for y, want := range map[string]string{
		"{cpu: 75}":        "use a share of all CPUs from 5% to 100%",
		"{cpu: 150%}":      "use a share of all CPUs",
		"{memory: 99%}":    "from 10% to 95%",
		"{memory: lots}":   "a share of RAM such as 75%",
		"{memory: 100M}":   "too little for an agent",
		"{cpu_weight: -1}": "cpu_weight: -1: use 1 to 10000",
	} {
		p := problems(t, minimal("  agent:\n    limits: "+y+"\n"))
		if len(p) != 1 || !strings.Contains(p[0], want) {
			t.Errorf("%s: %v, want %q", y, p, want)
		}
	}
	if _, err := Parse([]byte(minimal("    limits: {cpu: 50%}\n")), "t"); err == nil || !strings.Contains(err.Error(), "limits") {
		t.Errorf("admin limits: %v", err)
	}
}

func TestStarterWritesLimits(t *testing.T) {
	out, err := Starter{Box: "nas", Provider: ProviderLocal, Keys: []string{key}, AgentLimit: SuggestedLimit}.Render()
	if err != nil {
		t.Fatal(err)
	}
	c, err := Parse([]byte(out), "nas.yaml")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if l, ok := c.AgentLimits(); !ok || l.CPUPercent != 75 || l.MemoryPercent != 75 {
		t.Errorf("%+v\n%s", l, out)
	}
	if !strings.Contains(out, "      cpu: 75%                         # of all CPUs\n") {
		t.Errorf("layout:\n%s", out)
	}
	out, _ = Starter{Box: "nas", Provider: ProviderLocal, Keys: []string{key}}.Render()
	if c, err := Parse([]byte(out), "nas.yaml"); err != nil || c.Users.Agent.Limits != nil || !strings.Contains(out, "# limits: {memory: 90%}") {
		t.Errorf("without: %v\n%s", err, out)
	}
	// --limits none is written out, since leaving the key out is a ceiling.
	out, _ = Starter{Box: "nas", Provider: ProviderLocal, Keys: []string{key}, AgentUnlimited: true}.Render()
	c, err = Parse([]byte(out), "nas.yaml")
	if _, on := c.AgentLimits(); err != nil || on || !strings.Contains(out, "\n    limits: {memory: none}") {
		t.Errorf("unlimited: %v %v\n%s", err, on, out)
	}
}
