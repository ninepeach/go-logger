# go-logger V2 detailed design

| Item | Value |
| --- | --- |
| Project | `github.com/ninepeach/go-logger` |
| Target module | `github.com/ninepeach/go-logger/v2` |
| Target release | `v2.0.0` |
| Status | Approved implementation specification |
| Priorities | correctness, reliability, simplicity, performance |

This document is normative. The words **MUST**, **MUST NOT**, **SHOULD**, and
**MAY** have their usual RFC 2119 meanings. Examples are illustrative; explicit
rules take precedence. An implementation decision that is not settled here must
be brought back for design review rather than silently added to the public API.

## 1. Scope

V2 is a synchronous, structured logger with a zerolog-style `Logger`/`Event`
API. It has built-in JSON and console encoders, a `log/slog` adapter, rotating
files, and local or remote Syslog output.

V2 MUST:

- use only the Go standard library at runtime and in tests;
- support Linux, macOS, and Windows;
- be safe when a `Logger`, `slog.Handler`, or writer is shared by goroutines;
- make every enabled failure observable as either a returned error or exactly
  one call to an error handler;
- never call `os.Exit`, `panic`, or `runtime.Goexit` as logging behavior; and
- keep the v1 package working on its existing branch. V2 lives in a `/v2`
  module and is not implemented by mutating v1 APIs in place.

The following are non-goals: asynchronous queues, sampling, hooks, a general
encoder plug-in API, compression, background cleanup, multi-process rotation
coordination, network retry queues, and guaranteed persistence after a
successful `Write`. UDP delivery is inherently not guaranteed.

## 2. Architecture and ownership

```text
native calls                    slog calls
    |                               |
 Logger -> Event                slog.Handler
    |                               |
    +---------- normalized record --+
                       |
              built-in encoder core
                 /             \
              JSON           Console
                       |
               synchronized sink
              /        |         \
       io.Writer  RotatingWriter  SyslogWriter
```

- `Logger` owns immutable configuration and shared fields. It does not own the
  supplied output.
- `Event` owns one in-progress record. It is single-use and is not safe for
  concurrent use.
- The encoder core is internal. V2 deliberately exports `Format`, not an
  `Encoder` interface. Both native and slog paths MUST use this same core.
- `RotatingWriter` owns its file descriptor and must be closed by its creator.
- `SyslogWriter` owns its socket or local Syslog connection and must be closed
  by its creator.
- `Logger` MUST NOT expose `Close`; closing a logger must not close a writer
  shared by another logger.
- All submission and output is synchronous. Success means the complete record
  was accepted by the output API. It does not mean `fsync`, remote receipt, or
  survival of a process or machine crash.

No library-owned background goroutine is permitted.

## 3. Package and public API

The following declarations are the complete planned V2 public surface. Minor
helper constants may be added only when they do not change semantics.

