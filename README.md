# Logger Package

A simple logging package for Go inspired by the NATS logger library. This package supports logging to **syslog**, **standard output (stderr)**, and **log files**, with different log levels and options for customizing log output format.

## Features

- **Log Levels**: Supports logging at `INFO`, `DEBUG`, `TRACE`, `WARN`, `ERROR`, and `FATAL` levels.
- **Output**: Logs can be directed to `syslog`, `stderr` (standard output), or a specified log file.
- **Log Rotation**: The file logger supports log rotation, where logs are backed up and new logs are created once a file exceeds a size limit.
- **Customizable Format**: Supports plain text or colored log labels. 
- **Timestamp**: Log entries can include timestamps (with optional UTC time formatting).
- **PID Prefix**: Option to include the process ID in the log prefix for better traceability.

## Installation

To install the logger package, run:

```bash
go get github.com/ninepeach/go-logger

```

## QuickStart

To install the logger package, run:

```go
package main
import (
    "github.com/ninepeach/go-logger"
    "os"
)
func main() {
    // Create a standard logger that outputs to stderr with timestamp, debug, and trace enabled
    
    l := logger.NewStdLogger(true, true, true, false, true)
    
    // Log messages with various levels
    l.Noticef("This is an info-level message")
    l.Warnf("This is a warning-level message")
    l.Errorf("This is an error-level message")

      
    
    // These lines won't run due to Fatalf above, but are shown for demonstration
    l.Debugf("This is a debug-level message")
    l.Tracef("This is a trace-level message")
    
    l = logger.NewStdLogger(true, true, true, false, true, logger.LogUTC(true))
    l.Noticef("This is an LogUTC message")

    // Creating a file logger (logs will be written to "app.log")
    l = logger.NewFileLogger("app.log", true, true, true, true)

    l.SetSizeLimit(1 * 1024)

    // Simulate logging with file rotation
    for i := 0; i < 20; i++ {
        l.Noticef("Log message number %d", i)
    }

}

```

