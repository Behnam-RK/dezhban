package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

// Rotation geometry for the persistent log file. 5 MiB × (1 live + 2 archives)
// caps the footprint at ~15 MiB — months of the daemon's line rate — while
// keeping any single file small enough to open casually.
const (
	fileMaxBytes = 5 << 20
	fileBackups  = 2
)

// FileWriter is the daemon's persistent log sink: an append-only file with
// size-based rotation (dezhban.log → dezhban.log.1 → dezhban.log.2, oldest
// dropped). Concurrency-safe. The file is 0644 like state.json — the logs hold
// nothing the status surfaces don't, and the GUI / an unprivileged operator
// must be able to read history without root.
type FileWriter struct {
	mu   sync.Mutex
	f    *os.File
	size int64
	path string
}

// OpenFile opens (creating parents as needed) the persistent log file for
// appending.
func OpenFile(path string) (*FileWriter, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create log dir %q: %w", dir, err)
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open log file %q: %w", path, err)
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	return &FileWriter{f: f, size: st.Size(), path: path}, nil
}

func (w *FileWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.size+int64(len(p)) > fileMaxBytes {
		if err := w.rotateLocked(); err != nil {
			// Rotation failing must not silence logging: keep appending to the
			// oversized file rather than dropping records.
			fmt.Fprintln(os.Stderr, "dezhban: log rotation failed:", err)
		}
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

// Close closes the underlying file. Further Writes fail.
func (w *FileWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.f.Close()
}

// rotateLocked shifts the archive chain and reopens a fresh live file. On every
// error path it leaves w.f pointing at an appendable handle (via reopenLocked)
// so a rotation failure degrades to "keep appending" rather than silencing the
// log — Write closed the file to rename it, and must never be left holding a
// closed descriptor.
func (w *FileWriter) rotateLocked() error {
	if err := w.f.Close(); err != nil {
		w.reopenLocked()
		return err
	}
	os.Remove(fmt.Sprintf("%s.%d", w.path, fileBackups)) // oldest falls off
	for i := fileBackups - 1; i >= 1; i-- {
		os.Rename(fmt.Sprintf("%s.%d", w.path, i), fmt.Sprintf("%s.%d", w.path, i+1))
	}
	if err := os.Rename(w.path, w.path+".1"); err != nil && !os.IsNotExist(err) {
		w.reopenLocked() // couldn't rotate; resume appending to the existing file
		return err
	}
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		w.reopenLocked()
		return err
	}
	w.f = f
	w.size = 0
	return nil
}

// reopenLocked restores w.f to an appendable handle after a failed rotation, so
// logging is never permanently silenced by a transient rename/close error. Best
// effort: if even the reopen fails, w.f is left as-is and Writes surface the
// error, but a single hiccup no longer kills the sink for the process lifetime.
func (w *FileWriter) reopenLocked() {
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	w.f = f
	if st, err := f.Stat(); err == nil {
		w.size = st.Size()
	}
}

// NewTextHandler returns the standard dezhban text handler at the given level,
// writing to w — the shared shape of the stderr and file sinks.
func NewTextHandler(level string, w io.Writer) slog.Handler {
	return slog.NewTextHandler(w, &slog.HandlerOptions{Level: parseLevel(level)})
}