```go
package logger

type Level int8

const (
    TraceLevel    Level = -8
    DebugLevel    Level = -4
    InfoLevel     Level = 0
    WarnLevel     Level = 4
    ErrorLevel    Level = 8
    FatalLevel    Level = 12
    DisabledLevel Level = 127
)

func (l Level) String() string
func ParseLevel(s string) (Level, error)

type Format uint8

const (
    JSONFormat Format = iota
    ConsoleFormat
)

type Option func(*config) error // config remains unexported

func WithLevel(Level) Option
func WithFormat(Format) Option
func WithTimestamp(bool) Option
func WithUTC(bool) Option
func WithColor(bool) Option
func WithErrorHandler(func(error)) Option
func WithTimeFunc(func() time.Time) Option

func New(output io.Writer, options ...Option) (*Logger, error)

type Logger struct { /* unexported */ }

func (l *Logger) Enabled(Level) bool
func (l *Logger) Trace() *Event
func (l *Logger) Debug() *Event
func (l *Logger) Info() *Event
func (l *Logger) Warn() *Event
func (l *Logger) Error() *Event
func (l *Logger) Fatal() *Event
func (l *Logger) With() *Context
func (l *Logger) WithLevel(Level) (*Logger, error)

type Context struct { /* unexported */ }

func (c *Context) Str(string, string) *Context
func (c *Context) Int(string, int) *Context
func (c *Context) Int64(string, int64) *Context
func (c *Context) Uint64(string, uint64) *Context
func (c *Context) Float64(string, float64) *Context
func (c *Context) Bool(string, bool) *Context
func (c *Context) Time(string, time.Time) *Context
func (c *Context) Dur(string, time.Duration) *Context
func (c *Context) Err(error) *Context
func (c *Context) Bytes(string, []byte) *Context
func (c *Context) Any(string, any) *Context
func (c *Context) Logger() (*Logger, error)

type Event struct { /* unexported */ }

func (e *Event) Str(string, string) *Event
func (e *Event) Int(string, int) *Event
func (e *Event) Int64(string, int64) *Event
func (e *Event) Uint64(string, uint64) *Event
func (e *Event) Float64(string, float64) *Event
func (e *Event) Bool(string, bool) *Event
func (e *Event) Time(string, time.Time) *Event
func (e *Event) Dur(string, time.Duration) *Event
func (e *Event) Err(error) *Event
func (e *Event) Bytes(string, []byte) *Event
func (e *Event) Any(string, any) *Event
func (e *Event) Msg(string)
func (e *Event) Send()
func (e *Event) MsgErr(string) error
func (e *Event) SendErr() error

func NewSlogHandler(l *Logger) slog.Handler
```

`New` returns an error because invalid options must be rejected, not silently
normalized. A nil output means `os.Stderr`. A nil `*Logger` passed to
`NewSlogHandler` produces a non-panicking handler whose `Handle` returns
`ErrNilLogger`.

### 3.1 Defaults and validation

| Property | Default |
| --- | --- |
| Minimum level | `InfoLevel` |
| Format | `JSONFormat` |
| Timestamp | enabled |
| Timestamp zone | UTC |
| Color | disabled |
| Error handler | one-line fallback to `os.Stderr` |
| Clock | `time.Now` |

Only the seven declared levels and two declared formats are valid option
values. Invalid values cause `New` or `WithLevel` to return an error. Color is
valid only for console format; enabling it with JSON is a configuration error.
`WithTimeFunc(nil)` and `WithErrorHandler(nil)` are configuration errors.

`WithTimeFunc` exists to make boundary behavior testable. Production code
normally does not set it. The function is called once per enabled event at
submission, unless a slog record provides a non-zero time.

An event is enabled when `eventLevel >= minimumLevel` and the minimum is not
`DisabledLevel`. `FatalLevel` only records a level named `fatal`; it never exits.

### 3.2 Immutable derived loggers

`With()` returns a builder initialized from the logger's shared fields.
`Context.Logger()` returns a new logger. `WithLevel` returns a new logger. They
MUST NOT mutate the source logger. The output synchronization object, options,
and error handler are shared. Field data is copied so caller mutation after
`Logger()` returns cannot affect either logger.

The builder records its first validation/encoding error. Later field calls are
no-ops, and `Logger()` returns that error without producing a logger. Builders,
like Events, are not goroutine-safe.

## 4. Record and field semantics

Every record has system members followed by shared fields followed by event
fields. Within each user-field section, insertion order is preserved.

### 4.1 Reserved names and conflicts

At the top level, `time`, `level`, and `message` are reserved. A user field with
one of these keys is an encoding error even when the corresponding system member
is omitted. The empty key is allowed.

Within the same JSON object, a user key may occur only once across inherited
and event fields. A duplicate is an encoding error. The record is not written.
Keys may repeat in different nested slog groups because they belong to different
objects. These rules also apply to console encoding, so switching formats cannot
change validation behavior.

For native events, a conflict is remembered at the field call and reported at
submission. For child construction, `Context.Logger()` reports it. For slog,
`Handle` reports it. No last-write-wins or first-write-wins behavior is allowed.

### 4.2 Value representation

