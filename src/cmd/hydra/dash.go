package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/chethan-9817/pineapple/src/internal/tui"
)

// dash is the live state of one running job.
type dash struct {
	title, sub        string
	total             int
	done, pass, fail  int
	recent            []bool
	kill, acked, keys []float64
	last              string
	started           time.Time
	frame             int
	finished          bool
}

func newDash(title, sub string, total int) *dash {
	return &dash{title: title, sub: sub, total: total, started: time.Now()}
}

func push(s []float64, v float64) []float64 {
	s = append(s, v)
	if len(s) > 60 {
		s = s[len(s)-60:]
	}
	return s
}

func (d *dash) onEvent(ev event) {
	switch ev.kind {
	case evIter:
		d.done, d.total = ev.i, ev.n
		d.pass++
		d.recent = append(d.recent, true)
		if v, err := strconv.ParseFloat(ev.kv["kill_ms"], 64); err == nil {
			d.kill = push(d.kill, v)
		}
		if v, err := strconv.ParseFloat(ev.kv["acked"], 64); err == nil {
			d.acked = push(d.acked, v)
		}
		if v, err := strconv.ParseFloat(ev.kv["keys"], 64); err == nil {
			d.keys = push(d.keys, v)
		}
		d.last = fmt.Sprintf("iter %d · killed at %s ms · %s ACKed · %s in flight · %s keys · recovered in %s ms",
			ev.i, orDash(ev.kv["kill_ms"]), orDash(ev.kv["acked"]), orDash(ev.kv["inflight"]), orDash(ev.kv["keys"]), orDash(ev.kv["open_ms"]))
		if x := ev.kv["rejected"]; x != "" && x != "0" {
			d.last += " · " + x + " refused (ERR FULL)"
		}
		for _, k := range []string{"probe", "compact", "shrink"} {
			if v := ev.kv[k]; v != "" {
				d.last += " · " + k + " " + v
			}
		}
	case evCase:
		d.done++
		if ev.pass {
			d.pass++
		} else {
			d.fail++
		}
		d.recent = append(d.recent, ev.pass)
		d.last = ev.name
	case evFail:
		d.fail++
		d.recent = append(d.recent, false)
		d.last = tui.Red(ev.line)
	}
	if len(d.recent) > 60 {
		d.recent = d.recent[len(d.recent)-60:]
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func (d *dash) frac() float64 {
	if d.total <= 0 {
		return 0
	}
	f := float64(d.done) / float64(d.total)
	if f > 1 {
		f = 1
	}
	return f
}

// lines renders the dashboard as a box of the given width.
func (d *dash) lines(width int) []string {
	el := time.Since(d.started)
	inner := width - 4
	barW := inner - 34
	if barW < 10 {
		barW = 10
	}
	spin := " "
	if !d.finished {
		spin = tui.Cyan(tui.SpinnerFrames[d.frame%len(tui.SpinnerFrames)])
	} else if d.fail == 0 {
		spin = tui.OK()
	} else {
		spin = tui.Fail()
	}
	d.frame++

	eta := ""
	if d.done > 0 && d.total > d.done && !d.finished {
		rem := time.Duration(float64(el) / float64(d.done) * float64(d.total-d.done))
		eta = "  ETA " + tui.Dur(rem)
	}
	progress := fmt.Sprintf("%s %s %d/%d  %3.0f%%%s", spin, tui.Bar(d.frac(), barW), d.done, d.total, 100*d.frac(), eta)

	rate := 0.0
	if el > 0 {
		rate = float64(d.done) / el.Seconds()
	}
	failS := tui.Gray("0 failed")
	if d.fail > 0 {
		failS = tui.Red(tui.Bold(fmt.Sprintf("%d FAILED", d.fail)))
	}
	counts := fmt.Sprintf("%s %s   %s   %s   %s", tui.OK(), tui.Green(fmt.Sprintf("%d passed", d.pass)), failS, tui.Gray(fmt.Sprintf("%.1f/s", rate)), tui.Gray("elapsed "+tui.Dur(el)))

	var blocks strings.Builder
	for _, ok := range d.recent {
		if ok {
			blocks.WriteString(tui.Green("▮"))
		} else {
			blocks.WriteString(tui.Red("▮"))
		}
	}
	lines := []string{progress, counts, tui.Gray("recent ") + blocks.String()}
	sw := inner - 9
	if sw > 60 {
		sw = 60
	}
	if len(d.kill) > 0 {
		lines = append(lines, tui.Gray("kill ms ")+tui.Spark(d.kill, sw))
	}
	if len(d.acked) > 0 {
		lines = append(lines, tui.Gray("ACKed   ")+tui.Spark(d.acked, sw))
	}
	if len(d.keys) > 0 {
		lines = append(lines, tui.Gray("keys    ")+tui.Spark(d.keys, sw))
	}
	if d.last != "" {
		lines = append(lines, tui.Gray("last    ")+tui.Trunc(tui.Strip(d.last), inner-8))
	}
	title := tui.Bold(tui.Gradient("HYDRA", 196, 208)) + " " + tui.Bold(d.title)
	if d.sub != "" {
		title += " " + tui.Gray("· "+d.sub)
	}
	border := tui.Cyan
	if d.fail > 0 {
		border = tui.Red
	} else if d.finished {
		border = tui.Green
	}
	return tui.Box(title, lines, width, border)
}

// miniLine is the one-line form used when many jobs run in a row.
func (d *dash) miniLine(width int) string {
	spin := tui.Cyan(tui.SpinnerFrames[d.frame%len(tui.SpinnerFrames)])
	d.frame++
	if d.finished {
		spin = tui.OK()
		if d.fail > 0 {
			spin = tui.Fail()
		}
	}
	name := tui.PadRight(tui.Trunc(d.title, 36), 36)
	barW := width - 36 - 28
	if barW < 8 {
		barW = 8
	}
	return fmt.Sprintf("  %s %s %s %d/%d %s", spin, name, tui.Bar(d.frac(), barW), d.done, d.total, tui.Gray(tui.Dur(time.Since(d.started))))
}
