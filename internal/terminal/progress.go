package terminal

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
)

// Loading returns a stop function that waits for the progress line to be erased.
func (d *Display) Loading(ctx context.Context, label string) func() {
	if !d.tty {
		return func() {}
	}
	switch strings.ToLower(os.Getenv("GH_SPINNER_DISABLED")) {
	case "", "0", "false", "off", "no":
	default:
		fmt.Fprintln(d.out, label)
		return func() {}
	}
	const frames = "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"
	// The fixed ASCII label fits a single line even on a narrow terminal.
	if d.width < 4 {
		return func() {}
	}
	if len(label) > d.width-3 {
		label = label[:d.width-3]
	}
	frame := func(r rune) { fmt.Fprintf(d.out, "\r%s %s", d.paint("36", string(r)), label) }
	frame('⠋')
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		defer fmt.Fprintf(d.out, "\r%s\r", strings.Repeat(" ", len(label)+2))
		tick := time.NewTicker(80 * time.Millisecond)
		defer tick.Stop()
		glyphs := []rune(frames)
		for i := 1; ; i++ {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-tick.C:
				frame(glyphs[i%len(glyphs)])
			}
		}
	}()
	return func() { close(stop); <-done }
}