| Method/type | JSON | Console |
| --- | --- | --- |
| string | JSON string | quoted when required |
| signed/unsigned integer | JSON number | decimal |
| finite float | shortest round-trip JSON number | shortest round-trip number |
| `NaN`, `+Inf`, `-Inf` | encoding error | encoding error |
| bool | JSON boolean | `true` or `false` |
| time | RFC3339Nano string | RFC3339Nano |
| duration | `time.Duration.String()` string | same |
| error | `Error()` string under key `error` | same |
| bytes | RFC 4648 standard padded Base64 string | same |
| nil in `Any` | JSON null | `null` |

Event timestamps and `Time` fields use UTC when `WithUTC(true)` and preserve the
value's location otherwise. Invalid UTF-8 in keys, strings, messages, errors,
and `[]byte` produced through `Any` is replaced with U+FFFD, matching
`encoding/json`; raw `Bytes` is Base64 and therefore unaffected.

`Err(nil)` adds nothing. A non-nil `Err` is exactly equivalent to
`Str("error", err.Error())` for conflict detection.

`Any` MUST use `encoding/json` semantics, including `json.Marshaler` and
`encoding.TextMarshaler`, and MUST reject cycles, unsupported values (such as
functions, channels, and complex values), unsupported map keys, invalid custom
marshaler output, and non-finite floats. `Any` output is embedded as JSON in
JSON format. In console format, scalars use normal console rules and composite
values use compact JSON. The implementation may defer `Any` marshaling until
submission, but MUST snapshot the resulting bytes before calling the writer.
Caller mutation concurrent with submission remains a caller data race.

Primitive values are copied on field calls. `Bytes` copies its input on the
field call. After a field method returns, later caller mutation of that byte
slice cannot affect the record.

## 5. Event lifecycle and errors

An Event has exactly three logical states:

```text
open -- Msg/Send/MsgErr/SendErr --> submitted
open -- disabled creation -------> disabled-open -- submit --> submitted
```

- An enabled Event is created open. Its buffer may come from `sync.Pool`.
- A disabled Event has no encoding buffer and never calls the clock, encoder,
  output, or error handler. Field calls are chainable no-ops.
- The first submission atomically marks the Event submitted before encoding or
  writing. Encoding or write failure does not make it reusable.
- Any field method after submission is a no-op. A second `MsgErr` or `SendErr`
  returns `ErrEventConsumed`; a second `Msg` or `Send` reports
  `ErrEventConsumed` to the error handler. Nothing is written twice.
- An Event MUST NOT be copied or used by multiple goroutines.

To prevent stale pointers from referring to a different record, Event objects
themselves MUST NOT be pooled. Internal buffers and field storage MAY be pooled
and are returned only after the synchronous output call completes. A writer is
permitted by `io.Writer` to use the byte slice only until `Write` returns; a
writer that retains it violates the contract.

`Msg` includes `message` even when it is empty. `Send` omits `message`.
Disabled `MsgErr` and `SendErr` return nil.

`Msg` and `Send` call the configured error handler exactly once on an encoding,
validation, short-write, or output error. `MsgErr` and `SendErr` return the same
error and MUST NOT call the handler. The handler is invoked after all internal
logger/writer locks are released. If the handler panics, that panic belongs to
application code and is not recovered by the logger.

The default handler writes a compact diagnostic directly to `os.Stderr` using a
private mutex and does not call this package recursively. Failure of that
fallback write is ignored because there is no further safe reporting channel.

The package exposes sentinel errors usable with `errors.Is`:

```go
var (
    ErrInvalidConfig = errors.New("logger: invalid configuration")
    ErrReservedKey   = errors.New("logger: reserved field key")
    ErrDuplicateKey  = errors.New("logger: duplicate field key")
    ErrEventConsumed = errors.New("logger: event already submitted")
    ErrNilLogger     = errors.New("logger: nil logger")
    ErrClosed        = errors.New("logger: writer closed")
    ErrSyslogLocalUnsupported = errors.New("logger: local syslog unsupported")
)
```

Specific errors wrap the appropriate sentinel and include the operation and
key/path/address when safe. Errors MUST NOT include record contents or secrets.

## 6. Encoding and output atomicity

### 6.1 JSON

JSON is compact UTF-8 JSON Lines: one object and exactly one trailing `\n` per
record. It contains no ANSI bytes and no insignificant whitespace. Example:

