package config

import (
	"fmt"
	"net/netip"
	"net/url"
	"regexp"
	"strings"

	"github.com/keelbox/keelbox/internal/events"
)

// Notify sends box and agent events to the owner's channels
// (docs/adr/0043-notifications.md).
type Notify struct {
	Channels []Channel `yaml:"channels,omitempty"`
	// Mute lists event kinds never sent.
	Mute []string `yaml:"mute,omitempty"`
	// QuietHours is HH:MM-HH:MM in the box's timezone; it may cross
	// midnight.
	QuietHours string `yaml:"quiet_hours,omitempty"`
	// QuietHoursUrgent lets urgent events through quiet hours. Default true.
	QuietHoursUrgent *bool `yaml:"quiet_hours_urgent,omitempty"`
	// AgentText includes the agent's notification text (at most 200
	// characters). Default false: it can carry code or secrets to a
	// third-party push service.
	AgentText *bool `yaml:"agent_text,omitempty"`
}

// Channel is one place events go. Secrets are only ever in files, named by
// the *_file keys, mode 600.
type Channel struct {
	Name  string `yaml:"name"`
	Type  string `yaml:"type"`
	Level string `yaml:"level,omitempty"`
	// ntfy
	Server    string `yaml:"server,omitempty"`
	TopicFile string `yaml:"topic_file,omitempty"`
	TokenFile string `yaml:"token_file,omitempty"` // ntfy (Bearer) and pushover (application)
	// pushover
	UserFile string `yaml:"user_file,omitempty"`
	// telegram
	ChatID       string `yaml:"chat_id,omitempty"`
	BotTokenFile string `yaml:"bot_token_file,omitempty"`
	// webhook
	Format     string `yaml:"format,omitempty"`
	URLFile    string `yaml:"url_file,omitempty"`
	HeaderFile string `yaml:"header_file,omitempty"`
}

// Channel types.
const (
	ChannelNtfy     = "ntfy"
	ChannelPushover = "pushover"
	ChannelTelegram = "telegram"
	ChannelWebhook  = "webhook"
)

// MaxChannels is the most channels a box may have.
const MaxChannels = 8

func (c *Config) NotifyQuietHoursUrgent() bool { return On(c.Notify.QuietHoursUrgent, true) }
func (c *Config) NotifyAgentText() bool        { return On(c.Notify.AgentText, false) }

// LevelOf is the channel's lowest level sent, default info.
func (ch Channel) LevelOf() events.Level {
	l, _ := events.ParseLevel(or(ch.Level, "info"))
	return l
}

// NtfyServer is the channel's ntfy server, default https://ntfy.sh.
func (ch Channel) NtfyServer() string { return or(ch.Server, "https://ntfy.sh") }

// Muted reports whether kind is in notify.mute.
func (c *Config) Muted(kind string) bool { return contains(c.Notify.Mute, kind) }

var (
	channelNameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`)
	chatIDRe      = regexp.MustCompile(`^(-?[0-9]{1,20}|@[A-Za-z0-9_]{5,32})$`)
	quietRe       = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]-([01][0-9]|2[0-3]):[0-5][0-9]$`)
	tailnetV4     = netip.MustParsePrefix("100.64.0.0/10")
	tailnetV6     = netip.MustParsePrefix("fd7a:115c:a1e0::/48")
)

// channelKeys is which optional and required keys each type takes.
var channelKeys = map[string]struct{ required, optional []string }{
	ChannelNtfy:     {required: []string{"topic_file"}, optional: []string{"server", "token_file"}},
	ChannelPushover: {required: []string{"token_file", "user_file"}},
	ChannelTelegram: {required: []string{"chat_id", "bot_token_file"}},
	ChannelWebhook:  {required: []string{"format", "url_file"}, optional: []string{"header_file"}},
}

// SecretFile is one *_file key of a channel and the path it holds.
type SecretFile struct {
	Key  string // topic_file
	Path *string
}

