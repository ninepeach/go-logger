package logger

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// FileConfig configures one rotating file. The caller must exclusively own the active path.
type FileConfig struct {
	Filename           string           // Filename is the active file path.
	MaxSize            int64            // MaxSize is the size limit in bytes, or zero for unlimited.
	Daily              bool             // Daily enables rotation at calendar date changes.
	MaxBackups         int              // MaxBackups limits the number of backups, or zero for unlimited.
	MaxAgeDays         int              // MaxAgeDays limits backup age in calendar days, or zero for unlimited.
	UTC                bool             // UTC uses UTC calendar days.
	TimeFunc           func() time.Time // TimeFunc supplies calendar time and must return a nonzero value.
	OnMaintenanceError func(error)      // OnMaintenanceError may run concurrently and must not call this writer.
}

type activeFile interface {
	io.WriteCloser
	Stat() (fs.FileInfo, error)
}

type fileOps struct {
	open    func(string, int, fs.FileMode) (activeFile, error)
	rename  func(string, string) error
	link    func(string, string) error
	remove  func(string) error
	lstat   func(string) (fs.FileInfo, error)
	readDir func(string) ([]fs.DirEntry, error)
}

const retryDelay = 60 * time.Second
const appendFlags = os.O_WRONLY | os.O_APPEND | os.O_CREATE

// RotatingWriter writes complete records to a file and rotates synchronously.
type RotatingWriter struct {
	mu             sync.Mutex
	cfg            FileConfig
	ops            fileOps
	mono           func() time.Time
	file           activeFile
	size           int64
	activeDate     string
	closed         bool
	recovery       error
	retryAt        time.Time
	maintenance    error
	lastDiagnostic time.Time
}

// NewRotatingWriter opens or creates the active file.
func NewRotatingWriter(cfg FileConfig) (*RotatingWriter, error) {
	if cfg.Filename == "" || cfg.MaxSize < 0 || cfg.MaxBackups < 0 || cfg.MaxAgeDays < 0 {
		return nil, fmt.Errorf("%w: rotating file settings", ErrInvalidConfig)
	}
	if cfg.TimeFunc == nil {
		cfg.TimeFunc = time.Now
	}
	path, err := filepath.Abs(cfg.Filename)
	if err != nil {
		return nil, fmt.Errorf("logger: active path: %w", err)
	}
	cfg.Filename = path
	w := &RotatingWriter{cfg: cfg, mono: time.Now, ops: fileOps{
		open: func(path string, flags int, mode fs.FileMode) (activeFile, error) {
			return os.OpenFile(path, flags, mode)
		},
		rename: os.Rename, link: os.Link, remove: os.Remove, lstat: os.Lstat, readDir: os.ReadDir,
	}}
	now, err := w.dateNow()
	if err != nil {
		return nil, err
	}
	if err := w.reopen(now, true); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *RotatingWriter) date(t time.Time) string {
	zone := time.Local
	if w.cfg.UTC {
		zone = time.UTC
	}
	return t.In(zone).Format(time.DateOnly)
}

func (w *RotatingWriter) dateNow() (string, error) {
	now := w.cfg.TimeFunc()
	if now.IsZero() {
		return "", fmt.Errorf("%w: zero file clock", ErrInvalidConfig)
	}
	return w.date(now), nil
}

func (w *RotatingWriter) reopen(today string, inferDate bool) error {
	f, err := w.ops.open(w.cfg.Filename, appendFlags, 0640)
	if err != nil {
		return fmt.Errorf("logger: open active: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("logger: stat active: %w", errors.Join(err, f.Close()))
	}
	w.file, w.size = f, info.Size()
	if inferDate {
		w.activeDate = today
		if archived := w.date(info.ModTime()); w.size > 0 && archived < today {
			w.activeDate = archived
		}
	}
	return nil
}

// TakeMaintenanceError returns and clears the most recent maintenance failure.
func (w *RotatingWriter) TakeMaintenanceError() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	err := w.maintenance
	w.maintenance = nil
	return err
}

