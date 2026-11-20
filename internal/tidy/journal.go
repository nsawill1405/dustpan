package tidy

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const (
	moving    = "moving"
	moved     = "moved"
	restoring = "restoring"
	restored  = "restored"
	cancelled = "cancelled"
)

type move struct {
	Run        string
	Started    time.Time
	From       string
	To         string
	RestoredTo string
	File       fingerprint
	State      string
	disk       string
}

func (m move) valid() bool {
	if m.Run == "" || m.Started.IsZero() || !validPath(m.From) ||
		path.Base(m.From) != m.From || !validPath(m.To) || path.Dir(m.To) == "." {
		return false
	}
	switch m.State {
	case moving, moved, cancelled:
		return m.RestoredTo == ""
	case restoring, restored:
		return validPath(m.RestoredTo) && path.Base(m.RestoredTo) == m.RestoredTo
	}
	return false
}

func lockState(dir string) (func(), error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() { f.Close() }, nil
}

func readJournal(dir string) ([]move, error) {
	files, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var log []move
	for _, f := range files {
		if !strings.HasSuffix(f.Name(), ".json") {
			continue
		}
		name := filepath.Join(dir, f.Name())
		data, err := os.ReadFile(name)
		if err != nil {
			return nil, err
		}
		var m move
		if err := json.Unmarshal(data, &m); err != nil || !m.valid() {
			return nil, fmt.Errorf("invalid journal record %q; no files changed", name)
		}
		m.disk = name
		log = append(log, m)
	}
	return log, nil
}

func (m move) save() error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return WriteAtomic(m.disk, append(data, '\n'), 0600)
}

// WriteAtomic publishes a complete file or leaves its previous version intact.
func WriteAtomic(name string, data []byte, mode fs.FileMode) error {
	dir := filepath.Dir(name)
	f, err := os.CreateTemp(dir, ".dustpan-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	writeErr := f.Chmod(mode)
	if writeErr == nil {
		_, writeErr = f.Write(data)
	}
	if writeErr == nil {
		writeErr = f.Sync()
	}
	if err := errors.Join(writeErr, f.Close()); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), name); err != nil {
		return err
	}
	return syncDirectoryPath(dir)
}

func syncDirectoryPath(name string) error {
	d, err := os.Open(name)
	if err != nil {
		return err
	}
	defer d.Close()
	return syncDir(int(d.Fd()))
}

func syncJournalParents(dir string) error {
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return err
	}
	for {
		parent := filepath.Dir(dir)
		// An unwritable ancestor cannot contain a directory entry created by Dustpan.
		err := unix.Faccessat(unix.AT_FDCWD, parent, unix.W_OK, unix.AT_EACCESS)
		if errors.Is(err, fs.ErrPermission) || errors.Is(err, unix.EROFS) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("check journal parent %q: %w", parent, err)
		}
		if err := syncDirectoryPath(parent); err != nil {
			return fmt.Errorf("sync journal parent %q: %w", parent, err)
		}
		if parent == dir || filepath.Dir(parent) == parent {
			return nil
		}
		dir = parent
	}
}

func matches(root int, name string, id fingerprint) (bool, error) {
	st, err := lookup(root, name)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil && regular(st) && identity(st) == id, err
}

func reconcile(root int, log []move) error {
	for i := range log {
		m := &log[i]
		var from, to string
		switch m.State {
		case moving:
			from, to = m.From, m.To
		case restoring:
			from, to = m.To, m.RestoredTo
		default:
			continue
		}
		stillAtSource, err := matches(root, from, m.File)
		if err != nil {
			return err
		}
		if stillAtSource {
			if m.State == moving {
				m.State = cancelled
			} else {
				m.State, m.RestoredTo = moved, ""
			}
		} else {
			ok, err := matches(root, to, m.File)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("interrupted move: expected unchanged file at %q or %q; check both paths before retrying", from, to)
			}
			if err := syncMove(root, from, to); err != nil {
				return err
			}
			if m.State == moving {
				m.State = moved
			} else {
				m.State = restored
			}
		}
		if err := m.save(); err != nil {
			return err
		}
	}
	return nil
}
