package config

// Power is how the box treats sleep (docs/adr/0040-unclean-shutdown-and-sleep.md).
type Power struct {
	// Sleep is allow (the OS default) or never (sleep, suspend and
	// hibernate masked, lid switch ignored).
	Sleep string `yaml:"sleep,omitempty"`
}

// Disk sets the watch run's disk use alerts, per mount of /, /home and
// /var/lib/docker.
type Disk struct {
	WarnPercent *int `yaml:"warn_percent,omitempty"`
	FailPercent *int `yaml:"fail_percent,omitempty"`
}

// Power sleep settings.
const (
	SleepAllow = "allow"
	SleepNever = "never"
)

func (c *Config) PowerSleep() string { return or(c.Host.Power.Sleep, SleepAllow) }

// DiskThresholds returns host.disk.warn_percent and fail_percent, default
// 85 and 95.
func (c *Config) DiskThresholds() (warn, fail int) {
	warn, fail = 85, 95
	if c.Host.Disk.WarnPercent != nil {
		warn = *c.Host.Disk.WarnPercent
	}
	if c.Host.Disk.FailPercent != nil {
		fail = *c.Host.Disk.FailPercent
	}
	return warn, fail
}

func (c *Config) validatePower(add func(string, ...any)) {
	if !oneOf(c.PowerSleep(), SleepAllow, SleepNever) {
		add("host.power.sleep: %q: use allow or never", c.Host.Power.Sleep)
	}
	warn, fail := c.DiskThresholds()
	if warn < 1 || fail > 99 || warn >= fail {
		add("host.disk: warn_percent %d and fail_percent %d must be between 1 and 99, warn below fail", warn, fail)
	}
}

// Watchdog reports whether the watch run brings back a dead tmux server and
// a stopped Remote Control (sessions.watchdog, default true). The tmux part
// needs sessions.start_on_boot, whose unit it restarts.
func (c *Config) Watchdog() bool { return On(c.Sessions.Watchdog, true) }
