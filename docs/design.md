# go-logger V2 detailed design

| Item | Value |
| --- | --- |
| Project | `github.com/ninepeach/go-logger` |
| Target module | `github.com/ninepeach/go-logger/v2` |
| Target release | `v2.0.0` |
| Status | Review draft — owner approval required before implementation |
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

No library-owned background goroutine is permitted. V2 exposes no `Sync()`
method and does not call `File.Sync()` implicitly.

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

Only the seven declared levels and two declared formats are valid native option
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
and error handler are shared. Primitive and byte fields are copied. `Context.Any` is serialized into an
immutable JSON representation during `Context.Logger()`; the resulting bytes
are owned by the derived logger, so subsequent caller mutation cannot affect it.
Custom marshalers execute during `Context.Logger()`. Each failed field is
snapshotted as JSON `null`, while other fields are preserved. `Logger()` MUST
return the usable derived logger together with an error wrapping
`ErrFieldEncoding`; multiple failures are combined using `errors.Join`.
The caller MAY use the returned logger despite the non-nil error. Ordinary field encoding failures MUST NOT return a nil logger. Calling
`Logger()` a second time on the same builder MUST return
`(nil, ErrEventConsumed)` without re-running marshalers or modifying the
previously returned logger; this is the only defined nil-logger outcome for a
valid builder. The builder may collect
`Any` values until `Logger()`; callers MUST NOT mutate them concurrently with
that call. Existing parent snapshots are never re-serialized. A `Context`
builder is single-use after `Logger()` and is not goroutine-safe. Field
methods continue collecting after a field failure so independent fields are
not discarded. `Context.Logger()` does not automatically invoke the error
handler; its returned error is the caller's responsibility.

## 4. Record and field semantics

Every record has system members followed by shared fields followed by event
fields. Within each user-field section, insertion order is preserved.

### 4.1 Reserved names and conflicts

The system members `time`, `level`, and `message` are reserved at the root.
Only user attributes whose root key is `time`, `level`, `message`, or
`fields` are relocated into a root `fields` object. Other user attributes
remain at the root. The root `fields` object is omitted if no conflicting
attribute exists. A user attribute literally named `fields` is stored as
`fields.fields`; it cannot replace the system-created conflict container.
Shared, event, and slog attributes follow the same rule, including root groups.
Conflict resolution runs on the normalized user tree before relocation; a
relocated key and a non-conflicting root key never alias.

Repeated keys within the same object use **last-write-wins**, including a
child/event field overriding a shared field. The encoder MUST emit each key
exactly once with the last value, retaining the position of its first insertion.
A scalar/group conflict also uses last-write-wins: the later complete value
replaces the earlier value; attributes that arrive subsequently under that
group establish a new group object. Keys in distinct nested groups do not
conflict. An empty key is allowed. A duplicate MUST NOT discard the record.

This requires a normalized field representation before final encoding; native
primitive field methods may append typed field entries without reflection, but
MUST NOT emit duplicate JSON keys directly to the final output. JSON and
Console MUST resolve conflicts identically. The system metadata remains
independent of user fields.

### 4.2 Value representation

| Method/type | JSON | Console |
| --- | --- | --- |
| string | JSON string | quoted when required |
| signed/unsigned integer | JSON number | decimal |
| finite float | shortest round-trip JSON number | shortest round-trip number |
| `NaN`, `+Inf`, `-Inf` | `null` and `ErrFieldEncoding` | `null` and `ErrFieldEncoding` |
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

`Err(nil)` adds nothing. A non-nil `Err` is equivalent to a string field with key `error`;
conflict detection depends only on that key, not the result of `Error()`. Calling `Error()` MUST
be panic-safe, including for typed-nil errors: a panic degrades that field to
`null` and produces `ErrFieldEncoding` rather than crashing the logger.

`Any` MUST use `encoding/json` semantics, including `json.Marshaler` and
`encoding.TextMarshaler`. Cycles, unsupported values (including functions,
channels, complex values, and map keys), invalid custom marshaler output,
and non-finite floats cause field-level degradation, not record rejection.
A failed field is replaced with JSON `null` (console text `null`; the
console representation intentionally cannot distinguish this from an ordinary
string field whose displayed value is `null`), and an
error wrapping `ErrFieldEncoding` identifies the field path and underlying
cause without exposing its value. This preserves valid JSON but does not
guarantee compatibility with every downstream schema. Panics from custom
marshalers, `slog.LogValuer`, or `error.Error()` are recovered at the field
boundary and treated as field-level failures; application error-handler
panics are never recovered.
Other valid fields and the record MUST still be written. `Any` output is embedded as JSON in
JSON format. In console format, scalars use normal console rules and composite
values use compact JSON. For `Event.Any`, marshaling occurs at submission, and its result is owned by
the event until the synchronous write finishes. For `Context.Any`, marshaling
occurs during `Context.Logger()` as specified in section 3.2. Custom marshalers
are invoked exactly once per `Any` field in the relevant phase.
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
- The first submission logically marks the Event submitted before encoding or
  writing; no atomic instruction or concurrent-use guarantee is required. Encoding or write failure does not make it reusable.
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

