// Command claude-inhibit holds a systemd-logind sleep inhibitor while any
// Claude Code session is working, and releases it as soon as none are.
//
// "Working" means a live session whose status is not idle, or (unless
// -background=false) an idle session still running a Bash-tool background
// task or Monitor. Interrupting an agent needs no special handling: its
// status simply returns to idle.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/MStarvik/claude-inhibit/internal/logind"
	"github.com/MStarvik/claude-inhibit/internal/sessions"
)

func main() {
	var (
		interval   = flag.Duration("interval", 10*time.Second, "how often to rescan even without file changes; only catches work ending (exited processes, finished background tasks), since work starting always rewrites a session file")
		background = flag.Bool("background", true, "count idle sessions with a running background task or Monitor as working")
		what       = flag.String("what", "idle", "colon-separated locks to take: idle, sleep, shutdown, handle-lid-switch, … (idle blocks only the automatic idle action; sleep would also block deliberate suspends)")
		mode       = flag.String("mode", "block", `inhibitor mode: "block" or "delay"`)
		once       = flag.Bool("once", false, "print working sessions and exit")
	)
	flag.Parse()
	log.SetFlags(0) // journald adds timestamps

	scanner, err := sessions.DefaultScanner(*background)
	if err != nil {
		log.Fatal(err)
	}

	if *once {
		active, err := scanner.Working()
		if err != nil {
			log.Fatal(err)
		}
		for _, a := range active {
			fmt.Printf("%d\t%s\t%s\t%s\n", a.PID, a.Name, describe(a), a.Cwd)
		}
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	d := &daemon{scanner: scanner, what: *what, mode: *mode}
	defer d.release()
	if err := d.run(ctx, *interval); err != nil {
		log.Fatal(err)
	}
}

func describe(a sessions.Active) string {
	if a.Reason == sessions.ReasonBackground {
		return "background"
	}
	return a.Status
}

type daemon struct {
	scanner    sessions.Scanner
	what, mode string

	bus  *logind.Client
	lock *logind.Lock
	last string // description of the working set, to log only changes
}

func (d *daemon) run(ctx context.Context, interval time.Duration) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer watcher.Close()
	watching := false
	watch := func() {
		if !watching && watcher.Add(d.scanner.Dir) == nil {
			watching = true
		}
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	// Session files are rewritten in bursts; coalesce events before scanning.
	debounce := time.NewTimer(0)

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			watch() // the directory may not have existed at startup
			d.update()
		case <-debounce.C:
			watch()
			d.update()
		case _, ok := <-watcher.Events:
			if !ok {
				return nil
			}
			debounce.Reset(200 * time.Millisecond)
		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			log.Printf("watch: %v", err)
			if strings.Contains(err.Error(), "removed") {
				watching = false
			}
			// Events may have been lost (e.g. queue overflow): rescan now.
			debounce.Reset(200 * time.Millisecond)
		}
	}
}

func (d *daemon) update() {
	active, err := d.scanner.Working()
	if err != nil {
		log.Printf("scan: %v", err)
		return
	}

	desc := make([]string, len(active))
	for i, a := range active {
		desc[i] = a.String()
	}
	summary := strings.Join(desc, ", ")

	if len(active) == 0 {
		if d.lock != nil {
			d.release()
			log.Print("idle: released inhibitor")
		}
		d.last = ""
		return
	}

	if d.lock == nil {
		if err := d.acquire(); err != nil {
			log.Printf("inhibit: %v", err)
			return // retried on the next scan
		}
		log.Printf("working: took %s inhibitor (%s)", d.what, summary)
	} else if summary != d.last {
		log.Printf("working: %s", summary)
	}
	d.last = summary
}

func (d *daemon) acquire() error {
	if d.bus == nil {
		bus, err := logind.Connect()
		if err != nil {
			return err
		}
		d.bus = bus
	}
	lock, err := d.bus.Inhibit(d.what, "Claude Code", "Agent working", d.mode)
	if err != nil {
		// The connection may be dead (e.g. dbus restarted); reconnect next time.
		d.bus.Close()
		d.bus = nil
		return err
	}
	d.lock = lock
	return nil
}

func (d *daemon) release() {
	if err := d.lock.Release(); err != nil {
		log.Printf("release: %v", err)
	}
	d.lock = nil
}
