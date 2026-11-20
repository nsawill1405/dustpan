package tidy

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"time"

	"golang.org/x/sys/unix"
)

type Engine struct {
	Downloads string
	StateDir  string
	LargeFile func(name string, size int64)
}

type proposal struct {
	name string
	to   string
	stat unix.Stat_t
}

func (e Engine) open() (*os.File, string, error) {
	f, err := openDownloads(e.Downloads)
	if err != nil {
		return nil, "", err
	}
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		f.Close()
		return nil, "", err
	}
	journal := filepath.Join(e.StateDir, fmt.Sprintf("journal-%x-%x", st.Dev, st.Ino))
	return f, journal, nil
}

func scan(downloads *os.File, log []move, rules Rules, now time.Time, largeFile func(string, int64)) ([]proposal, error) {
	root := int(downloads.Fd())
	held := make(map[string]fingerprint)
	for _, m := range log {
		if m.State == restored {
			held[m.RestoredTo] = m.File
		}
	}
	names, err := downloads.Readdirnames(-1)
	if err != nil {
		return nil, err
	}
	// Filesystem iteration order is unspecified; keep plans and runs reproducible.
	slices.Sort(names)
	var checks []proposal
	var problems []error
	for _, name := range names {
		if ignored(name) {
			continue
		}
		st, err := statAt(root, name)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			problems = append(problems, fmt.Errorf("inspect %q: %w", name, err))
			continue
		}
		if !regular(st) || lastActive(st).After(now.Add(-rules.quiet)) || held[name] == identity(st) {
			continue
		}
		if rules.LargeBytes > 0 && st.Size > rules.LargeBytes {
			largeFile(name, st.Size)
			continue
		}
		if folder := rules.destination(name, lastActive(st), now); folder != "" {
			checks = append(checks, proposal{name, path.Join(folder, name), st})
		}
	}
	return checks, errors.Join(problems...)
}

func (e Engine) Sort(rules Rules, now time.Time, dryRun bool, out io.Writer) error {
	dir, journal, err := e.open()
	if err != nil {
		return err
	}
	defer dir.Close()
	root := int(dir.Fd())
	if !dryRun {
		unlock, err := lockState(journal)
		if err != nil {
			return err
		}
		defer unlock()
	}
	log, err := readJournal(journal)
	if err != nil {
		return err
	}
	if !dryRun {
		if err := reconcile(root, log); err != nil {
			return err
		}
	}
	largeFile := e.LargeFile
	if largeFile == nil {
		largeFile = func(name string, size int64) {
			fmt.Fprintf(out, "Large file (left in place): %q (%d bytes)\n", name, size)
		}
	}
	items, scanErr := scan(dir, log, rules, now, largeFile)
	if dryRun && len(items) == 0 && scanErr == nil {
		fmt.Fprintln(out, "Nothing to move.")
	}
	reserved := make(map[string]bool)
	run := now.UTC().Format("20060102T150405.000000000") + "-" + rand.Text()
	problems := []error{scanErr}
	for i, p := range items {
		dest, err := available(root, p.to, reserved)
		if err != nil {
			problems = append(problems, fmt.Errorf("plan %q: %w", p.name, err))
			continue
		}
		if dryRun {
			reserved[dest] = true
			fmt.Fprintf(out, "Would move %q → %q\n", p.name, dest)
			continue
		}
		dst, err := openDir(root, path.Dir(dest), true)
		if err != nil {
			problems = append(problems, fmt.Errorf("prepare destination for %q: %w", p.name, err))
			continue
		}
		unix.Close(dst)
		m := move{Run: run, Started: now, From: p.name, To: dest, File: identity(p.stat), State: moving,
			disk: filepath.Join(journal, fmt.Sprintf("%s-%08d.json", run, i))}
		if err := m.save(); err != nil {
			return err
		}
		if err := renameChecked(root, m.From, m.To, m.File, now.Add(-rules.quiet)); err != nil {
			m.State = cancelled
			problems = append(problems, fmt.Errorf("move %q: %w", m.From, err))
		} else {
			if err := syncMove(root, m.From, m.To); err != nil {
				return err
			}
			m.State = moved
			fmt.Fprintf(out, "Moved %q → %q\n", m.From, m.To)
		}
		if err := m.save(); err != nil {
			return errors.Join(append(problems, err)...)
		}
	}
	return errors.Join(problems...)
}

func (e Engine) Undo(now time.Time, today bool, out io.Writer) error {
	dir, journal, err := e.open()
	if err != nil {
		return err
	}
	defer dir.Close()
	root := int(dir.Fd())
	unlock, err := lockState(journal)
	if err != nil {
		return err
	}
	defer unlock()
	log, err := readJournal(journal)
	if err != nil {
		return err
	}
	if err := reconcile(root, log); err != nil {
		return err
	}
	var run string
	var attempted bool
	var problems []error
	for i := len(log) - 1; i >= 0; i-- {
		m := log[i]
		if m.State != moved {
			continue
		}
		if today {
			if m.Started.In(now.Location()).Format(time.DateOnly) != now.Format(time.DateOnly) {
				continue
			}
		} else if run == "" {
			run = m.Run
		} else if m.Run != run {
			break
		}
		attempted = true
		restoreTo, err := available(root, m.From, nil)
		if err != nil {
			problems = append(problems, fmt.Errorf("plan restore of %q: %w", m.To, err))
			continue
		}
		m.State, m.RestoredTo = restoring, restoreTo
		if err := m.save(); err != nil {
			return err
		}
		if err := renameChecked(root, m.To, m.RestoredTo, m.File, time.Time{}); err != nil {
			problems = append(problems, fmt.Errorf("restore %q: %w", m.To, err))
			m.State, m.RestoredTo = moved, ""
		} else {
			if err := syncMove(root, m.To, m.RestoredTo); err != nil {
				return err
			}
			m.State = restored
			fmt.Fprintf(out, "Restored %q → %q\n", m.To, m.RestoredTo)
		}
		if err := m.save(); err != nil {
			return errors.Join(append(problems, err)...)
		}
	}
	if !attempted {
		fmt.Fprintln(out, "Nothing to undo.")
	}
	return errors.Join(problems...)
}

func (e Engine) Status(out io.Writer) error {
	dir, journal, err := e.open()
	if err != nil {
		return err
	}
	defer dir.Close()
	log, err := readJournal(journal)
	if err != nil {
		return err
	}
	counts := make(map[string]int)
	for _, m := range log {
		counts[m.State]++
	}
	fmt.Fprintf(out, "Downloads: %s\nJournal: %s\nUndoable: %d; restored: %d; pending recovery: %d\n",
		e.Downloads, journal, counts[moved], counts[restored], counts[moving]+counts[restoring])
	return nil
}