`Msg` and `Send` call the configured error handler exactly once for all
field-degradation, validation, short-write, or output errors in that submission.
`MsgErr` and `SendErr` return the aggregated error and MUST NOT call the handler.
Multiple field failures and any output error are combined with `errors.Join`.
A non-nil `MsgErr` result does not imply that the record was not written:
`errors.Is(err, ErrFieldEncoding)` indicates at least one degraded field;
output failures remain separately discoverable through their wrapped causes.
Field degradation alone MUST NOT prevent writing the record. The handler is invoked after all internal
logger/writer locks are released. Custom handlers may be invoked concurrently
from different goroutines; callers MUST provide their own synchronization. If the handler panics, that panic belongs to
application code and is not recovered by the logger.

The default handler writes a compact diagnostic directly to `os.Stderr` using a
private mutex and does not call this package recursively. It MUST emit at most
one diagnostic per one-second interval, using monotonic elapsed time, and count
suppressed errors. The next emitted diagnostic MUST include the number of
errors suppressed since the previous emission; suppressed errors do not invoke
additional output. The rate-limit state is shared by loggers using the same
default handler, and is safe for concurrent calls. Failure of the fallback
write is ignored because there is no further safe reporting channel.

The package exposes sentinel errors usable with `errors.Is`:

```go
var (
    ErrInvalidConfig = errors.New("logger: invalid configuration")
    ErrFieldEncoding = errors.New("logger: field encoding degraded")
    ErrEventConsumed = errors.New("logger: event already submitted")
    ErrNilLogger     = errors.New("logger: nil logger")
    ErrClosed        = errors.New("logger: writer closed")
    ErrSyslogLocalUnsupported = errors.New("logger: local syslog unsupported")
    ErrSyslogUnavailable = errors.New("logger: syslog connection unavailable")
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

Nested slog groups are flattened by escaping each path segment independently:
replace each literal backslash with two backslashes, then each literal dot with
backslash-dot; join escaped segments with an unescaped dot. Apply the section's
ordinary key quoting rule **after** path escaping to the entire joined key.
Thus segments `["http", "method"]` become `http.method`, while a single
segment `"http.method"` becomes `http\\.method`; a segment with whitespace
is quoted after escaping. JSON groups remain nested objects.

### 6.3 Synchronization and short writes

One shared mutex in the output wrapper surrounds each complete record write for
ordinary `io.Writer` outputs. Derived loggers share that wrapper. Two independent `New` calls given the
same ordinary writer do NOT share a mutex: the caller MUST provide a
concurrency-safe writer or an external synchronized wrapper. Thus bytes
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
    OnMaintenanceError func(error)
}

type RotatingWriter struct { /* unexported */ }

func (w *RotatingWriter) TakeMaintenanceError() error

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
the process's system-local location (`time.Local`) otherwise. The Location
embedded in a custom `TimeFunc` result does not select the rotation timezone. Clock movement backward is treated as a date change just
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
that triggered rotation. Sequence allocation scans matching backups for the archived date and uses
`max(existing sequence) + 1`, or `0001` if none exist. A deleted sequence
MUST NOT be reused while a higher sequence exists. **V2 supports only a single RotatingWriter owner of a given
active path**. All rotation and cleanup operations are serialized by that
instance's mutex. The owner MUST NOT share its active path with another
process, another RotatingWriter, or an external writer, and external actors
MUST NOT create, replace, or rename managed backups while it runs.

The implementation checks the candidate backup path immediately before rename
and returns a collision error if it exists. This is a single-owner contract,
**not** an atomic no-replace guarantee against an external concurrent actor:
`os.Rename` may replace a destination on some platforms. The library MUST NOT
claim protection against actors that violate the ownership contract. No
exclusive placeholder file plus ordinary `os.Rename` may be described as
providing atomic no-replace semantics. Tests MUST verify no overwrite under
the supported single-owner model and explicit collision detection.


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
3. Verify the selected backup path is still absent and rename active to
   backup under the single-owner contract of section 7.3.
4. Open a new active file with the normal flags and mode.
5. Stat the new file, set size to its actual size (normally zero), set
   `activeDate = date(now)`, and transition to OPEN.
6. Write the triggering record once, updating size by the actual accepted byte
   count; do not retry a short write.
7. Run retention cleanup after that write, regardless of whether the write
   succeeded. Cleanup failure is reported independently, never as a `Write`
   error for an otherwise successful record.

No triggering record is written to the new active file until steps 1–5
succeed. If rotation fails but the old active path is restored to OPEN, the
triggering record MUST instead be written once to that active file; successful
fallback returns `(len(p), nil)` and reports rotation failure solely through
the maintenance-error channel. This permits active files to grow without bound
while rotation is unavailable: reliability takes priority over MaxSize.
After a failed rotation, suppress further rotation attempts for 60 seconds
measured using a private monotonic clock based on `time.Now`; `FileConfig.TimeFunc`
controls calendar/rotation dates only and MUST NOT control retry timing.
Tests MUST inject an internal monotonic-clock hook (not a public option) to
advance the backoff deterministically without sleeping. Backoff survives
RECOVERING -> OPEN transitions and suppresses both size and daily rotation;
normal writes continue, including writes that exceed MaxSize. The next eligible non-empty write may retry; no
background timer or goroutine is required. While daily rotation is suppressed,
`activeDate` remains the archived active file's original date. Thus records
from a later day may be archived under the older date and age retention may
remove them earlier than their individual write dates suggest; this is an
accepted consequence of prioritizing successful writes. If fallback writing itself fails,
return its actual byte count and write error. A
write error at step 6 may mean a partial record was accepted, as indicated by
its returned byte count. Cleanup failures at step 7 never change the returned
write count or error.

### 8.2 Failure matrix

| Failure | Mandatory recovery and resulting state |
| --- | --- |
| reserve/name collision | keep existing descriptor and OPEN; fallback-write triggering record, report rotation maintenance error, begin 60-second backoff |
| close | reopen active append; fallback-write if OPEN; otherwise RECOVERING and return error |
| rename | reopen unchanged active append; fallback-write if OPEN; otherwise RECOVERING and return error |
| new-active open after rename | rename backup back to active, then reopen append; fallback-write if OPEN; otherwise RECOVERING and return error |
| stat new active | close it, attempt rollback as above; fallback-write if OPEN; otherwise RECOVERING and return error |
| cleanup | remain OPEN; preserve triggering write result and report cleanup error through the independent maintenance-error channel |

Rotation and retention maintenance errors MUST NOT invalidate an accepted
record. The writer stores maintenance errors in an internal bounded maintenance-error slot (latest
error wins). Add public `TakeMaintenanceError() error`: it atomically returns
and clears the last pending maintenance error; nil means none. Maintenance failures
are also reported through an optional `FileConfig.OnMaintenanceError func(error)`
callback, invoked **after releasing the writer mutex**. The callback may run
concurrently with other callbacks and MUST NOT call back into the same writer.
A callback panic is application-owned. If no callback is configured, the writer MUST emit a compact diagnostic to
`os.Stderr` using a private synchronized, non-recursive fallback, outside its
mutex. Repeated maintenance failures MUST be rate-limited (at most one default
diagnostic per 60 seconds per writer); the pending latest-error slot remains
available via `TakeMaintenanceError`. When both the callback and
`TakeMaintenanceError` are used, each receives the error: the callback is a
notification and the slot is a separate latest-error snapshot. Cleanup is
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

Let `today = date(now)`. Backup age is an integer Gregorian civil-date difference
between `today` and the date in its filename, in the configured timezone.
Calculate by civil-day ordinals, not by dividing elapsed duration by 24 hours;
DST transitions must not change the result. With
`MaxAgeDays = N > 0`, delete backups where `ageDays >= N`; for example, `1`
keeps only backups dated today. Future-dated backups have age zero and are not
age-deleted.

After age deletion, sort by filename date ascending, sequence ascending. With
`MaxBackups = N > 0`, delete oldest backups until at most N remain. When both
limits are enabled, a backup is deleted when either rule requires it.

Only exact managed names for the configured base filename may be deleted.
Deletion errors are accumulated with `errors.Join`; cleanup continues so one
undeletable backup does not prevent handling others. See the independent maintenance-error reporting contract in section 8.2. Zero values mean unlimited, not zero retained.

## 10. slog compatibility

`NewSlogHandler` returns an immutable `slog.Handler` view over the logger. It
uses the logger's level, clock/record time, format, shared output, encoder, and
error policy. `Handle` returns errors directly AND reports each enabled submission failure
exactly once through the Logger's configured error handler. No separate
slog error callback or slog-specific public option is provided. This is required
because `slog.Logger` discards errors returned by `Handler.Handle`.
The adapter MUST use a submission path that does not itself invoke the Logger
error handler before the adapter reports the error. A disabled record causes
neither a callback nor an error.

### 10.1 Level and record mapping

`Enabled` MUST compare the **original integer value** of `slog.Level`
against the configured native minimum level, without rounding or bucket
mapping. `DisabledLevel` disables all records. `Handle` repeats this exact
check and returns nil for disabled records. Custom slog levels between standard
values therefore retain their precise filtering behavior.

For presentation, standard levels use `trace`, `debug`, `info`, `warn`,
and `error` at their exact native values. Any other slog level is displayed
using the lowercase form of the canonical `slog.Level.String()` textual
representation (e.g. `slog.Level(2)` becomes `info+2`, not `2`), and its original integer is retained in the normalized
record. Slog never implicitly emits `fatal`; only native Fatal does.

For enabled records, system fields use the original level and
`Record.Message`. A non-zero `Record.Time` is preserved and formatted under
the logger's UTC setting; a zero time uses the logger clock. `Record.PC` is
ignored because V2 has no source option.

Logger shared fields precede attributes stored by `WithAttrs`, which precede
attributes in the record. Last-write-wins rules from section 4.1 apply to the
resolved structure.

### 10.2 Attribute resolution

- Resolve `slog.LogValuer` by calling `LogValue()` directly in a bounded
  resolution loop with a fixed internal recursion limit. MUST NOT use
  `slog.Value.Resolve()`, because it converts panics into ordinary error values
  that cannot reliably be distinguished from intentionally returned errors.
- Recover a `LogValue()` panic at the field boundary. A panic or resolution
  limit exhaustion MUST degrade only the affected field to `null`, wrap
  `ErrFieldEncoding`, and preserve other fields and the record. A normally
  returned error value is an ordinary value, not a resolution failure.
  Diagnostics MUST NOT embed panic stacks or arbitrary field contents.
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
- Reserved-name relocation applies only at the root object. A nested key
  such as `http.time` is valid without relocation.
- Duplicate keys and scalar/group conflicts use section 4.1's last-write-wins
  rule within each resolved object.

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
    DialTimeout  time.Duration
    WriteTimeout time.Duration
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

For UDP and TCP, `Address` is required. `DialTimeout` and
`WriteTimeout` MUST be non-negative. Zero means a finite default of 5 seconds
for each. DialTimeout bounds connection establishment; WriteTimeout applies
to **each complete message**, not each individual socket write. Set the
connection write deadline once before sending the frame/datagram. Local Syslog
uses the same per-message WriteTimeout where the platform supports deadlines;
otherwise the constructor MUST reject a requested local timeout if it cannot
enforce it. The local implementation MUST NOT silently claim bounded writes.

A remote connection is opened by the constructor and protected by a mutex.
V2 does not reconnect or retry after transport failure. TCP uses RFC 6587
octet-counting framing; UDP sends one RFC 5424 message per datagram.

The RFC 5424 wire message is exactly:
`<PRI>1 TIMESTAMP HOSTNAME APP-NAME PROCID MSGID STRUCTURED-DATA MSG`.
TIMESTAMP is the event time in UTC RFC3339 format; HOSTNAME is
`os.Hostname()` or `-` on failure; APP-NAME is validated Tag (max 48
printable non-space ASCII bytes); PROCID is the decimal `os.Getpid()`;
MSGID and STRUCTURED-DATA are both `-`. The message body is the selected
JSON or Console encoded record, without its trailing newline, encoded as UTF-8
with **no BOM**. The message's embedded newlines and carriage returns are
escaped as literal `\\n` and `\\r`. The library does not map user
fields into RFC 5424 STRUCTURED-DATA. For TCP, prefix the byte length of the
entire RFC 5424 message, followed by one ASCII space; the length excludes the
prefix itself.

Local Syslog uses the platform's standard local facility through
platform-specific files. On Windows it returns `ErrSyslogLocalUnsupported`.
Address must be empty for local and non-empty for UDP/TCP. An empty Tag
defaults to the executable base name, then undergoes the same validation.

A TCP frame write loops over partial writes until the complete frame is
accepted or a failure occurs. If any TCP frame fails or times out, close the
connection and permanently mark the writer BROKEN. All subsequent writes
return `ErrSyslogUnavailable` until the caller constructs a new writer.
A failed UDP datagram also marks the writer BROKEN, with no automatic retry.
UDP success means only local acceptance, not remote receipt. Direct
`Write` returns `len(p)` only on complete frame/datagram acceptance and
returns `(0,error)` on any framing/transport failure, even if a TCP prefix
was partially sent. This is a record-level contract, not a claim that no
bytes reached the network. Close is idempotent; writes after Close return
`ErrClosed`. Severity mapping remains trace/debug=7, info=6, warn=4,
error=3, fatal=2; unknown custom slog levels use the nearest lower standard
severity band, without changing filtering.


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
- disabled path: no buffer, clock, writer, or handler call; benchmark allocations
  before freezing the Event API (do not assume nil-safe Events are required);
- `Fatal` writes without exiting;
- all field types, insertion order, and shared/event ordering;
- reserved, duplicate last-write-wins, reserved-field namespace, empty, nested, and escaped keys;
- second submission and post-submission methods;
- child immutability, Context.Any snapshot and caller slice/map mutation;
- Context.Logger second invocation returns `(nil, ErrEventConsumed)` without
  repeating marshalers; first invocation can return usable logger plus error;
- JSON validity, escaping, Unicode, invalid UTF-8, and exactly one newline;
- `Any` field-level degradation, cycles, bad marshalers including panics,
  special floats, typed-nil `Err` panics, `ErrFieldEncoding`, `errors.Join`,
  and successful writes with non-nil `MsgErr`; Context.Any degraded snapshots
  with usable non-nil Logger and non-nil error;
- console quoting, dotted-group escaping, and exact ANSI boundaries;
- short writes and error handler exactly-once/non-recursion behavior;
- default Logger error handler one-second rate limit, suppression counts,
  and concurrent calls; console degraded-null presentation ambiguity;
- concurrent derived loggers sharing an intentionally unsafe writer.

### RotatingWriter and retention

- new/create, existing append, missing parent, invalid config, and modes;
- size at `<`, `==`, and `>` boundaries, empty writes, overflow defense, and an
  oversized record;
- daily boundary in local time and UTC, backward clock, DST, and simultaneous
  size/daily triggers;
- restart with missing, empty, prior-day, same-day, and future-`mtime` files;
- backup archived date, max-plus-one monotonic sequence after retention,
  sequence collisions, >9999, and unrelated lookalikes;
- each failure-matrix row, rollback success/failure, RECOVERING recovery, and
  fallback write after recoverable rotation failure, 60-second monotonic
  backoff using an internal monotonic-clock hook, backoff across RECOVERING,
  daily suppression/old `activeDate`, unbounded active growth, and no
  triggering bytes when RECOVERING;
- partial writes with size reconciliation;
- age-only, count-only, combined, future-date, DST, and deletion-error retention;
- cleanup failure does not reject an accepted record; maintenance-error
  retrieval, callback, default stderr reporting and rate limiting;
- concurrent writes, Write/Close races, idempotent Close, and write after close.

### slog and Syslog

- all `slog.Handler` methods and immutable derivation; Logger error-handler
  exactly-once reporting and retained Handle return errors; custom-level
  lowercase presentation;
- `WithAttrs` before/after groups, nested/empty/inline groups, scalar/group
  conflicts, direct `LogValuer` resolution, panic, recursion exhaustion,
  ordinary error values, and record time;
- Syslog priority/facility mapping and exact RFC 5424 UDP/RFC 6587 TCP framing;
- local supported/unsupported builds, bad config, timeout, partial TCP write,
  connection failure, broken TCP stream after partial frame, bounded timeouts,
  newline injection defense, and Close.

The following must pass:

```sh
go test ./...
go test -race ./...
go vet ./...
GOOS=linux GOARCH=amd64 go test -exec=true ./... # compile-only; use CI runners to execute
GOOS=darwin GOARCH=arm64 go test -exec=true ./... # compile-only
GOOS=windows GOARCH=amd64 go test -exec=true ./... # compile-only
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
   never overwrite backups under the single-owner contract, and never claim the
   current record succeeded when its write failed.
8. Restart daily rotation, retention calendar age, slog grouping, field
   conflict resolution, close ownership, and Windows behavior have explicit passing tests.
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
