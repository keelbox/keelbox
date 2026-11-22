package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/notify"
)

// notifyArg takes notify's subcommand out of the positional arguments:
// keelbox notify test acme.
func notifyArg(pos []string) ([]string, string) {
	for i, p := range pos {
		if p == "test" {
			return append(append([]string(nil), pos[:i]...), pos[i+1:]...), p
		}
	}
	return pos, ""
}

// notifyTest is keelbox notify test on the box, as root: a test message on
// each channel through the real senders, with the secret files the config
// names (or the installed copies). It never queues or records anything.
func notifyTest(ctx context.Context, e *engine.Env, o options, stdout, stderr io.Writer) int {
	chans := e.Cfg.Notify.Channels
	if len(chans) == 0 {
		failf(stderr, "%s has no notify.channels; add one (%s/guides/notifications), then keelbox apply %s\n", e.Cfg.Box, docsURL, e.Cfg.Box)
		return 1
	}
	failed, tried := 0, 0
	for _, ch := range chans {
		if o.channel != "" && ch.Name != o.channel {
			continue
		}
		tried++
		label := fmt.Sprintf("%s (%s)", ch.Name, ch.Type)
		if err := notify.Test(ctx, e.Cfg, e.X, ch, notify.Options{}); err != nil {
			failed++
			fmt.Fprintf(stdout, "%-28s FAIL %v\n", label, err)
			continue
		}
		fmt.Fprintf(stdout, "%-28s ok\n", label)
	}
	switch {
	case tried == 0:
		failf(stderr, "no channel %q in notify.channels; keelbox notify test %s lists them all\n", o.channel, e.Cfg.Box)
		return 2
	case failed > 0:
		fmt.Fprintf(stdout, "%d of %s failed. Fix the channel's files or settings, then keelbox apply %s and test again.\n", failed, plural(tried, "channel", "channels"), e.Cfg.Box)
		return 1
	}
	fmt.Fprintf(stdout, "Sent a test message on %s.\n", plural(tried, "channel", "channels"))
	return 0
}
