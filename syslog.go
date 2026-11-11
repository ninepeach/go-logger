package logger

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// SyslogNetwork selects a supported Syslog transport.
type SyslogNetwork string

const (
	SyslogLocal SyslogNetwork = "local" // SyslogLocal selects a local Unix socket.
	SyslogTCP   SyslogNetwork = "tcp"   // SyslogTCP selects RFC 6587 octet-counted TCP.
)

// SyslogConfig configures a synchronous Syslog connection.
type SyslogConfig struct {
	Network      SyslogNetwork // Network is the transport.
	Address      string        // Address is a TCP host:port or an optional local socket path.
	Tag          string        // Tag is the application name, defaulting to the executable name.
	Facility     int           // Facility is an RFC 5424 facility from 0 through 23.
	DialTimeout  time.Duration // DialTimeout defaults to one second.
	WriteTimeout time.Duration // WriteTimeout defaults to one second locally and five seconds for TCP.
}

type syslogConn struct {
	net.Conn
	network string
}

// SyslogWriter sends complete messages and reconnects synchronously after transport failures.
type SyslogWriter struct {
	mu            sync.Mutex
	cfg           SyslogConfig
	dial          func() (syslogConn, error)
	mono          func() time.Time
	now           func() time.Time
	conn          syslogConn
	closed        bool
	retryAt       time.Time
	hostname, pid string
}

// NewSyslogWriter opens the configured transport.
func NewSyslogWriter(cfg SyslogConfig) (*SyslogWriter, error) {
	if cfg.Facility < 0 || cfg.Facility > 23 || cfg.DialTimeout < 0 || cfg.WriteTimeout < 0 {
		return nil, fmt.Errorf("%w: syslog facility or timeout", ErrInvalidConfig)
	}
	if cfg.Tag == "" {
		cfg.Tag = filepath.Base(os.Args[0])
	}
	if !headerToken(cfg.Tag, 48) {
		return nil, fmt.Errorf("%w: syslog tag", ErrInvalidConfig)
	}
	if cfg.DialTimeout == 0 {
		cfg.DialTimeout = time.Second
	}
	if cfg.WriteTimeout == 0 {
		cfg.WriteTimeout = time.Second
		if cfg.Network == SyslogTCP {
			cfg.WriteTimeout = 5 * time.Second
		}
	}
	w := &SyslogWriter{cfg: cfg, mono: time.Now, now: time.Now, pid: strconv.Itoa(os.Getpid())}
	hostname, err := os.Hostname()
	if err != nil || !headerToken(hostname, 255) {
		hostname = "-"
	}
	w.hostname = hostname
	switch cfg.Network {
	case SyslogTCP:
		if cfg.Address == "" {
			return nil, fmt.Errorf("%w: TCP address is required", ErrInvalidConfig)
		}
		w.dial = func() (syslogConn, error) {
			c, err := net.DialTimeout("tcp", cfg.Address, cfg.DialTimeout)
			return syslogConn{c, "tcp"}, err
		}
	case SyslogLocal:
		if strings.Contains(cfg.Address, "://") || strings.HasPrefix(cfg.Address, "@") || strings.IndexByte(cfg.Address, 0) >= 0 {
			return nil, fmt.Errorf("%w: local address must be a filesystem path", ErrInvalidConfig)
		}
		w.dial = func() (syslogConn, error) { return dialLocal(cfg.Address, cfg.DialTimeout) }
	default:
		return nil, fmt.Errorf("%w: syslog network", ErrInvalidConfig)
	}
	w.conn, err = w.dial()
	if err != nil {
		if errors.Is(err, ErrSyslogLocalUnsupported) {
			return nil, err
		}
		return nil, fmt.Errorf("logger: initial syslog dial: %w: %w", ErrSyslogUnavailable, err)
	}
	return w, nil
}

func headerToken(s string, max int) bool {
	if len(s) == 0 || len(s) > max {
		return false
	}
	for _, b := range []byte(s) {
		if b < 33 || b > 126 {
			return false
		}
	}
	return true
}

