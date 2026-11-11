package logger

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSyslogTCP(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	w, err := NewSyslogWriter(SyslogConfig{Network: SyslogTCP, Address: listener.Addr().String(), Tag: "app", Facility: 16})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if w.cfg.DialTimeout != time.Second || w.cfg.WriteTimeout != 5*time.Second {
		t.Fatal(w.cfg)
	}
	conn, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	check := func(pri int, stamp string, body string) {
		t.Helper()
		token, err := reader.ReadString(' ')
		if err != nil {
			t.Fatal(err)
		}
		n, err := strconv.Atoi(strings.TrimSuffix(token, " "))
		if err != nil || n < 1 || n > 4096 {
			t.Fatal(token, err)
		}
		payload := make([]byte, n)
		if _, err := io.ReadFull(reader, payload); err != nil {
			t.Fatal(err)
		}
		want := fmt.Sprintf("<%d>1 %s %s app %d - - %s", pri, stamp, w.hostname, os.Getpid(), body)
		if string(payload) != want {
			t.Fatalf("got %q\nwant %q", payload, want)
		}
	}
	now := time.Date(2026, 11, 11, 9, 8, 7, 123, time.UTC)
	w.now = func() time.Time { return now }
	writeRecord(t, w, "a\r\nb\\n\n")
	stamp := now.Format(time.RFC3339)
	check(134, stamp, `a\r\nb\n`)
	l := newLogger(t, w, WithTimestamp(false), WithLevel(TraceLevel), WithTimeFunc(func() time.Time { return now }))
	if err := l.Fatal().MsgErr("stop"); err != nil {
		t.Fatal(err)
	}
	check(130, stamp, `{"level":"fatal","message":"stop"}`)
	rec := slog.NewRecord(now.Add(time.Second), slog.Level(12), "high", 0)
	if err := NewSlogHandler(l).Handle(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	check(131, now.Add(time.Second).Format(time.RFC3339), `{"level":"error+4","message":"high"}`)
	if _, err := NewSyslogWriter(SyslogConfig{Network: "udp", Address: "127.0.0.1:1", Tag: "app"}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(nil); err != ErrClosed {
		t.Fatal(err)
	}
	if _, err := NewSyslogWriter(SyslogConfig{Network: SyslogTCP, Address: "127.0.0.1:invalid", Tag: "app"}); !errors.Is(err, ErrSyslogUnavailable) {
		t.Fatal(err)
	}
}

type fakeConn struct {
	net.Conn
	writes    []string
	steps     []func([]byte) (int, error)
	deadlines []time.Time
	closed    bool
}

func (c *fakeConn) SetWriteDeadline(d time.Time) error {
	c.deadlines = append(c.deadlines, d)
	return nil
}
func (c *fakeConn) Close() error { c.closed = true; return nil }
func (c *fakeConn) Write(p []byte) (int, error) {
	c.writes = append(c.writes, string(p))
	if len(c.steps) == 0 {
		return len(p), nil
	}
	fn := c.steps[0]
	c.steps = c.steps[1:]
	return fn(p)
}

func transportError(err error) func([]byte) (int, error) {
	return func([]byte) (int, error) { return 0, err }
}

func TestSyslogReconnectAndBackoff(t *testing.T) {
	boom := errors.New("broken socket")
	now := time.Now()
	first := &fakeConn{steps: []func([]byte) (int, error){func([]byte) (int, error) { return 5, nil }, transportError(boom)}}
	second := &fakeConn{steps: []func([]byte) (int, error){transportError(boom)}}
	third := &fakeConn{}
	attempts := 0
	w := &SyslogWriter{cfg: SyslogConfig{Tag: "app", WriteTimeout: time.Second}, conn: syslogConn{first, "tcp"}, mono: func() time.Time { return now }, now: time.Now, hostname: "host", pid: "1"}
	w.dial = func() (syslogConn, error) {
		attempts++
		switch attempts {
		case 1:
			return syslogConn{second, "tcp"}, nil
		case 2:
			return syslogConn{}, boom
		default:
			return syslogConn{third, "tcp"}, nil
		}
	}
	if n, err := w.Write([]byte("lost-first")); n != 0 || !errors.Is(err, ErrSyslogUnavailable) || !errors.Is(err, boom) || !first.closed || len(first.deadlines) != 1 || len(first.writes) != 2 {
		t.Fatal(n, err, first)
	}
	if n, err := w.Write([]byte("lost-second")); n != 0 || !errors.Is(err, ErrSyslogUnavailable) || !errors.Is(err, boom) || !second.closed || attempts != 1 {
		t.Fatal(n, err, attempts)
	}
	now = now.Add(59 * time.Second)
	if _, err := w.Write([]byte("backoff")); err != ErrSyslogUnavailable || attempts != 1 {
		t.Fatal(err, attempts)
	}
	now = now.Add(time.Second)
	if _, err := w.Write([]byte("lost-dial")); !errors.Is(err, boom) || attempts != 2 {
		t.Fatal(err, attempts)
	}
	now = now.Add(time.Minute)
	writeRecord(t, w, "current")
	if len(third.writes) != 1 || !strings.HasSuffix(third.writes[0], " current") || attempts != 3 {
		t.Fatal(third.writes, attempts)
	}
	if err := w.Close(); err != nil || !third.closed {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("closed")); err != ErrClosed || attempts != 3 {
		t.Fatal(err, attempts)
	}
}
