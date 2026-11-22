// Package notify delivers events (internal/events) to the owner's channels:
// ntfy, Pushover, Telegram and webhooks. Each channel is an events.Sender
// that applies the owner's choices (muted kinds, the channel's lowest level,
// quiet hours) before it renders the event with a fixed template and sends
// it. Channel secrets are read only from root's copies in SecretDir and
// never appear in an error, a log line or the logbook
// (docs/adr/0043-notifications.md).
package notify

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/events"
	"github.com/keelbox/keelbox/internal/system"
)

// LastPath records each channel's last send, for doctor and status.
const LastPath = events.Dir + "/last.json"

// Last is one channel's last send. Error is a SendError's text, which
// carries no secret.
type Last struct {
	Attempt time.Time `json:"attempt"`
	OK      time.Time `json:"ok,omitempty"`
	Kind    string    `json:"kind,omitempty"`
	Error   string    `json:"error,omitempty"`
}

// ReadLast reads every channel's last send.
func ReadLast(x system.Executor) map[string]Last {
	m := map[string]Last{}
	if data, err := x.ReadFile(LastPath); err == nil {
		_ = json.Unmarshal(data, &m)
	}
	return m
}

func recordLast(x system.Executor, channel string, l Last) {
	m := ReadLast(x)
	if l.Error == "" {
		l.OK = l.Attempt
	} else if prev, ok := m[channel]; ok {
		l.OK = prev.OK
	}
	m[channel] = l
	if b, err := json.MarshalIndent(m, "", "  "); err == nil {
		_ = x.MkdirAll(events.Dir, 0o700, "root", "root")
		_ = x.WriteFile(LastPath, append(b, '\n'), 0o600, "root", "root")
	}
}

// Options adjust Senders for tests.
type Options struct {
	Client    *http.Client
	Location  *time.Location // quiet hours' timezone, default the box's
	Now       func() time.Time
	endpoints endpoints
}

// allowedListener accepts root's and system accounts' sockets and the
// admin's, never the agent's or another account's (ADR 0054).
func allowedListener(c *config.Config, x system.Executor) func(int) bool {
	admin := -1
	if u, err := x.LookupUser(c.Users.Admin.Name); err == nil && u.Name != c.Users.Agent.Name {
		admin = u.UID
	}
	return func(uid int) bool { return systemUID(uid) || uid == admin }
}

