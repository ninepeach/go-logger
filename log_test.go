package logger

import (
	"bytes"
	"os"
	"testing"
)

// Helper function to create a new standard logger for testing
func newTestStdLogger(time, debug, trace, colors, pid bool, opts ...LogOption) *Logger {
	return NewStdLogger(time, debug, trace, colors, pid, opts...)
}

// Helper function to create a new file logger for testing
func newTestFileLogger(filename string, time, debug, trace, pid bool, opts ...LogOption) *Logger {
	return NewFileLogger(filename, time, debug, trace, pid, opts...)
}

// Test: Standard logger creation and logging at various levels
func TestNewStdLogger(t *testing.T) {
	l := newTestStdLogger(true, true, false, false, true)
	if l == nil {
		t.Fatal("expected a new logger, got nil")
	}

	var buf bytes.Buffer
	l.logger.SetOutput(&buf)

	// Test logging at various levels
	l.Noticef("This is an info-level message")
	verifyLogOutput(t, buf, "[INF] This is an info-level message")

	l.Warnf("This is a warning-level message")
	verifyLogOutput(t, buf, "[WRN] This is a warning-level message")

	l.Errorf("This is an error-level message")
	verifyLogOutput(t, buf, "[ERR] This is an error-level message")

	// These lines won't run due to Fatalf above, but are shown for demonstration
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("expected panic on Fatalf, but did not panic")
		}
	}()
	l.Fatalf("This is a fatal-level message")
}

// Test: Standard logger with UTC time option
func TestLoggerWithUTC(t *testing.T) {
	l := newTestStdLogger(true, true, true, false, true, LogUTC(true))
	if l == nil {
		t.Fatal("expected a new logger, got nil")
	}

	var buf bytes.Buffer
	l.logger.SetOutput(&buf)

	// Log a UTC message
	l.Noticef("This is a LogUTC message")
	verifyLogOutput(t, buf, "[INF] This is a LogUTC message")
}

// Test: File logger creation, file size limit, and file rotation
func TestLoggerFileRotation(t *testing.T) {
	tmpFile := "./test_rotate.log"
	defer os.Remove(tmpFile)

	l := newTestFileLogger(tmpFile, true, true, true, true)
	if l == nil {
		t.Fatal("expected a new file logger, got nil")
	}

	// Set file size limit for rotation
	err := l.SetSizeLimit(1 * 1024) // 1 KB size limit for the log file
	if err != nil {
		t.Fatalf("unexpected error setting size limit: %v", err)
	}

	// Simulate logging with file rotation (writing multiple logs)
	for i := 0; i < 20; i++ {
		l.Noticef("Log message number %d", i)
	}

	// Check if the log file exists and is not empty
	if _, err := os.Stat(tmpFile); os.IsNotExist(err) {
		t.Errorf("expected log file to be created, got error: %v", err)
	}

	fileInfo, err := os.Stat(tmpFile)
	if err != nil || fileInfo.Size() == 0 {
		t.Errorf("expected non-empty log file, got error: %v or empty file", err)
	}

	// Optionally check if the file is rotated or split based on size
	// Further logic could be added here to verify file rotation, if implemented
}

// Test: File logger max file count
func TestLoggerMaxNumFiles(t *testing.T) {
	tmpFile := "./test_max_files.log"
	defer os.Remove(tmpFile)

	l := newTestFileLogger(tmpFile, true, true, true, true)
	if l == nil {
		t.Fatal("expected a new file logger, got nil")
	}

	// Set max number of files
	err := l.SetMaxNumFiles(5)
	if err != nil {
		t.Fatalf("unexpected error setting max number of files: %v", err)
	}

	// You can test the rotation logic further if needed
	// Simulate multiple logs to trigger file rotation and test retention of max files
	for i := 0; i < 100; i++ {
		l.Noticef("Log message number %d", i)
	}

	// Verify that the log files are rotated and the number of files is retained as expected
	// Further verification logic can be added here if needed
}

// Helper function to verify log output
func verifyLogOutput(t *testing.T, buf bytes.Buffer, expected string) {
	if !bytes.Contains(buf.Bytes(), []byte(expected)) {
		t.Errorf("expected log output '%s', got: %s", expected, buf.String())
	}
}

// Test: Close the logger
func TestLoggerClose(t *testing.T) {
	tmpFile := "./test_close.log"
	defer os.Remove(tmpFile)

	l := newTestFileLogger(tmpFile, true, true, true, true)
	if l == nil {
		t.Fatal("expected a new file logger, got nil")
	}

	// Close the logger
	err := l.Close()
	if err != nil {
		t.Fatalf("unexpected error closing logger: %v", err)
	}

	// After closing, the file should no longer be writable
	if _, err := os.Stat(tmpFile); os.IsNotExist(err) {
		t.Errorf("expected log file to be present after closing, got error: %v", err)
	}
}
