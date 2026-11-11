//go:build linux || darwin

package logger

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestLocalSyslogFraming(t *testing.T) {
	for _, network := range []string{"unixgram", "unix"} {
		t.Run(network, func(t *testing.T) {
			// Darwin Unix socket paths are limited to 103 bytes.
			dir, err := os.MkdirTemp("/tmp", "gl-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			path := filepath.Join(dir, "sock")
			addr := &net.UnixAddr{Name: path, Net: network}
			var read func() string
			if network == "unixgram" {
				conn, err := net.ListenUnixgram(network, addr)
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
					t.Fatal(err)
				}
				read = func() string {
					var b [1024]byte
					n, err := conn.Read(b[:])
					if err != nil {
						t.Fatal(err)
					}
					return string(b[:n])
				}
			} else {
				listener, err := net.ListenUnix(network, addr)
				if err != nil {
					t.Fatal(err)
				}
				defer listener.Close()
				if err := listener.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
					t.Fatal(err)
				}
				read = func() string {
					conn, err := listener.AcceptUnix()
					if err != nil {
						t.Fatal(err)
					}
					defer conn.Close()
					if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
						t.Fatal(err)
					}
					s, err := bufio.NewReader(conn).ReadString('\n')
					if err != nil {
						t.Fatal(err)
					}
					return s
				}
			}
			w, err := NewSyslogWriter(SyslogConfig{Network: SyslogLocal, Address: path, Tag: "app", Facility: 1})
			if err != nil {
				t.Fatal(err)
			}
			defer w.Close()
			if w.cfg.WriteTimeout != time.Second || w.conn.network != network {
				t.Fatal(w.cfg, w.conn.network)
			}
			now := time.Date(2026, 11, 11, 9, 8, 7, 0, time.UTC)
			w.now = func() time.Time { return now }
			writeRecord(t, w, "a\r\nb\n")
			want := fmt.Sprintf("<14>%s app[%d]: a\\r\\nb", now.Local().Format(time.Stamp), os.Getpid())
			if network == "unix" {
				want += "\n"
			}
			if got := read(); got != want {
				t.Fatalf("got %q\nwant %q", got, want)
			}
		})
	}
}

func TestDatagramFailures(t *testing.T) {
	now := time.Now()
	conn := &fakeConn{steps: []func([]byte) (int, error){
		transportError(syscall.EMSGSIZE), transportError(syscall.ENOBUFS), transportError(os.ErrDeadlineExceeded), transportError(syscall.ECONNREFUSED),
	}}
	next := &fakeConn{steps: []func([]byte) (int, error){transportError(syscall.EMSGSIZE)}}
	attempts := 0
	w := &SyslogWriter{cfg: SyslogConfig{Tag: "app", WriteTimeout: time.Second}, conn: syslogConn{conn, "unixgram"}, mono: func() time.Time { return now }, now: time.Now}
	w.dial = func() (syslogConn, error) { attempts++; return syslogConn{next, "unixgram"}, nil }
	for _, cause := range []error{syscall.EMSGSIZE, syscall.ENOBUFS} {
		if n, err := w.Write([]byte("large")); n != 0 || !errors.Is(err, cause) || errors.Is(err, ErrSyslogUnavailable) || conn.closed {
			t.Fatal(n, err)
		}
	}
	if _, err := w.Write([]byte("timeout")); !errors.Is(err, os.ErrDeadlineExceeded) || !errors.Is(err, ErrSyslogUnavailable) || conn.closed {
		t.Fatal(err)
	}
	now = now.Add(59 * time.Second)
	if _, err := w.Write([]byte("suppressed")); err != ErrSyslogUnavailable || len(conn.writes) != 3 || attempts != 0 {
		t.Fatal(err, len(conn.writes), attempts)
	}
	now = now.Add(time.Second)
	if _, err := w.Write([]byte("retry retained")); !errors.Is(err, syscall.ECONNREFUSED) || !conn.closed || attempts != 0 {
		t.Fatal(err, attempts)
	}
	if _, err := w.Write([]byte("reconnect large")); !errors.Is(err, syscall.EMSGSIZE) || errors.Is(err, ErrSyslogUnavailable) || next.closed || attempts != 1 {
		t.Fatal(err, attempts)
	}
	writeRecord(t, w, "current")
	if len(next.writes) != 2 || !strings.HasSuffix(next.writes[1], " current") {
		t.Fatal(next.writes)
	}
}
