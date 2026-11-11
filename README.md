# go-logger v2

A synchronous, structured logging library for Go, supporting JSON Lines, console output, rotating files, Syslog, and the standard `log/slog` interface.

The library writes to any `io.Writer`, provides typed fields and structured context, and supports concurrent logging through shared logger instances.

## Installation

**Requirements:** Go 1.25.1 or later.

Install the latest stable release:

```sh
go get github.com/ninepeach/go-logger/v2@v2.0.1
```

Import the package:

```go
import "github.com/ninepeach/go-logger/v2"
```

The library uses the Go standard library and `golang.org/x/sys` for platform-specific functionality.

**Stable release:** [v2.0.1](https://github.com/ninepeach/go-logger/releases/tag/v2.0.1)

The `main` branch is used for ongoing development. Production applications should depend on a tagged release rather than an unversioned development commit.

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

    service, err := l.With().
        Str("service", "gateway").
        Logger()
    if err != nil {
        log.Fatal(err)
    }

    service.Info().
        Int("port", 8080).
        Msg("listening")

    service.Info().
        Bool("ready", true).
        Send()
}
```

`New(nil)` writes JSON Lines to stderr at `InfoLevel` and above, with UTC timestamps.

Example output:

```json
{"time":"2026-11-11T10:00:00Z","level":"info","message":"listening","service":"gateway","port":8080}
```

`Msg` includes a message, even when the string is empty. `Send` omits the message field.

Event builders are single-use. Use `With().…Logger()` to derive a logger with shared fields, and `WithLevel(level)` to derive one with a different threshold. Derived loggers share the original output.

### Log levels

Supported levels:

- `TraceLevel`
- `DebugLevel`
- `InfoLevel`
- `WarnLevel`
- `ErrorLevel`
- `FatalLevel`
- `DisabledLevel`

`DisabledLevel` disables logging.

`Fatal()` records a fatal event and returns control to the caller. It does not terminate the process.

## Console output

Use `ConsoleFormat` for human-readable output.

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

l.Debug().
    Str("peer", "edge-1").
    Msg("connection opened")
```

Color applies only to the console level token.

Use `WithUTC(false)` to preserve the timestamp's location when timestamps are enabled.

## Rotating files

`RotatingWriter` supports file rotation by size and calendar date, with backup retention controls.

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
    defer func() {
        err = errors.Join(err, w.Close())
    }()

    l, err := logger.New(w)
    if err != nil {
        return err
    }

    return l.Info().
        Str("service", "gateway").
        MsgErr("started")
}
```

The example requires the standard library's `errors` package.

### Rotation behavior

- `MaxSize` specifies the size limit in bytes.
- `Daily` enables rotation at calendar date changes.
- `MaxBackups` limits the number of retained backups.
- `MaxAgeDays` limits backup age in calendar days.
- `UTC` selects UTC calendar dates for rotation.
- Zero values for size, backup count, or age disable the corresponding limit.

The parent directory must already exist. One writer must exclusively own the active file.

Backup filenames follow this pattern:

```text
app.log.2026-11-11.0001
```

The date is derived from the archived file. Retention runs after rotation.

### Rotation failures

If rotation fails but the writer can still append to the active file, the writer reports the maintenance failure separately and continues writing.

Use:

```go
err := w.TakeMaintenanceError()
```

to retrieve and clear the latest maintenance error.

Alternatively, configure `FileConfig.OnMaintenanceError` to receive notifications.

The active file is opened with platform-specific safeguards against symbolic-link or reparse-point redirection. Applications should still use appropriately protected log directories.

## Syslog

`SyslogWriter` supports synchronous Syslog output using local Unix sockets or TCP.

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
    defer func() {
        err = errors.Join(err, w.Close())
    }()

    l, err := logger.New(w)
    if err != nil {
        return err
    }

    return l.Warn().
        Str("peer", "edge-1").
        MsgErr("connection reset")
}
```

The example requires the standard library's `errors` package and a Syslog listener at the configured address.

### Supported transports

| Transport | Configuration | Platform |
| --- | --- | --- |
| Local Unix socket | `SyslogLocal` | Linux, macOS |
| TCP | `SyslogTCP` | Linux, macOS, Windows |

Remote TCP Syslog uses RFC 5424 messages with RFC 6587 octet-counting framing.

