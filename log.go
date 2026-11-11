package logger

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// Level is the severity of a native log event.
type Level int8

const (
	TraceLevel    Level = -8  // TraceLevel is the most verbose level.
	DebugLevel    Level = -4  // DebugLevel records diagnostic information.
	InfoLevel     Level = 0   // InfoLevel records routine information.
	WarnLevel     Level = 4   // WarnLevel records warnings.
	ErrorLevel    Level = 8   // ErrorLevel records errors.
	FatalLevel    Level = 12  // FatalLevel records critical errors.
	DisabledLevel Level = 127 // DisabledLevel turns off logging.
)

// String returns the lowercase level name.
func (l Level) String() string {
	switch l {
	case TraceLevel:
		return "trace"
	case DebugLevel:
		return "debug"
	case InfoLevel:
		return "info"
	case WarnLevel:
		return "warn"
	case ErrorLevel:
		return "error"
	case FatalLevel:
		return "fatal"
	case DisabledLevel:
		return "disabled"
	default:
		return fmt.Sprintf("level(%d)", l)
	}
}

// ParseLevel parses a native level name, ignoring case and surrounding whitespace.
func ParseLevel(s string) (Level, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, level := range [...]Level{TraceLevel, DebugLevel, InfoLevel, WarnLevel, ErrorLevel, FatalLevel, DisabledLevel} {
		if s == level.String() {
			return level, nil
		}
	}
	return 0, fmt.Errorf("%w: unknown level", ErrInvalidConfig)
}

func validLevel(l Level) bool {
	switch l {
	case TraceLevel, DebugLevel, InfoLevel, WarnLevel, ErrorLevel, FatalLevel, DisabledLevel:
		return true
	default:
		return false
	}
}

// Format selects the built-in record encoding.
type Format uint8

const (
	JSONFormat    Format = iota // JSONFormat writes compact JSON Lines.
	ConsoleFormat               // ConsoleFormat writes human-readable lines.
)

var (
	ErrInvalidConfig          = errors.New("logger: invalid configuration")         // ErrInvalidConfig indicates an invalid option or writer configuration.
	ErrFieldEncoding          = errors.New("logger: field encoding degraded")       // ErrFieldEncoding indicates that a field was replaced by null.
	ErrEventConsumed          = errors.New("logger: already consumed")              // ErrEventConsumed indicates reuse of an event or context.
	ErrNilLogger              = errors.New("logger: nil logger")                    // ErrNilLogger indicates a slog handler has no logger.
	ErrClosed                 = errors.New("logger: writer closed")                 // ErrClosed indicates a terminal writer.
	ErrSyslogLocalUnsupported = errors.New("logger: local syslog unsupported")      // ErrSyslogLocalUnsupported indicates an unsupported platform.
	ErrSyslogUnavailable      = errors.New("logger: syslog connection unavailable") // ErrSyslogUnavailable indicates a transport failure or backoff.
)

type config struct {
	level                 Level
	format                Format
	timestamp, utc, color bool
	report                func(error)
	now                   func() time.Time
}

// Option configures a new logger.
type Option func(*config) error

// WithLevel sets the minimum native level.
func WithLevel(level Level) Option {
	return func(c *config) error {
		if !validLevel(level) {
			return fmt.Errorf("%w: minimum level", ErrInvalidConfig)
		}
		c.level = level
		return nil
	}
}

// WithFormat selects a built-in encoding.
func WithFormat(format Format) Option {
	return func(c *config) error {
		if format != JSONFormat && format != ConsoleFormat {
			return fmt.Errorf("%w: format", ErrInvalidConfig)
		}
		c.format = format
		return nil
	}
}

// WithTimestamp controls inclusion of the record time.
func WithTimestamp(enabled bool) Option {
	return func(c *config) error { c.timestamp = enabled; return nil }
}

// WithUTC controls conversion of timestamps to UTC.
func WithUTC(enabled bool) Option {
	return func(c *config) error { c.utc = enabled; return nil }
}

// WithColor controls console level coloring.
func WithColor(enabled bool) Option {
	return func(c *config) error { c.color = enabled; return nil }
}

// WithErrorHandler sets the callback for submission failures. It may run concurrently.
func WithErrorHandler(handler func(error)) Option {
	return func(c *config) error {
		if handler == nil {
			return fmt.Errorf("%w: nil error handler", ErrInvalidConfig)
		}
		c.report = handler
		return nil
	}
}

// WithTimeFunc sets the clock used at submission. It must be safe for concurrent calls.
func WithTimeFunc(now func() time.Time) Option {
	return func(c *config) error {
		if now == nil {
			return fmt.Errorf("%w: nil clock", ErrInvalidConfig)
		}
		c.now = now
		return nil
	}
}