// Senders are the box's channels as senders, with the owner's choices
// applied, recording each send in LastPath.
func Senders(c *config.Config, x system.Executor, o Options) []events.Sender {
	if o.Client == nil {
		o.Client = newClient(allowedListener(c, x))
	}
	if o.Location == nil {
		o.Location = time.Local
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.endpoints == (endpoints{}) {
		o.endpoints = defaultEndpoints
	}
	quiet, _ := parseQuiet(c.Notify.QuietHours)
	var out []events.Sender
	for _, ch := range c.Notify.Channels {
		out = append(out, &Channel{cfg: c, ch: ch, x: x, o: o, quiet: quiet})
	}
	return out
}

// Channel is one configured channel as an events.Sender.
type Channel struct {
	cfg   *config.Config
	ch    config.Channel
	x     system.Executor
	o     Options
	quiet *quietHours
}

func (c *Channel) Name() string { return c.ch.Name }

// wants reports whether the owner wants e on this channel: a kind sent by
// default and not muted, at or above the channel's level.
func (c *Channel) wants(e events.Event) bool {
	k, ok := events.Lookup(e.Kind)
	return ok && k.Default && !c.cfg.Muted(e.Kind) && e.Level >= c.ch.LevelOf()
}

// hold is when e may go out, if quiet hours hold it now.
func (c *Channel) hold(e events.Event) (time.Time, bool) {
	if c.quiet == nil || (e.Level == events.Urgent && c.cfg.NotifyQuietHoursUrgent()) {
		return time.Time{}, false
	}
	return c.quiet.until(c.o.Now().In(c.o.Location))
}

// Send delivers e, unless the owner does not want it here (nil: nothing to
// retry) or quiet hours hold it (*events.HoldError).
func (c *Channel) Send(ctx context.Context, e events.Event) error {
	if !c.wants(e) {
		return nil
	}
	if until, held := c.hold(e); held {
		return &events.HoldError{Until: until}
	}
	return c.deliver(ctx, Render(e), e.Kind)
}

// SendDigest delivers the events quiet hours held as one message.
func (c *Channel) SendDigest(ctx context.Context, es []events.Event) error {
	var want []events.Event
	for _, e := range es {
		if c.wants(e) {
			want = append(want, e)
		}
	}
	switch len(want) {
	case 0:
		return nil
	case 1:
		return c.deliver(ctx, Render(want[0]), want[0].Kind)
	}
	return c.deliver(ctx, Digest(want), "digest")
}

func (c *Channel) deliver(ctx context.Context, m Message, kind string) error {
	t, err := newTransport(installed(c.x, c.ch), c.cfg.Box, c.ch, c.o.Client, c.o.endpoints)
	if err == nil {
		err = t.send(ctx, m)
	}
	l := Last{Attempt: c.o.Now().UTC(), Kind: kind}
	if err != nil {
		l.Error = err.Error()
	}
	recordLast(c.x, c.ch.Name, l)
	return err
}

// Test sends a test message on ch now, past mute, level and quiet hours,
// and returns what happened. It reads the secret files the config names
// when they are on the box, else the installed copies, and records
// nothing.
func Test(ctx context.Context, c *config.Config, x system.Executor, ch config.Channel, o Options) error {
	if o.Client == nil {
		o.Client = newClient(allowedListener(c, x))
	}
	if o.endpoints == (endpoints{}) {
		o.endpoints = defaultEndpoints
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	e := events.Event{Time: o.Now().UTC(), Source: events.SourceKeelbox, Kind: "test", Level: events.Info, Box: c.Box,
		Key: "test:" + ch.Name, Fields: map[string]string{"channel": ch.Name}}
	if err := e.Validate(); err != nil {
		return err
	}
	t, err := newTransport(configured(x, ch), c.Box, ch, o.Client, o.endpoints)
	if err != nil {
		return err
	}
	return t.send(ctx, Render(e))
}

// IsSendError reports whether err is a channel's refusal or failure,
// rather than keelbox's own.
func IsSendError(err error) bool {
	var s *SendError
	return errors.As(err, &s)
}

// quietHours is notify.quiet_hours in minutes since midnight.
type quietHours struct{ from, to int }

func parseQuiet(s string) (*quietHours, bool) {
	a, b, ok := strings.Cut(s, "-")
	if !ok {
		return nil, false
	}
	from, ok1 := minutes(a)
	to, ok2 := minutes(b)
	if !ok1 || !ok2 || from == to {
		return nil, false
	}
	return &quietHours{from, to}, true
}

func minutes(hhmm string) (int, bool) {
	h, m, ok := strings.Cut(hhmm, ":")
	hi, err1 := strconv.Atoi(h)
	mi, err2 := strconv.Atoi(m)
	if !ok || err1 != nil || err2 != nil || hi < 0 || hi > 23 || mi < 0 || mi > 59 {
		return 0, false
	}
	return hi*60 + mi, true
}

// until returns the end of the quiet hours now is in, or false when now is
// outside them. The hours may cross midnight.
func (q *quietHours) until(now time.Time) (time.Time, bool) {
	m := now.Hour()*60 + now.Minute()
	in := q.from <= m && m < q.to
	if q.from > q.to {
		in = m >= q.from || m < q.to
	}
	if !in {
		return time.Time{}, false
	}
	end := time.Date(now.Year(), now.Month(), now.Day(), q.to/60, q.to%60, 0, 0, now.Location())
	if !end.After(now) {
		end = end.AddDate(0, 0, 1)
	}
	return end, true
}

// Summary is one line for keelbox status: channels, the last send and the
// queue.
func Summary(c *config.Config, x system.Executor) string {
	n := len(c.Notify.Channels)
	if n == 0 {
		return ""
	}
	s := strconv.Itoa(n) + " channel"
	if n > 1 {
		s += "s"
	}
	last := ReadLast(x)
	var newest Last
	var failing []string
	for _, ch := range c.Notify.Channels {
		l, ok := last[ch.Name]
		if !ok {
			continue
		}
		if l.OK.After(newest.OK) {
			newest = l
		}
		if l.Error != "" {
			failing = append(failing, ch.Name)
		}
	}
	if !newest.OK.IsZero() {
		s += "; last sent " + newest.OK.In(time.Local).Format("2006-01-02 15:04") + " (" + newest.Kind + ")"
	} else {
		s += "; nothing sent yet"
	}
	if q, err := events.Pending(x); err == nil {
		s += "; " + strconv.Itoa(len(q)) + " queued"
	}
	if len(failing) > 0 {
		sort.Strings(failing)
		s += "; last send failed on " + strings.Join(failing, ", ") + " (keelbox notify test)"
	}
	return s
}