func priority(facility int, r record) int {
	severity := 7
	switch {
	case !r.slog && r.level == int(FatalLevel):
		severity = 2
	case r.level >= int(ErrorLevel):
		severity = 3
	case r.level >= int(WarnLevel):
		severity = 4
	case r.level >= int(InfoLevel):
		severity = 6
	}
	return facility*8 + severity
}

// Write sends an informational message.
func (w *SyslogWriter) Write(p []byte) (int, error) {
	return w.writeRecord(p, record{level: int(InfoLevel)})
}

var syslogEscaper = strings.NewReplacer("\r", `\r`, "\n", `\n`)

func (w *SyslogWriter) frame(p []byte, r record) []byte {
	msg := syslogEscaper.Replace(utf8String(strings.TrimSuffix(string(p), "\n")))
	pri := priority(w.cfg.Facility, r)
	if w.conn.network == "tcp" {
		body := fmt.Sprintf("<%d>1 %s %s %s %s - - %s", pri, r.time.UTC().Format(time.RFC3339), w.hostname, w.cfg.Tag, w.pid, msg)
		return []byte(strconv.Itoa(len(body)) + " " + body)
	}
	body := fmt.Sprintf("<%d>%s %s[%s]: %s", pri, r.time.Local().Format(time.Stamp), w.cfg.Tag, w.pid, msg)
	if w.conn.network == "unix" {
		body += "\n"
	}
	return []byte(body)
}

func (w *SyslogWriter) writeRecord(p []byte, r record) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return 0, ErrClosed
	}
	if len(p) == 0 {
		return 0, nil
	}
	if r.time.IsZero() {
		r.time = w.now()
	}
	if w.mono().Before(w.retryAt) {
		return 0, ErrSyslogUnavailable
	}
	reconnect := w.conn.Conn == nil
	if reconnect {
		c, err := w.dial()
		if err != nil {
			w.retryAt = w.mono().Add(retryDelay)
			return 0, fmt.Errorf("logger: reconnect syslog: %w: %w", ErrSyslogUnavailable, err)
		}
		w.conn = c
	}
	frame := w.frame(p, r)
	deadlineErr := w.conn.SetWriteDeadline(time.Now().Add(w.cfg.WriteTimeout))
	err := deadlineErr
	if err == nil {
		err = sendFrame(w.conn, frame)
	}
	if err == nil {
		w.retryAt = time.Time{}
		return len(p), nil
	}
	if w.conn.network == "unixgram" && deadlineErr == nil {
		switch classifyDatagram(err) {
		case datagramKeep:
			w.retryAt = time.Time{}
			return 0, fmt.Errorf("logger: write syslog datagram: %w", err)
		case datagramSuppress:
			w.retryAt = w.mono().Add(retryDelay)
			return 0, fmt.Errorf("logger: write syslog datagram: %w: %w", ErrSyslogUnavailable, err)
		}
	}
	closeErr := w.conn.Close()
	w.conn = syslogConn{}
	w.retryAt = time.Time{}
	if reconnect {
		w.retryAt = w.mono().Add(retryDelay)
	}
	return 0, fmt.Errorf("logger: write syslog: %w: %w", ErrSyslogUnavailable, errors.Join(err, closeErr))
}

func sendFrame(c syslogConn, p []byte) error {
	for len(p) > 0 {
		n, err := c.Write(p)
		if err != nil {
			return err
		}
		if n <= 0 || n > len(p) || (c.network == "unixgram" && n != len(p)) {
			return io.ErrShortWrite
		}
		p = p[n:]
	}
	return nil
}

// Close releases the current connection and prevents reconnection.
func (w *SyslogWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	if w.conn.Conn == nil {
		return nil
	}
	err := w.conn.Close()
	w.conn = syslogConn{}
	if err != nil {
		return fmt.Errorf("logger: close syslog: %w", err)
	}
	return nil
}

type datagramAction uint8

const (
	datagramKeep datagramAction = iota
	datagramSuppress
	datagramDisconnect
)
