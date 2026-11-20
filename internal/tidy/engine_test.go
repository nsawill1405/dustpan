package tidy

import (
	"bytes"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var testNow = time.Date(2026, 10, 15, 12, 0, 0, 0, time.Local)

func fixture(t *testing.T) (Engine, Rules) {
	t.Helper()
	e := Engine{Downloads: t.TempDir(), StateDir: filepath.Join(t.TempDir(), "new", "nested", "state")}
	r, err := LoadRules("")
	if err != nil {
		t.Fatal(err)
	}
	return e, r
}

func add(t *testing.T, dir, name, data string, mod time.Time) string {
	t.Helper()
	f := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(f), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(f, mod, mod); err != nil {
		t.Fatal(err)
	}
	return f
}

func present(t *testing.T, dir, name, want string) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil || string(got) != want {
		t.Fatalf("%s: want %q, got %q (%v)", name, want, got, err)
	}
}

func absent(t *testing.T, dir, name string) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(dir, name)); !os.IsNotExist(err) {
		t.Fatalf("%s should not exist, got %v", name, err)
	}
}

func TestPreviewAndSort(t *testing.T) {
	e, r := fixture(t)
	r.LargeBytes = 10
	old := testNow.Add(-time.Hour)
	add(t, e.Downloads, "photo.JPG", "cat", old)
	add(t, e.Downloads, "manual.pdf", "read me", old)
	add(t, e.Downloads, "backup.tar.gz", "backup", old)
	add(t, e.Downloads, "setup.dmg", "install", old)
	add(t, e.Downloads, "stale.data", "archive", testNow.AddDate(0, -2, 0))
	add(t, e.Downloads, "unknown.data", "stay", old)
	add(t, e.Downloads, ".hidden.pdf", "stay", old)
	add(t, e.Downloads, "browser.pdf.part", "working", old)
	add(t, e.Downloads, "recent.png", "working", testNow)
	add(t, e.Downloads, "large.zip", strings.Repeat("x", 11), old)
	add(t, e.Downloads, "Folder/photo.jpg", "nested", old)
	used := add(t, e.Downloads, "opened.pdf", "recently read", old)
	if err := os.Chtimes(used, testNow, old); err != nil {
		t.Fatal(err)
	}
	target := add(t, t.TempDir(), "outside", "outside", old)
	if err := os.Symlink(target, filepath.Join(e.Downloads, "link.jpg")); err != nil {
		t.Fatal(err)
	}

	var plan bytes.Buffer
	if err := e.Sort(r, testNow, true, &plan); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Images/photo.JPG", "Documents/manual.pdf", "Archives/backup.tar.gz",
		"Installers/setup.dmg", "Old/2026-08/stale.data", "Large file (left in place): \"large.zip\""} {
		if !strings.Contains(plan.String(), want) {
			t.Errorf("preview missing %q: %s", want, plan.String())
		}
	}
	for _, not := range []string{"recent.png", "browser.pdf.part", "hidden.pdf", "link.jpg", "opened.pdf"} {
		if strings.Contains(plan.String(), not) {
			t.Errorf("preview includes %q", not)
		}
	}
	absent(t, e.Downloads, "Images")
	absent(t, filepath.Dir(e.StateDir), filepath.Base(e.StateDir))

	if err := e.Sort(r, testNow, false, io.Discard); err != nil {
		t.Fatal(err)
	}
	present(t, e.Downloads, "Images/photo.JPG", "cat")
	present(t, e.Downloads, "Documents/manual.pdf", "read me")
	present(t, e.Downloads, "Archives/backup.tar.gz", "backup")
	present(t, e.Downloads, "Installers/setup.dmg", "install")
	present(t, e.Downloads, "Old/2026-08/stale.data", "archive")
	present(t, e.Downloads, "Folder/photo.jpg", "nested")
	present(t, e.Downloads, "opened.pdf", "recently read")
	present(t, e.Downloads, "large.zip", strings.Repeat("x", 11))
	present(t, e.Downloads, "link.jpg", "outside")
	absent(t, e.Downloads, "photo.JPG")
}

func TestMoveAndUndoDoNotOverwrite(t *testing.T) {
	e, r := fixture(t)
	old := testNow.Add(-time.Hour)
	add(t, e.Downloads, "report.pdf", "incoming", old)
	add(t, e.Downloads, "Documents/report.pdf", "existing", old)
	if err := e.Sort(r, testNow, false, io.Discard); err != nil {
		t.Fatal(err)
	}
	present(t, e.Downloads, "Documents/report (2).pdf", "incoming")
	present(t, e.Downloads, "Documents/report.pdf", "existing")

	add(t, e.Downloads, "report.pdf", "new download", testNow)
	if err := e.Undo(testNow, false, io.Discard); err != nil {
		t.Fatal(err)
	}
	present(t, e.Downloads, "report.pdf", "new download")
	info, err := os.Stat(filepath.Join(e.Downloads, "report (2).pdf"))
	if err != nil || !info.ModTime().Equal(old) {
		t.Fatalf("restored file metadata: %v (%v)", info, err)
	}
	absent(t, e.Downloads, "Documents/report (2).pdf")
	// Close and reopen via another Sort, as the independent watcher would.
	if err := e.Sort(r, testNow.Add(2*time.Minute), false, io.Discard); err != nil {
		t.Fatal(err)
	}
	present(t, e.Downloads, "report (2).pdf", "incoming")
}

