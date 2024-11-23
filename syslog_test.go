package logger

import (
	"testing"
)

func TestGetSysLoggerTag(t *testing.T) {
	expected := "logger.test"
	tag := GetSysLoggerTag()
	if tag != expected {
		t.Errorf("Expected tag: %s, got: %s", expected, tag)
	}
}

func TestNewSysLogger_Local(t *testing.T) {
	logger, err := NewSysLogger("", true, true)
	if err != nil {
		t.Fatalf("Failed to create local syslogger: %v", err)
	}
	defer logger.Close()

	logger.Noticef("This is a notice log")
	logger.Warnf("This is a warning log")
	logger.Errorf("This is an error log")
	logger.Debugf("This is a debug log")
	logger.Tracef("This is a trace log")
}

func TestNewSysLogger_InvalidRemoteAddress(t *testing.T) {
	_, err := NewSysLogger("invalid://address", true, false)
	if err == nil {
		t.Fatal("Expected error for invalid remote address, got nil")
	}
}

func TestNewSysLogger_Remote(t *testing.T) {
	// Use a local syslog server address for testing, modify as needed
	remoteAddr := "udp://127.0.0.1:514"
	logger, err := NewSysLogger(remoteAddr, true, false)
	if err != nil {
		t.Fatalf("Failed to create remote syslogger: %v", err)
	}
	defer logger.Close()

	logger.Noticef("This is a notice log to remote syslog")
	logger.Warnf("This is a warning log to remote syslog")
	logger.Debugf("Debug logs should be visible if enabled")
}

func TestParseAddress(t *testing.T) {
	tests := []struct {
		addr          string
		expectedNet   string
		expectedAddr  string
		expectErr     bool
	}{
		{"udp://127.0.0.1:514", "udp", "127.0.0.1:514", false},
		{"tcp://192.168.1.1:514", "tcp", "192.168.1.1:514", false},
		{"unix:///var/run/syslog", "unix", "/var/run/syslog", false},
		{"invalid://address", "", "", true},
		{"", "", "", false}, // Local syslog case
	}

	for _, test := range tests {
		network, addr, err := parseAddress(test.addr)
		if test.expectErr {
			if err == nil {
				t.Errorf("Expected error for address %q, got nil", test.addr)
			}
		} else {
			if err != nil {
				t.Errorf("Unexpected error for address %q: %v", test.addr, err)
			}
			if network != test.expectedNet || addr != test.expectedAddr {
				t.Errorf("For address %q, expected (%q, %q), got (%q, %q)",
					test.addr, test.expectedNet, test.expectedAddr, network, addr)
			}
		}
	}
}

func TestSysLogger_Close(t *testing.T) {
	logger, err := NewSysLogger("", true, true)
	if err != nil {
		t.Fatalf("Failed to create local syslogger: %v", err)
	}

	if err := logger.Close(); err != nil {
		t.Errorf("Expected no error on Close, got: %v", err)
	}
}