```json
{"time":"2026-11-11T10:00:00Z","level":"info","message":"started","service":"gateway","connections":100}
```

System-member order is `time` (when enabled), `level`, then `message` (for
`Msg`). Shared and event fields follow. Successfully encoded output MUST always
be valid JSON.

### 6.2 Console

Console output is one line with this order:

```text
2026-11-11T10:00:00Z INFO started service=gateway connections=100
```

The timestamp is omitted when disabled. Level names are uppercase. The message
and a string value are emitted unquoted only when non-empty UTF-8 contains bytes
from `[A-Za-z0-9._/@:+-]` exclusively; otherwise they use Go `%q`-compatible
quoted escaping. Keys use the same quoting rule and render as `"key"=value`
when required. Composite `Any` JSON is quoted as a single value if it contains
spaces. Exactly one trailing newline is added.

Color, when enabled, wraps only the level token in ANSI SGR sequences and ends
with reset immediately after the token. Fixed colors are trace=gray,
debug=cyan, info=green, warn=yellow, error=red, fatal=bright red. Color is never
auto-detected from a terminal.

Nested slog groups are rendered with dotted, escaped keys (`http.method=GET`).
A literal dot in a group or attribute name is escaped as `\.` and a literal
backslash as `\\`, making paths unambiguous.

### 6.3 Synchronization and short writes

One shared mutex in the output wrapper surrounds each complete record write for
ordinary `io.Writer` outputs. Derived loggers share that wrapper. Thus bytes
from two records cannot interleave even if the supplied writer is not safe for
concurrent calls.

The wrapper performs one `Write(record)` call. `n < len(record)` is always an
error; if the writer returned nil error, return `io.ErrShortWrite`. It MUST NOT
retry a partial write, because retrying can duplicate an unknown prefix. It
returns the actual count internally for diagnostics, but logger submission only
returns the error.

`RotatingWriter` already serializes complete calls; wrapping it with the logger
mutex is still permitted. Error handlers are never invoked under either mutex.

## 7. RotatingWriter API

```go
type FileConfig struct {
    Filename   string
    MaxSize    int64
    Daily      bool
    MaxBackups int
    MaxAgeDays int
    UTC        bool
    TimeFunc   func() time.Time
}

type RotatingWriter struct { /* unexported */ }

func NewRotatingWriter(FileConfig) (*RotatingWriter, error)
func (w *RotatingWriter) Write([]byte) (int, error)
func (w *RotatingWriter) Close() error
```

`Filename` is required. `MaxSize`, `MaxBackups`, and `MaxAgeDays` must be
non-negative. Zero disables that limit. `TimeFunc` defaults to `time.Now` and
must not return a zero time. A nil explicit `TimeFunc` is indistinguishable from
the zero value and therefore selects the default.

Parent directories must already exist; the constructor does not create them.
The active file is opened with `O_CREATE|O_APPEND|O_WRONLY`, mode `0640` subject
to umask. Existing permissions are not changed. Symlink handling follows the OS
`os.OpenFile` behavior; V2 does not attempt secure-directory enforcement.

The constructor records the actual size from `Stat`, not zero and not the file
offset. `Write(nil)` and `Write([]byte{})` return `(0, nil)` while open and do not
rotate.

### 7.1 Rotation predicates

Before each non-empty write, using one captured `now`:

```text
sizeDue  = MaxSize > 0 && currentSize > 0 && currentSize + len(p) > MaxSize
dailyDue = Daily && currentSize > 0 && date(now) != activeDate
```

Overflow in `currentSize + len(p)` is treated as size due. The `currentSize > 0`
guard prevents empty backups. An oversized record written to an empty active
file remains intact even though the resulting file exceeds `MaxSize`; it is
rotated before the next non-empty write. A record is never split.

If either or both predicates are true, exactly one rotation attempt occurs.
After a successful rotation, the full record is written to the new active file.

`date(t)` is the Gregorian `YYYY-MM-DD` in UTC when `UTC` is true and in
`t.Local()` otherwise. Clock movement backward is treated as a date change just
like movement forward; this favors deterministic boundaries over guessing.

