package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/nsawill1405/dustpan/internal/tidy"
)

const scanInterval = 15 * time.Second

func watch(ctx context.Context, opts options, out, errOut io.Writer) error {
	var events <-chan fsnotify.Event
	var watchErrors <-chan error
	w, err := fsnotify.NewWatcher()
	if err == nil {
		defer w.Close()
		err = w.Add(opts.engine.Downloads)
		if err == nil {
			events, watchErrors = w.Events, w.Errors
		}
	}
	if err != nil {
		fmt.Fprintf(errOut, "Filesystem notifications unavailable; scanning every %s: %v\n", scanInterval, err)
	}
	fmt.Fprintf(out, "Watching %q\n", opts.engine.Downloads)
	tick := time.NewTicker(scanInterval)
	defer tick.Stop()
	var lastProblem string
	var previousLarge map[string]int64
	var lastScan time.Time
	run := func() {
		lastScan = time.Now()
		rules, err := tidy.LoadRules(opts.config)
		if err == nil {
			large := make(map[string]int64)
			engine := opts.engine
			engine.LargeFile = func(name string, size int64) {
				large[name] = size
				if previousLarge[name] != size {
					fmt.Fprintf(out, "Large file (left in place): %q (%d bytes)\n", name, size)
				}
			}
			err = engine.Sort(rules, lastScan, false, out)
			previousLarge = large
		}
		if err == nil {
			lastProblem = ""
		} else if err.Error() != lastProblem {
			fmt.Fprintln(errOut, err)
			lastProblem = err.Error()
		}
	}
	run()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			if time.Since(lastScan) >= 2*time.Second {
				run()
			}
		case event, ok := <-events:
			if !ok {
				events = nil
			} else if event.Has(fsnotify.Create|fsnotify.Write|fsnotify.Rename) && time.Since(lastScan) >= 2*time.Second {
				run()
			}
		case err, ok := <-watchErrors:
			if !ok {
				watchErrors = nil
			} else {
				fmt.Fprintf(errOut, "Filesystem watcher: %v\n", err)
			}
		}
	}
}
