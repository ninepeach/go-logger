package logger

import (
	"fmt"
	"log"
	"os"
	"sync"
)

// Logger represents the server logger
type Logger struct {
	sync.Mutex
	logger     *log.Logger
	debug      bool
	trace      bool
	infoLabel  string
	warnLabel  string
	errorLabel string
	fatalLabel string
	debugLabel string
	traceLabel string
	fl         *FileLogger
}

type LogOption interface {
	isLoggerOption()
}

// LogUTC controls whether timestamps in the log output should be UTC or local time.
type LogUTC bool

func (l LogUTC) isLoggerOption() {}

// logFlags returns the log flags based on the provided options.
func logFlags(time bool, opts ...LogOption) int {
	flags := 0
	if time {
		flags = log.LstdFlags | log.Lmicroseconds
	}

	for _, opt := range opts {
		if v, ok := opt.(LogUTC); ok && time && bool(v) {
			flags |= log.LUTC
		}
	}

	return flags
}

// NewStdLogger creates a standard logger that outputs to Stderr.
func NewStdLogger(time, debug, trace, colors, pid bool, opts ...LogOption) *Logger {
	flags := logFlags(time, opts...)
	prefix := ""
	if pid {
		prefix = pidPrefix()
	}

	l := &Logger{
		logger: log.New(os.Stderr, prefix, flags),
		debug:  debug,
		trace:  trace,
	}

	if colors {
		setColoredLabelFormats(l)
	} else {
		setPlainLabelFormats(l)
	}

	return l
}

// NewFileLogger creates a file logger with output directed to the specified file.
func NewFileLogger(filename string, time, debug, trace, pid bool, opts ...LogOption) *Logger {
	flags := logFlags(time, opts...)
	prefix := ""
	if pid {
		prefix = pidPrefix()
	}

	fl, err := newFileLogger(filename, prefix, time)
	if err != nil {
		log.Fatalf("error opening file: %v", err)
		return nil
	}

	l := &Logger{
		logger: log.New(fl, prefix, flags),
		debug:  debug,
		trace:  trace,
		fl:     fl,
	}
	fl.Lock()
	fl.logger = l
	fl.Unlock()

	setPlainLabelFormats(l)
	return l
}

// SetSizeLimit sets the size of a logfile after which a backup
// is created with the file name + "year.month.day.hour.min.sec.nanosec"
// and the current log is truncated.
func (l *Logger) SetSizeLimit(limit int64) error {
    l.Lock()
    if l.fl == nil {
        l.Unlock()
        return fmt.Errorf("can set log size limit only for file logger")
    }
    fl := l.fl
    l.Unlock()
    fl.setLimit(limit)
    return nil
}

// SetMaxNumFiles sets the number of archived log files that will be retained
func (l *Logger) SetMaxNumFiles(max int) error {
    l.Lock()
    if l.fl == nil {
        l.Unlock()
        return fmt.Errorf("can set log max number of files only for file logger")
    }
    fl := l.fl
    l.Unlock()
    fl.setMaxNumFiles(max)
    return nil
}

// Close implements the io.Closer interface to clean up
// resources in the server's logger implementation.
// Caller must ensure threadsafety.
func (l *Logger) Close() error {
    if l.fl != nil {
        return l.fl.close()
    }
    return nil
}

// Generate the pid prefix string
func pidPrefix() string {
	return fmt.Sprintf("[%d] ", os.Getpid())
}

func setPlainLabelFormats(l *Logger) {
	l.infoLabel = "[INF] "
	l.debugLabel = "[DBG] "
	l.warnLabel = "[WRN] "
	l.errorLabel = "[ERR] "
	l.fatalLabel = "[FTL] "
	l.traceLabel = "[TRC] "
}

func setColoredLabelFormats(l *Logger) {
	colorFormat := "[\x1b[%sm%s\x1b[0m] "
	l.infoLabel = fmt.Sprintf(colorFormat, "32", "INF")
	l.debugLabel = fmt.Sprintf(colorFormat, "36", "DBG")
	l.warnLabel = fmt.Sprintf(colorFormat, "0;93", "WRN")
	l.errorLabel = fmt.Sprintf(colorFormat, "31", "ERR")
	l.fatalLabel = fmt.Sprintf(colorFormat, "31", "FTL")
	l.traceLabel = fmt.Sprintf(colorFormat, "33", "TRC")
}

// Noticef logs a notice statement
func (l *Logger) Noticef(format string, v ...any) {
	l.logger.Printf(l.infoLabel+format, v...)
}

// Warnf logs a notice statement
func (l *Logger) Warnf(format string, v ...any) {
	l.logger.Printf(l.warnLabel+format, v...)
}

// Errorf logs an error statement
func (l *Logger) Errorf(format string, v ...any) {
	l.logger.Printf(l.errorLabel+format, v...)
}

// Fatalf logs a fatal error
func (l *Logger) Fatalf(format string, v ...any) {
	l.logger.Fatalf(l.fatalLabel+format, v...)
}

// Debugf logs a debug statement
func (l *Logger) Debugf(format string, v ...any) {
	if l.debug {
		l.logger.Printf(l.debugLabel+format, v...)
	}
}

// Tracef logs a trace statement
func (l *Logger) Tracef(format string, v ...any) {
	if l.trace {
		l.logger.Printf(l.traceLabel+format, v...)
	}
}