// SecretFiles lists the channel's secret file keys that are set, in a
// fixed order, so a caller can read or re-point them.
func (ch *Channel) SecretFiles() []SecretFile {
	var out []SecretFile
	for _, f := range []SecretFile{
		{"topic_file", &ch.TopicFile}, {"token_file", &ch.TokenFile}, {"user_file", &ch.UserFile},
		{"bot_token_file", &ch.BotTokenFile}, {"url_file", &ch.URLFile}, {"header_file", &ch.HeaderFile},
	} {
		if *f.Path != "" {
			out = append(out, f)
		}
	}
	return out
}

func (ch Channel) keys() map[string]string {
	return map[string]string{
		"server": ch.Server, "topic_file": ch.TopicFile, "token_file": ch.TokenFile, "user_file": ch.UserFile,
		"chat_id": ch.ChatID, "bot_token_file": ch.BotTokenFile, "format": ch.Format, "url_file": ch.URLFile, "header_file": ch.HeaderFile,
	}
}

func (c *Config) validateNotify(add func(string, ...any)) {
	n := c.Notify
	if len(n.Channels) > MaxChannels {
		add("notify.channels: %d channels; at most %d", len(n.Channels), MaxChannels)
	}
	names := map[string]bool{}
	for i, ch := range n.Channels {
		at := fmt.Sprintf("notify.channels[%d]", i)
		if !channelNameRe.MatchString(ch.Name) {
			add("%s.name: %q must be lowercase letters, digits and hyphens, at most 32 characters", at, ch.Name)
		} else if names[ch.Name] {
			add("%s.name: %q is used twice", at, ch.Name)
		}
		names[ch.Name] = true
		if _, err := events.ParseLevel(or(ch.Level, "info")); err != nil {
			add("%s.level: %v", at, err)
		}
		spec, ok := channelKeys[ch.Type]
		if !ok {
			add("%s.type: %q: use ntfy, pushover, telegram or webhook", at, ch.Type)
			continue
		}
		for k, v := range ch.keys() {
			switch {
			case contains(spec.required, k) && v == "":
				add("%s.%s: required for a %s channel", at, k, ch.Type)
			case v != "" && !contains(spec.required, k) && !contains(spec.optional, k):
				add("%s.%s: a %s channel does not take %s", at, k, ch.Type, k)
			case v != "" && strings.HasSuffix(k, "_file") && !excludeRe.MatchString(v):
				add("%s.%s: %q must be a file path without control characters", at, k, v)
			}
		}
		if ch.Type == ChannelNtfy {
			if err := checkServer(ch.NtfyServer()); err != nil {
				add("%s.server: %v", at, err)
			}
		}
		if ch.ChatID != "" && !chatIDRe.MatchString(ch.ChatID) {
			add("%s.chat_id: %q must be a numeric chat id or an @channel name", at, ch.ChatID)
		}
		if ch.Format != "" && !oneOf(ch.Format, "json", "text") {
			add("%s.format: %q: use json or text", at, ch.Format)
		}
	}
	for _, k := range n.Mute {
		if _, ok := events.Lookup(k); !ok {
			add("notify.mute: %q is not an event kind (%s)", k, strings.Join(events.Kinds(), ", "))
		}
	}
	if n.QuietHours != "" {
		if !quietRe.MatchString(n.QuietHours) {
			add("notify.quiet_hours: %q must be HH:MM-HH:MM, such as 22:00-07:00", n.QuietHours)
		} else if a, b, _ := strings.Cut(n.QuietHours, "-"); a == b {
			add("notify.quiet_hours: %q starts and ends at the same time", n.QuietHours)
		}
	}
}

// checkServer allows https anywhere, and plain http only to loopback or the
// tailnet (a self-hosted ntfy there).
func checkServer(s string) error {
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("%q must be a server URL such as https://ntfy.sh, without credentials or a path", s)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if PlainHTTPAllowed(u.Hostname()) {
			return nil
		}
		return fmt.Errorf("%q: plain http only to loopback or a tailnet address; use https", s)
	}
	return fmt.Errorf("%q must start with https://", s)
}

// PlainHTTPAllowed reports whether a notification may go to host over
// plain http: only loopback and the tailnet, whose traffic WireGuard
// already encrypts.
func PlainHTTPAllowed(host string) bool {
	if strings.HasSuffix(host, ".ts.net") || host == "localhost" {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && (ip.IsLoopback() || tailnetV4.Contains(ip) || tailnetV6.Contains(ip))
}
