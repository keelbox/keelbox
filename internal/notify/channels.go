package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/events"
	"github.com/keelbox/keelbox/internal/system"
)

// SecretDir holds root's copies of the channels' secret files (0700, files
// 0600): apply copies each *_file there, and only the senders read them.
const SecretDir = "/etc/keelbox/notify" //nolint:gosec // G101: a directory, not a credential

// Secret is one secret file a channel names.
type Secret struct {
	Key  string // the config key, such as topic_file
	Path string // the path the config gives
}

// Secrets lists the secret files ch names, in a fixed order.
func Secrets(ch config.Channel) []Secret {
	var out []Secret
	for _, f := range ch.SecretFiles() {
		out = append(out, Secret{f.Key, *f.Path})
	}
	return out
}

// SecretPath is root's copy of a channel's secret file, such as
// /etc/keelbox/notify/phone.topic.
func SecretPath(channel, key string) string {
	return SecretDir + "/" + channel + "." + strings.TrimSuffix(key, "_file")
}

// transport delivers a message on one channel.
type transport interface {
	send(ctx context.Context, m Message) error
}

// endpoints are the services' API addresses; tests point them at a local
// server.
type endpoints struct {
	pushover, telegram string
}

var defaultEndpoints = endpoints{pushover: "https://api.pushover.net/1/messages.json", telegram: "https://api.telegram.org"}

// installed reads root's copy of a channel's secret file.
func installed(x system.Executor, ch config.Channel) func(key string) ([]byte, error) {
	return func(key string) ([]byte, error) { return x.ReadFile(SecretPath(ch.Name, key)) }
}

// configured reads the secret file the config names when it is there and
// only root can read it (a laptop's staged copy, or a file on the box),
// else root's installed copy: keelbox notify test checks the config it is
// given, before an apply has installed it.
func configured(x system.Executor, ch config.Channel) func(key string) ([]byte, error) {
	paths := map[string]string{}
	for _, s := range Secrets(ch) {
		paths[s.Key] = config.ExpandHome(s.Path)
	}
	return func(key string) ([]byte, error) {
		if p := paths[key]; p != "" {
			if st, err := x.Stat(p); err == nil && st.Exists && !st.IsDir && !st.IsSymlink && st.Mode&0o077 == 0 {
				if data, err := x.ReadFile(p); err == nil {
					return data, nil
				}
			}
		}
		return installed(x, ch)(key)
	}
}

// newTransport reads ch's secrets with read and builds its sender. A
// missing or empty secret is an error that says how to install it.
func newTransport(read func(string) ([]byte, error), box string, ch config.Channel, client *http.Client, ep endpoints) (transport, error) {
	p := poster{channel: ch.Name, kind: ch.Type, client: client}
	secret := func(key string) (string, error) {
		data, err := read(key)
		v := strings.TrimSpace(string(data))
		if err != nil || v == "" {
			return "", p.fail(0, key+" is not installed on the box", "keelbox apply "+box+" installs it")
		}
		if strings.ContainsFunc(v, unicode.IsControl) && key != "url_file" {
			return "", p.fail(0, key+" holds more than one line", "keep one line in it, then keelbox apply "+box)
		}
		return v, nil
	}
	var err error
	get := func(key string) string {
		if err != nil {
			return ""
		}
		var v string
		v, err = secret(key)
		return v
	}
	switch ch.Type {
	case config.ChannelNtfy:
		t := ntfy{p: p, server: ch.NtfyServer(), topic: get("topic_file")}
		if ch.TokenFile != "" {
			t.token = get("token_file")
		}
		return t, err
	case config.ChannelPushover:
		t := pushover{p: p, endpoint: ep.pushover, token: get("token_file"), user: get("user_file")}
		return t, err
	case config.ChannelTelegram:
		t := telegram{p: p, endpoint: ep.telegram, chat: ch.ChatID, token: get("bot_token_file")}
		return t, err
	case config.ChannelWebhook:
		t := webhook{p: p, format: ch.Format, url: firstLine(get("url_file"))}
		if ch.HeaderFile != "" {
			t.header, t.value = headerLine(get("header_file"))
			if err == nil && t.header == "" {
				err = p.fail(0, "header_file is not one Name: value line", "fix the file, then keelbox apply "+box)
			}
		}
		return t, err
	}
	return nil, p.fail(0, "unknown channel type", "use ntfy, pushover, telegram or webhook")
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(l)
}

var headerNameRe = regexp.MustCompile(`^[A-Za-z0-9!#$%&'*+.^_|~-]{1,64}$`)

