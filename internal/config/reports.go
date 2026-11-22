package config

// Reports configures keelbox reports: the box's local inbox of problems
// with keelbox the agent wrote up, with the user's yes, for the owner to
// review and send (ADR 0062).
type Reports struct {
	Enabled *bool `yaml:"enabled,omitempty"`
}

// ReportsEnabled reports whether the agent may file keelbox reports.
func (c *Config) ReportsEnabled() bool { return On(c.Reports.Enabled, true) }
