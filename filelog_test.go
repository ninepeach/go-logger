package logger

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func writeRecord(t testing.TB, w io.Writer, s string) {
	t.Helper()
	n, err := w.Write([]byte(s))
	if n != len(s) || err != nil {
		t.Fatalf("write: %d, %v", n, err)
	}
}

func TestRotationBoundariesAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "active")
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	w, err := NewRotatingWriter(FileConfig{Filename: path, MaxSize: 4, Daily: true, UTC: true, TimeFunc: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	writeRecord(t, w, "abcd")
	writeRecord(t, w, "")
	if _, err := os.Stat(path + ".2026-11-11.0001"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	now = now.AddDate(0, 0, 1)
	writeRecord(t, w, "12345")
	if got := readFile(t, path+".2026-11-11.0001"); got != "abcd" {
		t.Fatal(got)
	}
	writeRecord(t, w, "z")
	if got := readFile(t, path+".2026-11-12.0001"); got != "12345" {
		t.Fatal(got)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(nil); err != ErrClosed {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, now, now); err != nil {
		t.Fatal(err)
	}
	cfg := w.cfg
	cfg.MaxSize = 0
	now = now.AddDate(0, 0, 1)
	w, err = NewRotatingWriter(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := w.Close(); err != nil {
			t.Error(err)
		}
	})
	writeRecord(t, w, "next")
	if got := readFile(t, path+".2026-11-12.0002"); got != "z" {
		t.Fatal(got)
	}
}

type failFile struct {
	activeFile
	closeErr, statErr, writeErr error
}

func (f failFile) Stat() (fs.FileInfo, error) {
	if f.statErr != nil {
		return nil, f.statErr
	}
	return f.activeFile.Stat()
}
func (f failFile) Close() error { return errors.Join(f.activeFile.Close(), f.closeErr) }
func (f failFile) Write(p []byte) (int, error) {
	if f.writeErr != nil {
		n, err := f.activeFile.Write(p[:1])
		return n, errors.Join(err, f.writeErr)
	}
	return f.activeFile.Write(p)
}

func TestRotationFailureMatrix(t *testing.T) {
	for _, failure := range []string{"reserve", "collision", "close", "rename", "open", "stat", "rollback", "cleanup", "partial"} {
		t.Run(failure, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "active")
			now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
			mono := time.Now()
			boom := errors.New("injected")
			var notes []error
			w, err := NewRotatingWriter(FileConfig{Filename: path, MaxSize: 1, UTC: true, MaxBackups: 1, TimeFunc: func() time.Time { return now }, OnMaintenanceError: func(err error) { notes = append(notes, err) }})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := w.Close(); err != nil {
					t.Error(err)
				}
			})
			w.mono = func() time.Time { return mono }
			writeRecord(t, w, "a")
			original := w.ops
			backup := path + ".2026-11-11.0001"
			switch failure {
			case "reserve":
				w.ops.readDir = func(string) ([]fs.DirEntry, error) { return nil, boom }
			case "collision":
				w.ops.lstat = func(p string) (fs.FileInfo, error) {
					if p == backup {
						return nil, nil
					}
					return original.lstat(p)
				}
			case "close":
				w.file = failFile{activeFile: w.file, closeErr: boom}
			case "rename":
				w.ops.rename = func(string, string) error { return boom }
			case "open", "rollback", "stat":
				w.ops.open = func(p string, flags int, mode fs.FileMode) (activeFile, error) {
					if flags&os.O_EXCL != 0 && failure != "stat" {
						return nil, boom
					}
					f, err := original.open(p, flags, mode)
					if err == nil && flags&os.O_EXCL != 0 {
						f = failFile{activeFile: f, statErr: boom}
					}
					return f, err
				}
				if failure == "rollback" {
					w.ops.link = func(string, string) error { return boom }
				}
			case "cleanup":
				if err := os.WriteFile(path+".2026-11-10.0001", []byte("old"), 0640); err != nil {
					t.Fatal(err)
				}
				w.ops.remove = func(string) error { return boom }
			case "partial":
				w.ops.open = func(p string, flags int, mode fs.FileMode) (activeFile, error) {
					f, err := original.open(p, flags, mode)
					if err == nil {
						f = failFile{activeFile: f, writeErr: boom}
					}
					return f, err
				}
			}
			n, err := w.Write([]byte("bc"))
			switch failure {
			case "rollback":
				if n != 0 || !errors.Is(err, boom) || w.file != nil || len(notes) != 0 || readFile(t, backup) != "a" {
					t.Fatal(n, err, notes)
				}
				w.ops.open = func(string, int, fs.FileMode) (activeFile, error) { return nil, fs.ErrPermission }
				if _, err := w.Write([]byte("no")); !errors.Is(err, boom) || !errors.Is(err, fs.ErrPermission) {
					t.Fatal(err)
				}
				w.ops = original
				writeRecord(t, w, "d")
				writeRecord(t, w, "e")
				if got := readFile(t, path); got != "de" {
					t.Fatal(got)
				}
			case "cleanup":
				if n != 2 || err != nil || len(notes) != 1 || !errors.Is(w.TakeMaintenanceError(), boom) || w.TakeMaintenanceError() != nil || readFile(t, path) != "bc" {
					t.Fatal(n, err, notes)
				}
			case "partial":
				if n != 1 || !errors.Is(err, boom) || w.size != 1 || readFile(t, path) != "b" || len(notes) != 0 {
					t.Fatal(n, err, notes)
				}
			default:
				if n != 2 || err != nil || len(notes) != 1 || w.TakeMaintenanceError() != notes[0] || readFile(t, path) != "abc" {
					t.Fatal(n, err, notes)
				}
				w.ops = original
				mono = mono.Add(59 * time.Second)
				writeRecord(t, w, "d")
				if got := readFile(t, path); got != "abcd" {
					t.Fatal(got)
				}
				mono = mono.Add(time.Second)
				writeRecord(t, w, "e")
				if got := readFile(t, backup); got != "abcd" {
					t.Fatal(got)
				}
			}
		})
	}
}

func TestRetentionAndSequence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app")
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	for _, suffix := range []string{"2026-11-10.0001", "2026-11-11.9999", "2026-11-11.00999", "2026-11-12.0001", "2026-02-30.0001"} {
		if err := os.WriteFile(path+"."+suffix, nil, 0640); err != nil {
			t.Fatal(err)
		}
	}
	w, err := NewRotatingWriter(FileConfig{Filename: path, MaxSize: 1, MaxAgeDays: 1, MaxBackups: 2, UTC: true, TimeFunc: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	writeRecord(t, w, "x")
	writeRecord(t, w, "y")
	if got := readFile(t, path+".2026-11-11.10000"); got != "x" {
		t.Fatal(got)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	want := "app app.2026-02-30.0001 app.2026-11-11.00999 app.2026-11-11.10000 app.2026-11-12.0001"
	if strings.Join(names, " ") != want {
		t.Fatal(names)
	}
}