func TestUndoBatchAndLocalDay(t *testing.T) {
	e, r := fixture(t)
	local := time.FixedZone("UTC+8", 8*3600)
	yesterday := time.Date(2026, 10, 14, 23, 0, 0, 0, local)
	thisMorning := yesterday.Add(2 * time.Hour)
	for i, now := range []time.Time{yesterday, thisMorning, thisMorning.Add(time.Minute), thisMorning.Add(2 * time.Minute)} {
		name := []string{"yesterday.pdf", "today1.pdf", "today2.pdf", "today3.pdf"}[i]
		add(t, e.Downloads, name, name, now.Add(-time.Hour))
		if err := e.Sort(r, now, false, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.Undo(thisMorning, false, io.Discard); err != nil {
		t.Fatal(err)
	}
	present(t, e.Downloads, "today3.pdf", "today3.pdf")
	absent(t, e.Downloads, "today2.pdf")
	if err := e.Undo(thisMorning, true, io.Discard); err != nil {
		t.Fatal(err)
	}
	present(t, e.Downloads, "today1.pdf", "today1.pdf")
	present(t, e.Downloads, "today2.pdf", "today2.pdf")
	present(t, e.Downloads, "Documents/yesterday.pdf", "yesterday.pdf")
}

func TestSkipsSymlinkFoldersAndChangedUndoFiles(t *testing.T) {
	e, r := fixture(t)
	external := t.TempDir()
	add(t, e.Downloads, "a.pdf", "original", testNow.Add(-time.Hour))
	folder := filepath.Join(e.Downloads, "Documents")
	if err := os.Symlink(external, folder); err != nil {
		t.Fatal(err)
	}
	if err := e.Sort(r, testNow, false, io.Discard); err == nil {
		t.Fatal("expected symlink folder to be rejected")
	}
	absent(t, external, "a.pdf")
	if err := os.Remove(folder); err != nil {
		t.Fatal(err)
	}
	if err := e.Sort(r, testNow, false, io.Discard); err != nil {
		t.Fatal(err)
	}
	add(t, e.Downloads, "Documents/a.pdf", "edited later", testNow)
	if err := e.Undo(testNow, false, io.Discard); err == nil {
		t.Fatal("expected refusal to undo a changed file")
	}
	present(t, e.Downloads, "Documents/a.pdf", "edited later")
	absent(t, e.Downloads, "a.pdf")
}

func TestRecoverInterruptedMoveAndRestore(t *testing.T) {
	e, r := fixture(t)
	add(t, e.Downloads, "before.pdf", "payload", testNow.Add(-time.Hour))
	dir, journal, err := e.open()
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	if err := os.MkdirAll(journal, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(e.Downloads, "Documents"), 0755); err != nil {
		t.Fatal(err)
	}
	root := int(dir.Fd())
	st, err := lookup(root, "before.pdf")
	if err != nil {
		t.Fatal(err)
	}
	m := move{Run: "test", Started: testNow, From: "before.pdf", To: "Documents/before.pdf",
		File: identity(st), State: moving, disk: filepath.Join(journal, "record.json")}
	if err := m.save(); err != nil {
		t.Fatal(err)
	}
	if err := renameChecked(root, m.From, m.To, m.File, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := e.Sort(r, testNow, false, io.Discard); err != nil {
		t.Fatal(err)
	}
	log, err := readJournal(journal)
	if err != nil || len(log) != 1 || log[0].State != moved {
		t.Fatalf("expected recovered move, got %v (%v)", log, err)
	}
	m = log[0]
	m.State, m.RestoredTo = restoring, m.From
	if err := m.save(); err != nil {
		t.Fatal(err)
	}
	if err := renameChecked(root, m.To, m.RestoredTo, m.File, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := e.Sort(r, testNow, false, io.Discard); err != nil {
		t.Fatal(err)
	}
	present(t, e.Downloads, "before.pdf", "payload")
	log, err = readJournal(journal)
	if err != nil || len(log) != 1 || log[0].State != restored {
		t.Fatalf("expected recovered restore, got %v (%v)", log, err)
	}
}

func TestExclusiveFilesystemRename(t *testing.T) {
	e, _ := fixture(t)
	add(t, e.Downloads, "a", "source", testNow)
	add(t, e.Downloads, "b", "destination", testNow)
	f, err := openDownloads(e.Downloads)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fd := int(f.Fd())
	if err := renameExclusive(fd, "a", fd, "b"); !os.IsExist(err) {
		t.Fatalf("expected %v, got %v", fs.ErrExist, err)
	}
	present(t, e.Downloads, "a", "source")
	present(t, e.Downloads, "b", "destination")
}