### 7.2 Restart and active date

At construction:

- a missing or empty active file gets `activeDate = date(now)`;
- a non-empty active file gets `activeDate = date(file.ModTime())` in the
  configured zone;
- if that modification date is in the future relative to `date(now)`, use
  `date(now)` instead, avoiding an immediate misleading future-dated archive.

Consequently, the first write after restart rotates a non-empty file whose
trusted modification date belongs to another calendar day. No sidecar state is
used. This explicitly accepts the limitation that external `mtime` changes can
alter the inferred day.

### 7.3 Backup names

The active path is stable. A backup path is:

```text
<Filename>.<activeDate>.NNNN
```

where `NNNN` starts at `0001` and is at least four digits (it may grow beyond
9999). The date describes the archived active data, not the date of the write
that triggered rotation. Sequence allocation scans matching backups and chooses
the lowest positive unused number. It uses an exclusive destination operation
or an existence recheck so an existing path is never overwritten.

Only a suffix matching exactly `.<YYYY-MM-DD>.<positive decimal sequence>` is a
managed backup. Lookalike files are unrelated and never cleaned up.

Multi-process use is unsupported: instances may race between name selection and
rename. The implementation MUST detect the collision and return an error; it
must never overwrite the other process's backup.

## 8. Rotation state machine and failure recovery

All mutable writer state is protected by one mutex:

```text
OPEN       file != nil; accepts writes
RECOVERING file may be nil; a previous rotation could not restore OPEN
CLOSED     terminal; all writes fail with ErrClosed
```

`Write` holds the mutex for predicate evaluation, rotation/recovery, and the
complete file write. `Close` takes the same mutex. Calls therefore do not
interleave. No callback into logger error handling occurs inside the writer.

### 8.1 Portable rotation transaction

Windows cannot reliably rename an open file, so every platform uses this same
sequence:

1. Reserve a non-existing backup name.
2. Close the active descriptor. If close fails, immediately try to reopen the
   unchanged active path in append mode. Return the close error; do not rename.
3. Rename active to backup without overwrite.
4. Open a new active file with the normal flags and mode.
5. Stat the new file, set size to its actual size (normally zero), set
   `activeDate = date(now)`, transition to OPEN, then run retention cleanup.

No triggering record is written until all five rotation steps required for OPEN
have succeeded. Thus an error always means the caller's current record was not
accepted by this writer.

### 8.2 Failure matrix

| Failure | Mandatory recovery and resulting state |
| --- | --- |
| reserve/name collision | keep existing descriptor and OPEN; return error |
| close | reopen active append; OPEN if successful, otherwise RECOVERING |
| rename | reopen unchanged active append; OPEN if successful, otherwise RECOVERING |
| new-active open after rename | rename backup back to active, then reopen append; OPEN if both succeed; otherwise RECOVERING |
| stat new active | close it, attempt rollback as above; OPEN if rollback succeeds; otherwise RECOVERING |
| cleanup | remain OPEN; rotation succeeded; report cleanup error and do not write triggering record |

Cleanup failure intentionally returns an error before writing the record, even
though rotation succeeded, so every failure remains observable and the call has
simple all-or-no-record semantics. The next call can write normally; cleanup is
retried only after a later rotation.

Rollback MUST NOT overwrite an active path that appeared concurrently. If a
backup was successfully created but rollback cannot safely restore it, it stays
as a valid backup. Recovery never deletes it.

### 8.3 RECOVERING behavior

At the start of each non-empty `Write` in RECOVERING:

1. Try to open the active path in append mode, creating it only if absent.
2. Stat it and rebuild `currentSize` and `activeDate` using the restart rules.
3. On success transition to OPEN and process the same write normally.
4. On failure remain RECOVERING and return `(0, error)` wrapping both the prior
   rotation context and current recovery error.

There is no retry loop within one call and no sleep. This permits recovery from
transient permission or filesystem failures without busy waiting or silently
redirecting output.

### 8.4 Normal writes and close

The file write is called once. The writer returns the actual `n`. A short write
with nil error becomes `io.ErrShortWrite`; `currentSize` increases by `n` even
on error. The call is never retried.