// Write appends a record, rotating first when a configured threshold is exceeded.
func (w *RotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	n, err, maintenance := w.write(p)
	report := w.cfg.OnMaintenanceError
	fallback := false
	if maintenance != nil {
		w.maintenance = maintenance
		if report == nil {
			now := w.mono()
			if w.lastDiagnostic.IsZero() || now.Sub(w.lastDiagnostic) >= retryDelay {
				w.lastDiagnostic, fallback = now, true
			}
		}
	}
	w.mu.Unlock()
	if maintenance != nil {
		if report != nil {
			report(maintenance)
		}
		if fallback {
			stderrDiagnostic("file maintenance", maintenance, 0)
		}
	}
	return n, err
}

func (w *RotatingWriter) write(p []byte) (int, error, error) {
	if w.closed {
		return 0, ErrClosed, nil
	}
	if len(p) == 0 {
		return 0, nil, nil
	}
	today, err := w.dateNow()
	if err != nil {
		return 0, err, nil
	}
	if w.file == nil {
		if err := w.reopen(today, true); err != nil {
			return 0, errors.Join(w.recovery, err), nil
		}
		w.recovery = nil
	}
	sizeDue := w.cfg.MaxSize > 0 && int64(len(p)) > w.cfg.MaxSize-w.size
	dailyDue := w.cfg.Daily && today != w.activeDate
	var maintenance error
	rotated := false
	if w.size > 0 && (sizeDue || dailyDue) && !w.mono().Before(w.retryAt) {
		if err := w.rotate(today); err != nil {
			w.retryAt = w.mono().Add(retryDelay)
			if w.file == nil {
				w.recovery = err
				return 0, err, nil
			}
			maintenance = err
		} else {
			rotated = true
		}
	}
	if w.size == 0 {
		w.activeDate = today
	}
	n, err := w.file.Write(p)
	if int64(n) > math.MaxInt64-w.size {
		w.size = math.MaxInt64
	} else {
		w.size += int64(n)
	}
	err = checkedWrite(n, len(p), err)
	if err != nil {
		err = fmt.Errorf("logger: append active %q: %w", w.cfg.Filename, err)
	}
	if rotated {
		maintenance = w.cleanup(today)
	}
	return n, err, maintenance
}

func (w *RotatingWriter) rotate(today string) error {
	backup, err := w.nextBackup()
	if err != nil {
		return fmt.Errorf("logger: allocate backup: %w", err)
	}
	closeErr := w.file.Close()
	w.file = nil
	if closeErr != nil {
		return errors.Join(fmt.Errorf("logger: close for rotation: %w", closeErr), w.reopen(today, false))
	}
	if _, err = w.ops.lstat(backup); err == nil {
		err = fs.ErrExist
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return errors.Join(fmt.Errorf("logger: reserve backup %q: %w", backup, err), w.reopen(today, false))
	}
	if err := w.ops.rename(w.cfg.Filename, backup); err != nil {
		return errors.Join(fmt.Errorf("logger: archive active: %w", err), w.reopen(today, false))
	}
	f, err := w.ops.open(w.cfg.Filename, appendFlags|os.O_EXCL, 0640)
	if err != nil {
		return errors.Join(fmt.Errorf("logger: create new active: %w", err), w.rollback(backup, today))
	}
	created, identityErr := w.ops.lstat(w.cfg.Filename)
	info, err := f.Stat()
	if err != nil || identityErr != nil {
		closeErr := f.Close()
		removeErr := w.removeCreated(created)
		return errors.Join(fmt.Errorf("logger: stat new active: %w", errors.Join(err, identityErr)), closeErr, removeErr, w.rollback(backup, today))
	}
	w.file, w.size, w.activeDate = f, info.Size(), today
	return nil
}

func (w *RotatingWriter) removeCreated(created fs.FileInfo) error {
	current, err := w.ops.lstat(w.cfg.Filename)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("logger: inspect new active: %w", err)
	}
	if created == nil || !os.SameFile(created, current) {
		return fmt.Errorf("logger: active path changed during rotation: %w", fs.ErrExist)
	}
	return w.ops.remove(w.cfg.Filename)
}

