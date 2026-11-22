package cli

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/ui"
)

// The end of keelbox park, resume and resize, in the form of the end of
// keelbox up (upready.go): a headline that says what is now so, with the
// time the command took, and under it a card with what follows from that:
// what is billed, the address, what to start again, the command that
// undoes it. Off a terminal the headline is a sentence and the card its
// plain rows.

// outcome is how one of the three commands ended.
type outcome struct {
	// headline says what is now so: "acme is parked".
	headline string
	took     time.Duration
	// title and rows are the card's.
	title string
	rows  []ui.Line
}

// showOutcome prints the end of a command that did what it was asked.
func showOutcome(out io.Writer, st ui.Style, o outcome) {
	card := st
	if card.Width == 0 {
		// A person reads it either way: where the width is not known the
		// rows wrap to 80 columns, as a go-ahead's do.
		card.Width = card.Cols() + 1
	}
	box := card.Card(ui.Card{Title: o.title, Lines: o.rows, Fill: true})
	took := "in " + ui.Duration(o.took)
	var b strings.Builder
	b.WriteString("\n")
	if !st.Live {
		b.WriteString(st.Bold(o.headline+" "+took+".") + "\n")
	} else {
		head := "  " + st.Tick() + " " + st.Bold(o.headline)
		// The time ends where the card does.
		width := st.Cols()
		if st.UTF8 {
			width = ui.Width(box[:strings.IndexByte(box, '\n')])
		}
		if gap := width - ui.Width(head) - ui.Width(took); gap >= 2 {
			b.WriteString(head + strings.Repeat(" ", gap) + st.Dim(took) + "\n\n")
		} else {
			b.WriteString(head + "\n    " + st.Dim(took) + "\n\n")
		}
	}
	b.WriteString(box)
	_, _ = io.WriteString(out, b.String())
}

// workAgain are the rows that say how to pick the work up on a box whose
// server restarted or is a new one: what was running is not.
func workAgain(st ui.Style, cfg *config.Config, ref, stopped string) []ui.Line {
	open, what := "keelbox attach "+ref, "opens the agent's terminal"
	if !cfg.Tmux() {
		open, what = "keelbox ssh "+ref, "opens a shell as the agent"
	}
	return []ui.Line{
		ui.Row("start again", command(st, open)),
		ui.Row("", fmt.Sprintf("%s. Sessions and dev servers were stopped %s, as after a reboot; containers with a restart policy came back by themselves", what, stopped)),
	}
}

// parkedOutcome is the end of keelbox park.
func parkedOutcome(st ui.Style, box, ref string, took time.Duration, srv *cloudServer, pk *cloudParked, bill parkBill, times cloudTimes) outcome {
	gone := "the server"
	if bill.server != "" {
		gone += ", " + bill.server
	}
	return outcome{
		headline: box + " is parked", took: took, title: "While " + box + " is parked",
		rows: []ui.Line{
			ui.Row("still billed", billed(st, fmt.Sprintf("%s, which holds its disk (%s)", pk.ref, pk.detail))),
			ui.Row("not billed", amounts(fmt.Sprintf("%s. Its address %s is given up", gone, srv.ip))),
			ui.Row("bring it back", command(st, "keelbox resume "+ref)),
			ui.Row("", fmt.Sprintf("takes %s, typically. It comes back at a new address, with its files and sign-ins as they were", times.resume)),
		},
	}
}

// resumedOutcome is the end of keelbox resume: was is the address the box
// had when it was parked, as far as this computer knows it.
func resumedOutcome(st ui.Style, cfg *config.Config, ref string, took time.Duration, srv *cloudServer, pk *cloudParked, was string) outcome {
	box := cfg.Box
	address := []ui.Line{ui.Row("address", srv.ip)}
	if was != "" && was != srv.ip {
		address = []ui.Line{
			ui.Row("address", srv.ip+" (it was "+was+")"),
			ui.Row("", "anything that names the old one, such as a DNS record or another tool's SSH config, needs the new one"),
		}
	}
	rows := append(workAgain(st, cfg, ref, "when it was parked"), address...)
	if cfg.Tailscale() {
		rows = append(rows, ui.Row("tailnet", "it rejoins under its name by itself, unless its Tailscale key expired while it was parked; then: "+command(st, "keelbox login "+ref+" tailscale")))
	}
	rows = append(rows,
		ui.Row("billed again", "the server. "+capitalFirst(pk.ref)+" is deleted, and no longer billed"),
		ui.Row("check it", command(st, "keelbox doctor "+ref)))
	return outcome{headline: box + " is back", took: took, title: "Start working again", rows: rows}
}

// resizedOutcome is the end of keelbox resize: stays says what the resize
// left as it was, and back whether the size before can be changed back to.
func resizedOutcome(st ui.Style, cfg *config.Config, ref string, took time.Duration, from, to cloudSize, withDisk bool, stays string, back bool) outcome {
	rows := []ui.Line{
		ui.Row("size", to.name+": "+billed(st, to.phrase(withDisk))),
		ui.Row("it was", from.name+": "+amounts(from.phrase(withDisk))),
	}
	rows = append(rows, priceChange(from, to, "before")...)
	rows = append(rows, ui.Row("kept", amounts(stays)))
	rows = append(rows, workAgain(st, cfg, ref, "by the restart")...)
	if back {
		rows = append(rows, ui.Row("change back", command(st, "keelbox resize "+ref+" --size "+from.name)))
	}
	return outcome{headline: cfg.Box + " is " + to.name + " now", took: took, title: "After the resize", rows: rows}
}

// priceChange is the row that says what a change of size does to the
// month's bill, where the cloud gives both prices: more than now, before
// the change, and more than before, after it.
func priceChange(from, to cloudSize, than string) []ui.Line {
	switch diff := to.price - from.price; {
	case from.price <= 0 || to.price <= 0:
	case diff > 0:
		return []ui.Line{ui.Row("costs", amounts(fmt.Sprintf("about %s %.2f a month more than %s", to.currency, diff, than)))}
	case diff < 0:
		return []ui.Line{ui.Row("saves", amounts(fmt.Sprintf("about %s %.2f a month", to.currency, -diff)))}
	}
	return nil
}
