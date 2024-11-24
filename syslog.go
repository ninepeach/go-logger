package logger

import (
    "fmt"
    "log"
    "log/syslog"
    "net/url"
    "os"
    "strings"
)

// SysLogger provides a system logger implementation.
type SysLogger struct {
    writer *syslog.Writer
    debug  bool
    trace  bool
}

// GetSysLoggerTag generates a tag name for syslog based on the executable name.
func GetSysLoggerTag() string {
    procName := os.Args[0]
    if idx := strings.LastIndex(procName, string(os.PathSeparator)); idx != -1 {
        return procName[idx+1:]
    }
    return procName
}

// NewSysLogger creates a new system logger for local or remote use.
func NewSysLogger(addr string, debug, trace bool) (*SysLogger, error) {
    network, destination, err := parseAddress(addr)
    if err != nil {
        return nil, fmt.Errorf("failed to parse syslog address: %v", err)
    }

    var writer *syslog.Writer
    if network == "" { // Local syslog
        writer, err = syslog.New(syslog.LOG_DAEMON|syslog.LOG_NOTICE, GetSysLoggerTag())
    } else { // Remote syslog
        writer, err = syslog.Dial(network, destination, syslog.LOG_DEBUG, GetSysLoggerTag())
    }

    if err != nil {
        return nil, fmt.Errorf("failed to connect to syslog: %v", err)
    }

    return &SysLogger{
        writer: writer,
        debug:  debug,
        trace:  trace,
    }, nil
}

// parseAddress parses the address for remote syslog.
func parseAddress(addr string) (network, destination string, err error) {
    if addr == "" {
        return "", "", nil // Local syslog
    }

    u, err := url.Parse(addr)
    if err != nil {
        return "", "", err
    }

    switch u.Scheme {
    case "udp", "tcp":
        return u.Scheme, u.Host, nil
    case "unix":
        return u.Scheme, u.Path, nil
    default:
        return "", "", fmt.Errorf("invalid network type: %q", u.Scheme)
    }
}

// logf handles generic log formatting and writes to syslog.
func (l *SysLogger) logf(level func(string) error, format string, v ...interface{}) {
    if err := level(fmt.Sprintf(format, v...)); err != nil {
        log.Printf("failed to write to syslog: %v", err)
    }
}

// Noticef logs a notice message.
func (l *SysLogger) Noticef(format string, v ...interface{}) {
    l.logf(l.writer.Notice, format, v...)
}

// Warnf logs a warning message.
func (l *SysLogger) Warnf(format string, v ...interface{}) {
    l.logf(l.writer.Warning, format, v...)
}

// Fatalf logs a fatal error
func (l *SysLogger) Fatalf(format string, v ...any) {
    l.logf(l.writer.Crit, format, v...)
}

// Errorf logs an error message.
func (l *SysLogger) Errorf(format string, v ...interface{}) {
    l.logf(l.writer.Err, format, v...)
}

// Debugf logs a debug message if debug is enabled.
func (l *SysLogger) Debugf(format string, v ...interface{}) {
    if l.debug {
        l.logf(l.writer.Debug, format, v...)
    }
}

// Tracef logs a trace message if trace is enabled.
func (l *SysLogger) Tracef(format string, v ...interface{}) {
    if l.trace {
        l.logf(l.writer.Notice, format, v...)
    }
}

// Close closes the syslog writer.
func (l *SysLogger) Close() error {
    if l.writer != nil {
        return l.writer.Close()
    }
    return nil
}