For local Syslog, specify `SyslogLocal` and optionally provide a Unix socket filesystem path in `Address`. An empty local address tries standard system socket paths.

### Security

**TCP Syslog is plaintext and does not provide TLS encryption.**

For communication across untrusted networks, use a secure tunnel or a trusted TLS-capable Syslog relay.

### Resource lifecycle

Close each `RotatingWriter` or `SyslogWriter` after all loggers using it have finished.

A successful submission means the destination writer accepted the record. Durability and remote receipt depend on the destination.

## Standard library slog

The library provides a `log/slog` handler adapter.

```go
l, err := logger.New(nil)
if err != nil {
    return err
}

sl := slog.New(logger.NewSlogHandler(l)).
    With("service", "gateway")

sl.WithGroup("http").
    Info("request complete", "status", 200)
```

Import `log/slog` for this example.

The adapter uses the logger's level filtering, encoding, and output mechanisms.

JSON output preserves groups as nested objects.

### Nested groups

Nested `slog.Group` values are limited to a maximum processing depth of **64 levels**.

Groups exceeding this limit are not recursively expanded. The handler reports an error wrapping `ErrFieldEncoding`.

`WithAttrs` preserves a snapshot of nested group attributes within the supported depth limit.

The depth limit applies to nested `slog.Group` processing. It does not impose a separate limit on chains of `WithGroup` calls.

## Submission errors and fields

### Error handling

`Msg` and `Send` report submission errors through the configured `WithErrorHandler` callback.

The callback may run concurrently. The default handler writes rate-limited diagnostics to stderr.

For explicit error handling, use:

```go
err := l.Info().
    Str("operation", "startup").
    MsgErr("service started")
```

`MsgErr` and `SendErr` return submission errors directly.

If:

```go
errors.Is(err, logger.ErrFieldEncoding)
```

is true, at least one field could not be encoded successfully. Depending on the field type and failure, an invalid field may be represented as `null` or an over-depth group may be omitted or truncated.

The record may already have been written when an error is returned. Inspect the error before retrying to avoid duplicate records.

### Field behavior

- Duplicate fields use the last value.
- Root user fields named `time`, `level`, `message`, or `fields` are placed inside the root `fields` object.
- Structured fields are encoded as JSON values.
- Invalid field values are reported through the error-handling mechanism.

### Concurrency

A logger and its derived loggers may be used concurrently.

Independent `New` calls sharing an ordinary writer require a concurrency-safe writer.

Each event builder and shared-field builder belongs to a single goroutine.

## Migrating from v1

Version 2 introduces a new module path and API.

| v1 | v2 |
| --- | --- |
| `github.com/ninepeach/go-logger` | `github.com/ninepeach/go-logger/v2` |
| `NewStdLogger` | `New(nil, options...)` |
| `Noticef`, `Warnf`, `Errorf` | `Info().Msg(...)`, `Warn().Msg(...)`, `Error().Msg(...)` |
| `NewFileLogger`, `SetSizeLimit` | `NewRotatingWriter(FileConfig{...})` and `New(writer)` |
| `unix://path` | `SyslogConfig{Network: logger.SyslogLocal, Address: "path"}` |
| `udp://host:port` | UDP removed; configure a TCP receiver and use `SyslogTCP` |
| `Fatalf` logs and exits | `Fatal().Msg(...)` logs; the application decides whether to exit |

Applications migrating from v1 should review error handling, file rotation configuration, and process termination behavior.

## Development

The `main` branch is the active development branch.

Clone the repository:

```sh
git clone https://github.com/ninepeach/go-logger.git
cd go-logger
```

Run tests:

```sh
make test
```

Run the race detector:

```sh
make race
```

Run static analysis:

```sh
make vet
```

Run the complete release checks:

```sh
make check
```

Run benchmarks:

```sh
make bench
```

For local development from another Go module:

```sh
go mod edit -require=github.com/ninepeach/go-logger/v2@v2.0.1
go mod edit -replace=github.com/ninepeach/go-logger/v2=/path/to/go-logger
```

Remove the local `replace` directive before publishing the consuming application.

## Documentation

See [V2 Design](docs/design.md) for detailed information about field encoding, file rotation, Syslog behavior, and the logging architecture.

## License

See [LICENSE](LICENSE).