`Close` is idempotent. Its first call sets CLOSED even if closing the descriptor
returns an error; subsequent calls return nil. `Write` after that returns
`(0, ErrClosed)`. A concurrent write either completes before close or observes
CLOSED; it cannot write through a descriptor being closed.

## 9. Retention

Retention runs synchronously only after a successful rotation. The active file
is excluded.

Let `today = date(now)`. Backup age is an integer calendar-day difference
between `today` and the date in its filename, in the configured timezone. With
`MaxAgeDays = N > 0`, delete backups where `ageDays >= N`; for example, `1`
keeps only backups dated today. Future-dated backups have age zero and are not
age-deleted.

After age deletion, sort by filename date ascending, sequence ascending. With
`MaxBackups = N > 0`, delete oldest backups until at most N remain. When both
limits are enabled, a backup is deleted when either rule requires it.

Only exact managed names for the configured base filename may be deleted.
Deletion errors are accumulated with `errors.Join`; cleanup continues so one
undeletable backup does not prevent handling others. See the failure matrix for
how the aggregate is reported. Zero values mean unlimited, not zero retained.

## 10. slog compatibility

`NewSlogHandler` returns an immutable `slog.Handler` view over the logger. It
uses the logger's level, clock/record time, format, shared output, encoder, and
error policy. `Handle` returns errors directly and MUST NOT invoke the logger's
error handler, matching `slog.Handler`'s explicit error contract.

### 10.1 Level and record mapping

| slog level | V2 level/name |
| --- | --- |
| `< slog.LevelDebug` | trace |
| `slog.LevelDebug .. < slog.LevelInfo` | debug |
| `slog.LevelInfo .. < slog.LevelWarn` | info |
| `slog.LevelWarn .. < slog.LevelError` | warn |
| `>= slog.LevelError` | error |

No slog level maps to fatal. `Enabled` applies this mapping and the logger
threshold. `Handle` silently returns nil for a disabled record, as required of a
well-behaved handler.

For enabled records, system fields use the mapped level and `Record.Message`.
A non-zero `Record.Time` is preserved and formatted under the logger's UTC
setting; a zero time uses the logger clock. `Record.PC` is ignored because V2
has no source option.

Logger shared fields precede attributes stored by `WithAttrs`, which precede
attributes in the record. Duplicate-key rules apply to the resolved structure.

### 10.2 Attribute resolution

- Resolve `slog.LogValuer` repeatedly until the value is not `KindLogValuer`,
  with the same recursion guard behavior as `slog`'s reference handlers.
- A resolution error becomes a string value under the original key, following
  standard slog handler behavior, rather than panicking.
- `KindTime` and `KindDuration` use the native representations in section 4.
- `KindAny` uses native `Any` semantics.
- An empty attribute (`key == ""` and zero value) is discarded.
- A group-valued attribute with an empty key is inlined into its current group.
- Empty groups after resolving and discarding empty attributes are omitted.

### 10.3 Groups

JSON groups are actual nested objects, not dotted flat keys. For example:

```go
h := handler.WithGroup("http").WithAttrs([]slog.Attr{
    slog.String("method", "GET"),
}).WithGroup("request")
```

with `slog.Int("id", 7)` produces the user portion:

```json
{"http":{"method":"GET","request":{"id":7}}}
```

The exact semantics are:

- `WithGroup("")` returns an equivalent handler and opens no group.
- `WithGroup(name)` appends an open group path. It emits nothing until an
  attribute is placed inside it.
- `WithAttrs` stores its attributes at the currently open group path at the
  moment of the call.
- Later `WithGroup` calls do not move earlier attributes into the new group.
- Multiple calls are cumulative and derived handlers never mutate parents.
- Groups with no surviving attributes are omitted recursively.
- Reserved names are forbidden only in the root object. A key such as
  `http.time` is valid because `time` is nested.
- Duplicate keys are rejected within each resolved object, including a clash
  between an attribute-created group and a scalar of the same name.

Console output flattens that identical tree using the escaping rule in section
6.2. This changes presentation only, never grouping or conflict detection.

## 11. Syslog

