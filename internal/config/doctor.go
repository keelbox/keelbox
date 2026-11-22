package config

// Doctor schedules doctor on the box (docs/adr/0042-scheduled-doctor.md).
type Doctor struct {
	// Schedule is a systemd OnCalendar expression, or off.
	Schedule string `yaml:"schedule,omitempty"`
	// NotifyWarnings sends warnings, not only failures.
	NotifyWarnings *bool `yaml:"notify_warnings,omitempty"`
}

// DoctorSchedule is doctor.schedule, default hourly; "" when off.
func (c *Config) DoctorSchedule() string {
	s := or(c.Doctor.Schedule, "hourly")
	if s == "off" {
		return ""
	}
	return s
}

func (c *Config) DoctorNotifyWarnings() bool { return On(c.Doctor.NotifyWarnings, false) }

func (c *Config) validateDoctor(add func(string, ...any)) {
	if s := c.DoctorSchedule(); s != "" && !scheduleRe.MatchString(s) {
		add("doctor.schedule: %q is not a systemd calendar expression such as hourly, or off", c.Doctor.Schedule)
	}
}
