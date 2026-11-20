package tidy

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

const dirFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC

type fingerprint struct {
	Device     int64
	Inode      uint64
	Bytes      int64
	ModifiedNS int64
}

func identity(st unix.Stat_t) fingerprint {
	return fingerprint{int64(st.Dev), st.Ino, st.Size, st.Mtim.Nano()}
}

func lastActive(st unix.Stat_t) time.Time {
	return time.Unix(0, max(st.Atim.Nano(), st.Mtim.Nano()))
}

func regular(st unix.Stat_t) bool { return st.Mode&unix.S_IFMT == unix.S_IFREG }

func openDir(root int, dir string, create bool) (int, error) {
	fd, err := unix.Openat(root, ".", dirFlags, 0)
	if err != nil {
		return -1, err
	}
	if dir == "." {
		return fd, nil
	}
	if !validPath(dir) {
		unix.Close(fd)
		return -1, fmt.Errorf("invalid folder %q", dir)
	}
	for _, part := range strings.Split(dir, "/") {
		next, err := unix.Openat(fd, part, dirFlags, 0)
		if create && errors.Is(err, fs.ErrNotExist) {
			err = unix.Mkdirat(fd, part, 0755)
			if err == nil {
				err = syncDir(fd)
			}
			if err == nil || errors.Is(err, fs.ErrExist) {
				next, err = unix.Openat(fd, part, dirFlags, 0)
			}
		}
		unix.Close(fd)
		if err != nil {
			return -1, fmt.Errorf("open folder %q: %w", dir, err)
		}
		fd = next
	}
	return fd, nil
}

func syncDir(fd int) error {
	err := unix.Fsync(fd)
	if errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOTSUP) {
		return nil
	}
	return err
}

func syncMove(root int, from, to string) error {
	for _, dir := range []string{path.Dir(from), path.Dir(to)} {
		fd, err := openDir(root, dir, false)
		if err != nil {
			return err
		}
		err = syncDir(fd)
		unix.Close(fd)
		if err != nil {
			return err
		}
	}
	return nil
}

func statAt(fd int, name string) (unix.Stat_t, error) {
	var st unix.Stat_t
	err := unix.Fstatat(fd, name, &st, unix.AT_SYMLINK_NOFOLLOW)
	return st, err
}

func lookup(root int, name string) (unix.Stat_t, error) {
	fd, err := openDir(root, path.Dir(name), false)
	if err != nil {
		return unix.Stat_t{}, err
	}
	defer unix.Close(fd)
	return statAt(fd, path.Base(name))
}

func available(root int, preferred string, reserved map[string]bool) (string, error) {
	dir, base := path.Dir(preferred), path.Base(preferred)
	fd, err := openDir(root, dir, false)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	if err == nil {
		defer unix.Close(fd)
	}
	for i := 1; ; i++ {
		name := base
		if i > 1 {
			name = numberedName(base, i)
		}
		candidate := path.Join(dir, name)
		if reserved[candidate] {
			continue
		}
		if fd < 0 {
			return candidate, nil
		}
		if _, err := statAt(fd, name); errors.Is(err, fs.ErrNotExist) {
			return candidate, nil
		} else if err != nil {
			return "", err
		}
	}
}

func numberedName(name string, n int) string {
	const maxBytes = 255
	suffix := fmt.Sprintf(" (%d)", n)
	ext := path.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	if len(suffix)+len(ext) >= maxBytes {
		stem, ext = name, ""
	}
	if limit := maxBytes - len(suffix) - len(ext); len(stem) > limit {
		for !utf8.RuneStart(stem[limit]) {
			limit--
		}
		stem = stem[:limit]
	}
	return stem + suffix + ext
}

func renameChecked(root int, from, to string, id fingerprint, quietBefore time.Time) error {
	src, err := openDir(root, path.Dir(from), false)
	if err != nil {
		return err
	}
	defer unix.Close(src)
	dst, err := openDir(root, path.Dir(to), false)
	if err != nil {
		return err
	}
	defer unix.Close(dst)
	st, err := statAt(src, path.Base(from))
	if err != nil {
		return err
	}
	if !regular(st) || identity(st) != id {
		return fmt.Errorf("file changed since it was recorded")
	}
	if !quietBefore.IsZero() && lastActive(st).After(quietBefore) {
		return fmt.Errorf("file was recently accessed")
	}
	return renameExclusive(src, path.Base(from), dst, path.Base(to))
}

func openDownloads(dir string) (*os.File, error) {
	fd, err := unix.Open(dir, dirFlags, 0)
	if err != nil {
		return nil, fmt.Errorf("open Downloads: %w", err)
	}
	return os.NewFile(uintptr(fd), dir), nil
}