```go
type SyslogNetwork string

const (
    SyslogLocal SyslogNetwork = "local"
    SyslogUDP   SyslogNetwork = "udp"
    SyslogTCP   SyslogNetwork = "tcp"
)

type SyslogConfig struct {
    Network  SyslogNetwork
    Address  string
    Tag      string
    Facility int
    Timeout  time.Duration
}

type SyslogWriter struct { /* unexported */ }

func NewSyslogWriter(SyslogConfig) (*SyslogWriter, error)
func (w *SyslogWriter) Write([]byte) (int, error)
func (w *SyslogWriter) Close() error
```

`SyslogWriter` implements `io.Writer` and a private level-aware sink interface.
When supplied to `New`, Logger uses that private interface so each event maps to
the right Syslog priority. Direct `Write` uses informational severity. Input is
treated as the complete already-encoded message; one trailing newline is
removed before Syslog framing.

Facility must be one of the standard RFC 5424 facility numbers 0 through 23.
Severity mapping is trace/debug -> debug (7), info -> informational (6), warn ->
warning (4), error -> error (3), fatal -> critical (2). Priority is
`facility*8 + severity`.

For `udp` and `tcp`, Address is required and parsed by `net.Dialer`. Timeout must
be non-negative; zero means no library-added deadline. One connection is opened
by the constructor and protected by a mutex. V2 does not reconnect or retry
after write failure. TCP uses RFC 6587 octet-counting framing; UDP sends one RFC
5424 message per datagram. Structured application fields remain in the selected
JSON or console payload; V2 does not translate them into RFC 5424 structured
data elements.

Local Syslog uses the platform's standard local Syslog facility through
platform-specific files. On Windows it returns `ErrSyslogLocalUnsupported` at
construction while the package still compiles. Address must be empty for local
and non-empty for UDP/TCP. Tag must contain printable non-space ASCII and be at
most 48 bytes. An empty tag defaults to the executable base name. Newlines in a
payload are escaped so one call cannot inject multiple Syslog records.

`Write` follows the `io.Writer` contract. UDP success returns `len(p)` only when
the complete datagram was accepted locally. TCP handles partial socket writes
internally until the one frame is complete or an error occurs, because framing
defines a recoverable byte stream; on error it returns the count of payload
bytes known to be represented only when determinable, otherwise zero and the
error. Close is idempotent; later writes return `ErrClosed`.

For structured slog-to-Syslog use:

```go
sw, err := logger.NewSyslogWriter(cfg)
l, err := logger.New(sw, logger.WithFormat(logger.JSONFormat))
sl := slog.New(logger.NewSlogHandler(l))
```

Network, encoding, and close errors are never swallowed. There is no buffer,
backoff, retry loop, or delivery guarantee.

## 12. Cross-platform constraints

- Platform-specific local Syslog code MUST be isolated with build constraints
  and provide the same constructor symbols on every supported OS.
- Rotation MUST use the portable close/rename/open transaction in section 8;
  do not rely on renaming an open file.
- File path comparison and operations follow the host OS. Managed backup
  matching operates on the base filename in its parent directory, not string
  prefix matching across full paths.
- Tests MUST NOT assume Unix path separators, permission enforcement on
  Windows, case-sensitive filesystems, or atomic replacement semantics.
- Public core APIs must compile under `GOOS=linux`, `darwin`, and `windows`.

## 13. Performance requirements

Correctness has priority. The implementation SHOULD append primitives directly
to reusable buffers, avoid `map[string]any` in native hot paths, avoid reflection
outside `Any`/slog resolution, and avoid `fmt.Sprintf` for primitive encoding.
Disabled events allocate no encoding buffer and perform no output work.

Benchmarks MUST cover disabled events, JSON, console, primitive fields, `Any`,
one and parallel goroutines, RotatingWriter without and during rotation, and the
slog adapter. Report ns/op, B/op, allocs/op, and throughput where meaningful.
A comparison with zerolog MAY be kept in a separate benchmark module so it does
not violate V2's zero-dependency module; workloads must have equivalent fields,
messages, timestamp behavior, and sinks.

No numeric performance promise is a release gate until a reproducible baseline
is recorded. Correctness, race freedom, and failure observability are gates.