type sink struct {
	sync.Mutex
	writer io.Writer
}

// Logger is a concurrency-safe, immutable logger. Its output is owned by the caller.
type Logger struct {
	config config
	sink   *sink
	fields []field
}

// New constructs a logger. A nil output selects standard error.
func New(output io.Writer, options ...Option) (*Logger, error) {
	c := config{timestamp: true, utc: true, now: time.Now, report: defaultDiagnostic.report}
	for _, option := range options {
		if option == nil {
			return nil, fmt.Errorf("%w: nil option", ErrInvalidConfig)
		}
		if err := option(&c); err != nil {
			return nil, err
		}
	}
	if c.color && c.format != ConsoleFormat {
		return nil, fmt.Errorf("%w: color requires console format", ErrInvalidConfig)
	}
	if output == nil {
		output = os.Stderr
	}
	return &Logger{config: c, sink: &sink{writer: output}}, nil
}

func (l *Logger) enabled(level int) bool {
	return l != nil && l.config.level != DisabledLevel && level >= int(l.config.level)
}

// Enabled reports whether a native event at level will be submitted.
func (l *Logger) Enabled(level Level) bool { return l.enabled(int(level)) }

// WithLevel derives a logger with a new minimum level.
func (l *Logger) WithLevel(level Level) (*Logger, error) {
	derived := *l
	if err := WithLevel(level)(&derived.config); err != nil {
		return nil, err
	}
	return &derived, nil
}

// With starts a single-use builder for shared fields.
func (l *Logger) With() *Context { return &Context{logger: l} }

// Context builds shared fields for a derived logger.
type Context struct {
	logger   *Logger
	fields   []field
	consumed bool
}

// Logger consumes the builder and snapshots its fields. The result remains usable after field encoding errors.
func (c *Context) Logger() (*Logger, error) {
	if c.consumed {
		return nil, ErrEventConsumed
	}
	c.consumed = true
	derived := *c.logger
	derived.fields = append(append([]field(nil), derived.fields...), c.fields...)
	var failures []error
	for i := len(c.logger.fields); i < len(derived.fields); i++ {
		f := &derived.fields[i]
		value, err := snapshot(f.value, c.logger.config.utc)
		if err != nil {
			failures = append(failures, fieldError([]string{f.key}, err))
		}
		f.value = value
	}
	c.fields = nil
	return &derived, errors.Join(failures...)
}

func (c *Context) add(key string, value any) *Context {
	if !c.consumed {
		if c.fields == nil {
			c.fields = make([]field, 0, 4)
		}
		c.fields = append(c.fields, field{key, value})
	}
	return c
}

// Event is a single-use record builder. It must not be copied or used concurrently.
type Event struct {
	logger             *Logger
	level              Level
	disabled, consumed bool
	fields             []field
}

func (l *Logger) event(level Level) *Event {
	return &Event{logger: l, level: level, disabled: !l.Enabled(level)}
}

// Trace starts a trace event.
func (l *Logger) Trace() *Event { return l.event(TraceLevel) }

// Debug starts a debug event.
func (l *Logger) Debug() *Event { return l.event(DebugLevel) }

// Info starts an informational event.
func (l *Logger) Info() *Event { return l.event(InfoLevel) }

// Warn starts a warning event.
func (l *Logger) Warn() *Event { return l.event(WarnLevel) }

// Error starts an error event.
func (l *Logger) Error() *Event { return l.event(ErrorLevel) }

// Fatal starts a critical event.
func (l *Logger) Fatal() *Event { return l.event(FatalLevel) }

func (e *Event) active() bool { return !e.disabled && !e.consumed }

func (e *Event) add(key string, value any) *Event {
	if e.fields == nil {
		e.fields = make([]field, 0, 4)
	}
	e.fields = append(e.fields, field{key, value})
	return e
}

// Msg submits the event with a message and reports any error through the handler.
func (e *Event) Msg(message string) { e.report(e.MsgErr(message)) }

// Send submits the event without a message and reports any error through the handler.
func (e *Event) Send() { e.report(e.SendErr()) }

// MsgErr submits the event with a message and returns any error.
func (e *Event) MsgErr(message string) error { return e.submit(&message) }

// SendErr submits the event without a message and returns any error.
func (e *Event) SendErr() error { return e.submit(nil) }

func (e *Event) report(err error) {
	if err != nil {
		e.logger.config.report(err)
	}
}

