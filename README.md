# go-logger v2

A synchronous, structured Go logger with JSON Lines and console output. It can write to any `io.Writer`, including stderr, rotating files, and Syslog. The package uses only the Go standard library and includes an adapter for `log/slog`.

## Installation

The V2 module requires Go 1.25.1 or later. Once a V2 release is published, install it with:

```sh
go get github.com/ninepeach/go-logger/v2
```

To try a local V2 checkout from an application module, point the dependency at that checkout:

```sh
go mod edit -require=github.com/ninepeach/go-logger/v2@v2.0.0
go mod edit -replace=github.com/ninepeach/go-logger/v2=/path/to/go-logger
```

## Quick start

```go
package main

import (
	"log"

	"github.com/ninepeach/go-logger/v2"
)

func main() {
	l, err := logger.New(nil)
	if err != nil {
		log.Fatal(err)
	}

	service, err := l.With().Str("service", "gateway").Logger()
	if err != nil {
		log.Fatal(err)
	}

	service.Info().Int("port", 8080).Msg("listening")
	service.Info().Bool("ready", true).Send()
}
```

`New(nil)` writes JSON Lines to stderr at `InfoLevel` and above, with UTC timestamps. The first event produces a line like this:

```json
{"time":"2026-11-11T10:00:00Z","level":"info","message":"listening","service":"gateway","port":8080}
```

`Msg` includes a message, even if the string is empty. `Send` omits the message field. Event builders are single-use. Use `With().…Logger()` to derive a logger with shared fields and `l.WithLevel(level)` to derive one with a different threshold. Both derived loggers share the original output.

Available levels are `TraceLevel`, `DebugLevel`, `InfoLevel`, `WarnLevel`, `ErrorLevel`, and `FatalLevel`. Use `DisabledLevel` to turn off logging. `Fatal()` records a fatal event and returns control to the caller.

## Console output

```go
l, err := logger.New(nil,
	logger.WithFormat(logger.ConsoleFormat),
	logger.WithColor(true),
	logger.WithLevel(logger.DebugLevel),
	logger.WithTimestamp(false),
)
if err != nil {
	return err
}

l.Debug().Str("peer", "edge-1").Msg("connection opened")
```

Color applies only to the console level token. Use `WithUTC(false)` to preserve the timestamp's location when timestamps are enabled.

## Rotating files

```go
func writeToFile() (err error) {
	w, err := logger.NewRotatingWriter(logger.FileConfig{
		Filename:   "app.log",
		MaxSize:    10 * 1024 * 1024,
		Daily:      true,
		MaxBackups: 7,
		MaxAgeDays: 14,
		UTC:        true,
	})
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, w.Close()) }()

	l, err := logger.New(w)
	if err != nil {
		return err
	}
	return l.Info().Str("service", "gateway").MsgErr("started")
}
```

The example uses the standard library's `errors` package. The parent directory must exist, and one writer must own the active file. `MaxSize` is measured in bytes. A zero size, backup count, or age disables that limit. Backups use names such as `app.log.2026-11-11.0001`, where the date comes from the archived file. Retention runs after rotation.

When rotation fails but the writer can still append to the active file, it reports the rotation failure separately and continues writing. Use `TakeMaintenanceError()` to retrieve and clear the latest maintenance error, or set `FileConfig.OnMaintenanceError` for notifications.

## Syslog

```go
func writeToSyslog() (err error) {
	w, err := logger.NewSyslogWriter(logger.SyslogConfig{
		Network:  logger.SyslogTCP,
		Address:  "127.0.0.1:514",
		Tag:      "gateway",
		Facility: 16, // local0
	})
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, w.Close()) }()

	l, err := logger.New(w)
	if err != nil {
		return err
	}
	return l.Warn().Str("peer", "edge-1").MsgErr("connection reset")
}
```

The example uses `errors` from the standard library and requires a Syslog listener at the configured address. Remote Syslog uses plaintext TCP with RFC 5424 messages and RFC 6587 octet-counting. For local Syslog on Linux or macOS, use `SyslogLocal` with an optional Unix socket filesystem path in `Address`. An empty local address tries the standard system socket paths. Windows supports TCP Syslog.

The caller closes each `RotatingWriter` or `SyslogWriter` after all loggers using it have finished. A successful submission means the writer accepted the record. Durability and remote receipt depend on the destination.

## Standard library slog

```go
l, err := logger.New(nil)
if err != nil {
	return err
}

sl := slog.New(logger.NewSlogHandler(l)).With("service", "gateway")
sl.WithGroup("http").Info("request complete", "status", 200)
```

Import `log/slog` for this example. The adapter uses the logger's level filter, encoder, and output. JSON preserves groups as nested objects.

## Submission errors and fields

- `Msg` and `Send` report a submission error once through the configured `WithErrorHandler` callback, which may run concurrently. The default handler writes rate-limited diagnostics to stderr.
- `MsgErr` and `SendErr` return submission errors directly. If `errors.Is(err, logger.ErrFieldEncoding)` is true, at least one field was replaced with `null`. The record may already have been written, so inspect the error before retrying.
- Duplicate fields use the last value. Root user fields named `time`, `level`, `message`, or `fields` are placed inside the root `fields` object.
- A logger and its derived loggers can be used concurrently. Independent `New` calls that share an ordinary writer need a concurrency-safe writer. Each event and shared-field builder belongs to a single goroutine.

## Migrating from v1

| V1 usage | V2 equivalent |
| --- | --- |
| Import `github.com/ninepeach/go-logger` | Import `github.com/ninepeach/go-logger/v2` |
| `NewStdLogger` | `New(nil, options...)` |
| `Noticef`, `Warnf`, `Errorf` | `Info().Msg(...)`, `Warn().Msg(...)`, `Error().Msg(...)` with typed fields where appropriate |
| `NewFileLogger` and `SetSizeLimit` | `NewRotatingWriter(FileConfig{...})` and `New(writer)` |
| `unix://path` | `SyslogConfig{Network: logger.SyslogLocal, Address: "path"}` |
| `udp://host:port` | IP UDP support was removed. Configure a TCP receiver and use `SyslogTCP`. |
| `Fatalf` logs and exits | `Fatal().Msg(...)` logs. The application decides whether to exit. |

See the [V2 design](docs/design.md) for detailed field, rotation, and Syslog behavior.