// headerLine splits "Name: value"; "" when it is not one.
func headerLine(s string) (string, string) {
	name, value, ok := strings.Cut(s, ":")
	name, value = strings.TrimSpace(name), strings.TrimSpace(value)
	if !ok || !headerNameRe.MatchString(name) || value == "" || strings.ContainsFunc(value, unicode.IsControl) {
		return "", ""
	}
	return http.CanonicalHeaderKey(name), value
}

// ntfy publishes as JSON to the server's root, so the topic travels in the
// body, not the URL, and any text survives (headers would not carry it).
// https://docs.ntfy.sh/publish/#publish-as-json
type ntfy struct {
	p                    poster
	server, topic, token string
}

var ntfyPriority = map[events.Level]int{events.Info: 3, events.Warn: 4, events.Urgent: 5}
var ntfyTags = map[events.Level][]string{events.Warn: {"warning"}, events.Urgent: {"rotating_light"}}

func (n ntfy) send(ctx context.Context, m Message) error {
	u, err := url.Parse(strings.TrimSuffix(n.server, "/") + "/")
	if err != nil {
		return n.p.fail(0, "the server is not a URL", "check notify.channels server")
	}
	body, _ := json.Marshal(map[string]any{
		"topic": n.topic, "title": truncate(m.Title, 250), "message": truncate(m.Text, 4000),
		"priority": ntfyPriority[m.Level], "tags": ntfyTags[m.Level],
	})
	h := http.Header{}
	if n.token != "" {
		h.Set("Authorization", "Bearer "+n.token)
	}
	return n.p.post(ctx, u, "application/json", body, h)
}

// pushover posts the form its API takes. Priority 2 (emergency) is never
// used: it repeats until acknowledged and needs retry and expire.
// https://pushover.net/api
type pushover struct {
	p                     poster
	endpoint, token, user string
}

var pushoverPriority = map[events.Level]string{events.Info: "-1", events.Warn: "0", events.Urgent: "1"}

func (o pushover) send(ctx context.Context, m Message) error {
	u, err := url.Parse(o.endpoint)
	if err != nil {
		return o.p.fail(0, "bad endpoint", "")
	}
	form := url.Values{"token": {o.token}, "user": {o.user}, "title": {truncate(m.Title, 250)},
		"message": {truncate(m.Text, 1024)}, "priority": {pushoverPriority[m.Level]}}
	return o.p.post(ctx, u, "application/x-www-form-urlencoded", []byte(form.Encode()), nil)
}

// telegram calls the Bot API's sendMessage. The token is in the URL path,
// which is why no error here ever carries the URL.
// https://core.telegram.org/bots/api#sendmessage
type telegram struct {
	p                     poster
	endpoint, chat, token string
}

func (t telegram) send(ctx context.Context, m Message) error {
	base, err := url.Parse(t.endpoint)
	if err != nil {
		return t.p.fail(0, "bad endpoint", "")
	}
	u := *base
	u.Path = strings.TrimSuffix(base.Path, "/") + "/bot" + t.token + "/sendMessage"
	var chat any = t.chat
	if !strings.HasPrefix(t.chat, "@") {
		chat = json.Number(t.chat)
	}
	body, _ := json.Marshal(map[string]any{
		"chat_id": chat, "text": truncate(m.Title+"\n"+m.Text, 4096), "disable_notification": m.Level == events.Info,
	})
	return t.p.post(ctx, &u, "application/json", body, nil)
}

// webhook posts to the URL in url_file: the event as JSON, with a "text"
// line that Slack, Mattermost and others show as it is, or only the text.
type webhook struct {
	p             poster
	format, url   string
	header, value string
}

func (w webhook) send(ctx context.Context, m Message) error {
	u, err := url.Parse(w.url)
	if err != nil || u.Host == "" || u.User != nil {
		return w.p.fail(0, "url_file does not hold a URL without credentials", "put one URL in the file, then apply again")
	}
	h := http.Header{}
	if w.header != "" {
		h.Set(w.header, w.value)
	}
	if w.format == "text" {
		return w.p.post(ctx, u, "text/plain; charset=utf-8", []byte(m.Line()+"\n"), h)
	}
	e := m.Event
	v := map[string]any{"text": m.Line(), "title": m.Title, "message": m.Text, "level": m.Level.String(), "kind": "digest"}
	if e.Kind != "" {
		v["kind"], v["box"], v["source"], v["time"], v["key"], v["fields"] = e.Kind, e.Box, e.Source, e.Time, e.Key, e.Fields
	}
	body, _ := json.Marshal(v)
	return w.p.post(ctx, u, "application/json", body, h)
}