## 14. Required tests

All tests use the standard library. Failure injection should use small internal
interfaces or test-only hooks, not exported production APIs.

### Logger, Event, and encoders

- every exact level and between-level slog mapping;
- disabled path: no buffer, clock, writer, or handler call;
- `Fatal` writes without exiting;
- all field types, insertion order, and shared/event ordering;
- reserved, duplicate, empty, nested, and escaped keys;
- second submission and post-submission methods;
- child immutability and caller slice mutation;
- JSON validity, escaping, Unicode, invalid UTF-8, and exactly one newline;
- `Any` marshal errors, cycles, bad marshalers, and special floats;
- console quoting, dotted-group escaping, and exact ANSI boundaries;
- short writes and error handler exactly-once/non-recursion behavior;
- concurrent loggers sharing an intentionally unsafe writer.

### RotatingWriter and retention

- new/create, existing append, missing parent, invalid config, and modes;
- size at `<`, `==`, and `>` boundaries, empty writes, overflow defense, and an
  oversized record;
- daily boundary in local time and UTC, backward clock, DST, and simultaneous
  size/daily triggers;
- restart with missing, empty, prior-day, same-day, and future-`mtime` files;
- backup archived date, sequence collisions, >9999, and unrelated lookalikes;
- each failure-matrix row, rollback success/failure, RECOVERING recovery, and
  proof the triggering record was not written on error;
- partial writes with size reconciliation;
- age-only, count-only, combined, future-date, and deletion-error retention;
- concurrent writes, Write/Close races, idempotent Close, and write after close.

### slog and Syslog

- all `slog.Handler` methods and immutable derivation;
- `WithAttrs` before/after groups, nested/empty/inline groups, scalar/group
  conflicts, `LogValuer`, resolution errors, and record time;
- Syslog priority/facility mapping and exact UDP/TCP framing;
- local supported/unsupported builds, bad config, timeout, partial TCP write,
  connection failure, newline injection defense, and Close.

The following must pass:

```sh
go test ./...
go test -race ./...
go vet ./...
GOOS=linux go test ./...
GOOS=darwin go test ./...
GOOS=windows go test ./...
```

Cross-OS commands may be compile-only where binaries cannot run on the host.

## 15. Release acceptance criteria

V2 is acceptable only when all of the following are true:

1. The implementation matches every normative rule in this document, or this
   document is reviewed and amended first.
2. Required functional, injected-failure, race, vet, and cross-build checks pass.
3. The V2 module has no non-standard-library dependency in `go.mod` or source.
4. Every exported declaration has Go documentation and stable examples.
5. No log method terminates the application or silently drops an enabled record.
   Environmental loss remains possible, but every locally observed failure is
   returned or reported exactly once.
6. JSON success always produces valid JSON Lines; console output meets its exact
   quoting and color rules.
7. Rotation failures leave OPEN, RECOVERING, or CLOSED state exactly as defined,
   never overwrite backups, and never claim the current record succeeded.
8. Restart daily rotation, retention calendar age, slog grouping, field
   conflicts, close ownership, and Windows behavior have explicit passing tests.
9. Benchmarks and their environment are recorded for review; optimization has
   not weakened correctness.
10. README, migration guide, and V2 examples are updated before tagging
    `v2.0.0`.

## 16. Implementation guardrails

Implementation work MUST NOT:

- invent additional exported APIs or change these semantics without review;
- add third-party dependencies or a dependency on `go-netx`;
- introduce asynchronous output, background rotation, or background retention;
- use global mutable logger configuration;
- retry ordinary partial `io.Writer` records;
- swallow encoding, filesystem, network, cleanup, or close errors;
- reuse Event objects through a pool;
- flatten JSON slog groups; or
- use `panic`, `log.Fatal`, or `os.Exit` for recoverable conditions.

Implementation should proceed in test-backed stages: core values and encoding,
Logger/Event lifecycle, slog adapter, RotatingWriter state machine and retention,
Syslog, then cross-platform and benchmark work. This document authorizes no code
change by itself; implementation starts only after the repository owner
separately approves it.