func (e *Event) submit(message *string) error {
	if e.consumed {
		return ErrEventConsumed
	}
	e.consumed = true
	if e.disabled {
		return nil
	}
	fields := e.fields
	e.fields = nil
	return e.logger.submit(record{level: int(e.level), name: e.level.String(), message: message}, fields, nil)
}

type diagnostic struct {
	sync.Mutex
	last       time.Time
	suppressed uint64
	now        func() time.Time
}

var defaultDiagnostic = diagnostic{now: time.Now}
var stderrMu sync.Mutex

func stderrDiagnostic(label string, err error, suppressed uint64) {
	stderrMu.Lock()
	defer stderrMu.Unlock()
	message := strings.Join(strings.Fields(err.Error()), " ")
	if suppressed > 0 {
		fmt.Fprintf(os.Stderr, "logger: %s: %s (%d suppressed)\n", label, message, suppressed)
	} else {
		fmt.Fprintf(os.Stderr, "logger: %s: %s\n", label, message)
	}
}

func (d *diagnostic) report(err error) {
	d.Lock()
	defer d.Unlock()
	now := d.now()
	if !d.last.IsZero() && now.Sub(d.last) < time.Second {
		d.suppressed++
		return
	}
	stderrDiagnostic("submission", err, d.suppressed)
	d.last, d.suppressed = now, 0
}

// Str appends a field to the event.
func (e *Event) Str(key string, value string) *Event {
	if !e.active() {
		return e
	}
	return e.add(key, value)
}

// Int appends a field to the event.
func (e *Event) Int(key string, value int) *Event {
	if !e.active() {
		return e
	}
	return e.add(key, int64(value))
}

// Int64 appends a field to the event.
func (e *Event) Int64(key string, value int64) *Event {
	if !e.active() {
		return e
	}
	return e.add(key, value)
}

// Uint64 appends a field to the event.
func (e *Event) Uint64(key string, value uint64) *Event {
	if !e.active() {
		return e
	}
	return e.add(key, value)
}

// Float64 appends a field to the event.
func (e *Event) Float64(key string, value float64) *Event {
	if !e.active() {
		return e
	}
	return e.add(key, value)
}

// Bool appends a field to the event.
func (e *Event) Bool(key string, value bool) *Event {
	if !e.active() {
		return e
	}
	return e.add(key, value)
}

// Time appends a field to the event.
func (e *Event) Time(key string, value time.Time) *Event {
	if !e.active() {
		return e
	}
	return e.add(key, value)
}

// Dur appends a field to the event.
func (e *Event) Dur(key string, value time.Duration) *Event {
	if !e.active() {
		return e
	}
	return e.add(key, value)
}

// Any appends a field to the event.
func (e *Event) Any(key string, value any) *Event {
	if !e.active() {
		return e
	}
	return e.add(key, anyValue{value})
}

// Bytes appends a copy of the byte slice.
func (e *Event) Bytes(key string, value []byte) *Event {
	if !e.active() {
		return e
	}
	return e.add(key, append([]byte{}, value...))
}

// Err appends a non-nil error under the key error.
func (e *Event) Err(value error) *Event {
	if !e.active() || value == nil {
		return e
	}
	return e.add("error", errorValue{value})
}

// Str appends a field to the context.
func (c *Context) Str(key string, value string) *Context { return c.add(key, value) }

// Int appends a field to the context.
func (c *Context) Int(key string, value int) *Context { return c.add(key, int64(value)) }

// Int64 appends a field to the context.
func (c *Context) Int64(key string, value int64) *Context { return c.add(key, value) }

// Uint64 appends a field to the context.
func (c *Context) Uint64(key string, value uint64) *Context { return c.add(key, value) }

// Float64 appends a field to the context.
func (c *Context) Float64(key string, value float64) *Context { return c.add(key, value) }

// Bool appends a field to the context.
func (c *Context) Bool(key string, value bool) *Context { return c.add(key, value) }

// Time appends a field to the context.
func (c *Context) Time(key string, value time.Time) *Context { return c.add(key, value) }

// Dur appends a field to the context.
func (c *Context) Dur(key string, value time.Duration) *Context { return c.add(key, value) }

// Any appends a field to the context.
func (c *Context) Any(key string, value any) *Context { return c.add(key, anyValue{value}) }

// Bytes appends a copy of the byte slice.
func (c *Context) Bytes(key string, value []byte) *Context {
	if c.consumed {
		return c
	}
	return c.add(key, append([]byte{}, value...))
}

// Err appends a non-nil error under the key error.
func (c *Context) Err(value error) *Context {
	if c.consumed || value == nil {
		return c
	}
	return c.add("error", errorValue{value})
}
