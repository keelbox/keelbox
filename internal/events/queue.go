package events

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/keelbox/keelbox/internal/system"
)

// Paths on the box. The directory is root's alone: the agent can neither
// read what is queued nor add to it.
const (
	Dir        = "/var/lib/keelbox/notify"
	QueueDir   = Dir + "/queue"
	CursorPath = Dir + "/cursor.json"
)

// MaxAge is how long an event waits for its channels before it is dropped.
const MaxAge = 24 * time.Hour

// Sender delivers an event on one channel. Errors must not carry the
// channel's secrets: the queue keeps only whether a send worked.
type Sender interface {
	Name() string
	Send(ctx context.Context, e Event) error
}

// Digester is a Sender that can deliver several events as one message.
// Dispatch hands it every due event that was held (quiet hours) at once,
// so the owner gets one digest when quiet hours end, not a burst.
type Digester interface {
	Sender
	SendDigest(ctx context.Context, es []Event) error
}

// HoldError is what a Sender returns for an event it will not send yet,
// such as one below urgent during quiet hours. Dispatch keeps the event
// until Until without counting a failed attempt, and marks it held.
type HoldError struct{ Until time.Time }

func (h *HoldError) Error() string { return "held until " + h.Until.Format(time.RFC3339) }

// Nop is a Sender that delivers nothing and always succeeds, for tests and
// for sources that run before any channel exists.
type Nop struct{}

func (Nop) Name() string                      { return "nop" }
func (Nop) Send(context.Context, Event) error { return nil }

// Queued is one event in the queue, with its delivery state.
type Queued struct {
	Name     string    `json:"-"`
	Event    Event     `json:"event"`
	Attempts int       `json:"attempts"`
	Next     time.Time `json:"next"`
	// Sent lists the channels that have the event, so a retry goes only
	// to the ones that failed.
	Sent []string `json:"sent,omitempty"`
	// Held is set once a sender held the event (quiet hours): when it is
	// due again it goes out in a digest.
	Held bool `json:"held,omitempty"`
}

// Emit validates e and adds it to the queue, one file per event, named by
// time so the queue reads in order. It does not send: Dispatch does.
func Emit(x system.Executor, e Event) (string, error) {
	if err := e.Validate(); err != nil {
		return "", err
	}
	if err := x.MkdirAll(Dir, 0o700, "root", "root"); err != nil {
		return "", err
	}
	if err := x.MkdirAll(QueueDir, 0o700, "root", "root"); err != nil {
		return "", err
	}
	var r [4]byte
	if _, err := rand.Read(r[:]); err != nil {
		return "", err
	}
	name := fmt.Sprintf("%020d-%s.json", e.Time.UnixNano(), hex.EncodeToString(r[:]))
	q := Queued{Name: name, Event: e, Next: e.Time}
	return name, write(x, &q)
}

func write(x system.Executor, q *Queued) error {
	b, err := json.Marshal(q)
	if err != nil {
		return err
	}
	return x.WriteFile(QueueDir+"/"+q.Name, append(b, '\n'), 0o600, "root", "root")
}

// Pending returns the queue, oldest first. An unreadable entry is removed,
// since nothing could ever send it.
func Pending(x system.Executor) ([]Queued, error) {
	names, err := x.ReadDir(QueueDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Queued
	for _, n := range names {
		if !strings.HasSuffix(n, ".json") {
			continue
		}
		data, err := x.ReadFile(QueueDir + "/" + n)
		if err != nil {
			return out, err
		}
		var q Queued
		if json.Unmarshal(data, &q) != nil || q.Event.Validate() != nil {
			_ = x.Remove(QueueDir + "/" + n)
			continue
		}
		q.Name = n
		out = append(out, q)
	}
	return out, nil
}

// Delivery is what Dispatch did.
type Delivery struct {
	Sent, Waiting, Dropped int
}

// backoff is the wait after the nth failed attempt: 2, 4, 8 minutes and
// so on, at most an hour.
func backoff(n int) time.Duration {
	d := 2 * time.Minute
	for i := 1; i < n && d < time.Hour; i++ {
		d *= 2
	}
	return min(d, time.Hour)
}

// Dispatch sends every queued event that is due to each sender that does
// not have it yet. An event leaves the queue once every sender has it, or
// once it is older than MaxAge. With no senders nothing is sent and events
// wait until they expire, so a channel configured within a day still gets
// them.
func Dispatch(ctx context.Context, x system.Executor, senders []Sender, now time.Time) (Delivery, error) {
	var d Delivery
	queue, err := Pending(x)
	if err != nil {
		return d, err
	}
	var due []*Queued
	for i := range queue {
		q := &queue[i]
		if now.Sub(q.Event.Time) > MaxAge {
			if err := x.Remove(QueueDir + "/" + q.Name); err != nil {
				return d, err
			}
			d.Dropped++
			continue
		}
		if len(senders) == 0 || now.Before(q.Next) {
			d.Waiting++
			continue
		}
		due = append(due, q)
	}
	digest(ctx, senders, due)
	for _, q := range due {
		path := QueueDir + "/" + q.Name
		failed := false
		var hold time.Time
		for _, s := range senders {
			if contains(q.Sent, s.Name()) {
				continue
			}
			err := s.Send(ctx, q.Event)
			var h *HoldError
			switch {
			case errors.As(err, &h):
				if hold.IsZero() || h.Until.Before(hold) {
					hold = h.Until
				}
				continue
			case err != nil:
				failed = true
				continue
			}
			q.Sent = append(q.Sent, s.Name())
		}
		if !failed && hold.IsZero() {
			if err := x.Remove(path); err != nil {
				return d, err
			}
			d.Sent++
			continue
		}
		if failed {
			q.Attempts++
			q.Next = now.Add(backoff(q.Attempts))
		}
		if !hold.IsZero() {
			q.Held = true
			if !failed || hold.Before(q.Next) {
				q.Next = hold
			}
		}
		if err := write(x, q); err != nil {
			return d, err
		}
		d.Waiting++
	}
	return d, nil
}

// digest sends the due held events as one message to each sender that
// can, when there are at least two. Senders that cannot, or a digest that
// fails, leave the events to be sent one by one.
func digest(ctx context.Context, senders []Sender, due []*Queued) {
	for _, s := range senders {
		dg, ok := s.(Digester)
		if !ok {
			continue
		}
		var batch []*Queued
		for _, q := range due {
			if q.Held && !contains(q.Sent, s.Name()) {
				batch = append(batch, q)
			}
		}
		if len(batch) < 2 {
			continue
		}
		es := make([]Event, len(batch))
		for i, q := range batch {
			es[i] = q.Event
		}
		if dg.SendDigest(ctx, es) != nil {
			continue
		}
		for _, q := range batch {
			q.Sent = append(q.Sent, s.Name())
		}
	}
}
