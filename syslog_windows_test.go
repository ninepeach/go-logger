//go:build windows

package logger

import (
	"errors"
	"testing"
)

func TestLocalSyslogUnsupported(t *testing.T) {
	if _, err := NewSyslogWriter(SyslogConfig{Network: SyslogLocal, Tag: "app"}); !errors.Is(err, ErrSyslogLocalUnsupported) {
		t.Fatal(err)
	}
}