func (w *RotatingWriter) rollback(backup, today string) error {
	// A hard link restores the archived file atomically only if the active
	// pathname is absent. The backup stays intact if restoration fails.
	if err := w.ops.link(backup, w.cfg.Filename); err != nil {
		return fmt.Errorf("logger: restore backup: %w", err)
	}
	if err := w.ops.remove(backup); err != nil {
		// Leaving both links would allow future writes to mutate the backup.
		return errors.Join(fmt.Errorf("logger: remove rollback link: %w", err), w.ops.remove(w.cfg.Filename))
	}
	return w.reopen(today, false)
}

// Close releases the active descriptor and prevents further writes.
func (w *RotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	if err != nil {
		return fmt.Errorf("logger: close active: %w", err)
	}
	return nil
}

type backupFile struct {
	name string
	date string
	seq  string
	day  int64
}

func (w *RotatingWriter) backups() ([]backupFile, error) {
	dir, base := filepath.Split(w.cfg.Filename)
	entries, err := w.ops.readDir(dir)
	if err != nil {
		return nil, err
	}
	prefix := base + "."
	var backups []backupFile
	for _, entry := range entries {
		if entry.IsDir() || entry.Type() & ^os.ModeSymlink != 0 {
			continue
		}
		suffix, ok := strings.CutPrefix(entry.Name(), prefix)
		if !ok || len(suffix) < 15 || suffix[10] != '.' {
			continue
		}
		date, seq := suffix[:10], suffix[11:]
		t, err := time.Parse(time.DateOnly, date)
		if err != nil {
			continue
		}
		valid := true
		for _, b := range []byte(seq) {
			if b < '0' || b > '9' {
				valid = false
				break
			}
		}
		if !valid || (len(seq) > 4 && seq[0] == '0') {
			continue
		}
		seq = strings.TrimLeft(seq, "0")
		if seq == "" {
			continue
		}
		backups = append(backups, backupFile{filepath.Join(dir, entry.Name()), date, seq, t.Unix() / 86400})
	}
	return backups, nil
}

func lessSeq(a, b string) bool {
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}

func (w *RotatingWriter) nextBackup() (string, error) {
	backups, err := w.backups()
	if err != nil {
		return "", err
	}
	max := ""
	for _, b := range backups {
		if b.date == w.activeDate && lessSeq(max, b.seq) {
			max = b.seq
		}
	}
	seq := []byte(max)
	carry := true
	for i := len(seq) - 1; i >= 0; i-- {
		if seq[i] < '9' {
			seq[i]++
			carry = false
			break
		}
		seq[i] = '0'
	}
	if carry {
		seq = append([]byte{'1'}, seq...)
	}
	return fmt.Sprintf("%s.%s.%04s", w.cfg.Filename, w.activeDate, seq), nil
}

func (w *RotatingWriter) cleanup(today string) error {
	if w.cfg.MaxAgeDays == 0 && w.cfg.MaxBackups == 0 {
		return nil
	}
	backups, err := w.backups()
	if err != nil {
		return fmt.Errorf("logger: list backups: %w", err)
	}
	sort.Slice(backups, func(i, j int) bool {
		a, b := backups[i], backups[j]
		if a.date != b.date {
			return a.date < b.date
		}
		return lessSeq(a.seq, b.seq)
	})
	date, err := time.Parse(time.DateOnly, today)
	if err != nil {
		return fmt.Errorf("logger: cleanup calendar day: %w", err)
	}
	day := date.Unix() / 86400
	var failures []error
	remove := func(b backupFile) bool {
		err := w.ops.remove(b.name)
		if err != nil {
			failures = append(failures, fmt.Errorf("logger: remove backup: %w", err))
		}
		return err == nil
	}
	remaining := backups[:0]
	for _, b := range backups {
		if w.cfg.MaxAgeDays > 0 && day-b.day >= int64(w.cfg.MaxAgeDays) {
			if !remove(b) {
				remaining = append(remaining, b)
			}
		} else {
			remaining = append(remaining, b)
		}
	}
	count := len(remaining)
	for _, b := range remaining {
		if w.cfg.MaxBackups == 0 || count <= w.cfg.MaxBackups {
			break
		}
		// Age deletion already attempted this backup.
		if w.cfg.MaxAgeDays > 0 && day-b.day >= int64(w.cfg.MaxAgeDays) {
			continue
		}
		if remove(b) {
			count--
		}
	}
	return errors.Join(failures...)
}
