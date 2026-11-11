package logger

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"strings"
	"sync"
	"testing"
	"time"
)

func newLogger(t testing.TB, out io.Writer, opts ...Option) *Logger {
	t.Helper()
	l, err := New(out, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

type marshalFunc func() ([]byte, error)

func (m marshalFunc) MarshalJSON() ([]byte, error) { return m() }

type panicError struct{ message string }

func (e *panicError) Error() string { return e.message }

type writerFunc func([]byte) (int, error)

func (w writerFunc) Write(p []byte) (int, error) { return w(p) }

func TestNativeRecord(t *testing.T) {
	var out bytes.Buffer
	clock := time.Date(2026, 11, 11, 17, 23, 0, 123, time.FixedZone("test", 8*3600))
	l := newLogger(t, &out, WithTimeFunc(func() time.Time { return clock }))
	mutable := map[string]int{"n": 1}
	raw := []byte{0, 255}
	builder := l.With().Str("before", "first").Any("map", mutable).Bytes("bytes", raw).Str("level", "user")
	child, err := builder.Logger()
	if err != nil {
		t.Fatal(err)
	}
	if again, err := builder.Logger(); again != nil || err != ErrEventConsumed {
		t.Fatal(again, err)
	}
	mutable["n"] = 9
	raw[0] = 9
	e := child.Fatal().Int("before", 42).Str("message", "field").Str("fields", "literal").Str("", "").Bool("ok", true).
		Float64("ratio", 0.1).Time("when", clock).Dur("elapsed", time.Second).Int64("signed", -2).
		Uint64("unsigned", math.MaxUint64).Err(errors.New("example")).Str("\xff", "\xff")
	if err := e.MsgErr(""); err != nil {
		t.Fatal(err)
	}
	if err := e.Str("ignored", "yes").SendErr(); err != ErrEventConsumed {
		t.Fatal(err)
	}
	want := `{"time":"2026-11-11T09:23:00.000000123Z","level":"fatal","message":"","before":42,"map":{"n":1},"bytes":"AP8=","fields":{"level":"user","message":"field","fields":"literal"},"":"","ok":true,"ratio":0.1,"when":"2026-11-11T09:23:00.000000123Z","elapsed":"1s","signed":-2,"unsigned":18446744073709551615,"error":"example","�":"�"}`
	if got := out.String(); got != want+"\n" || !json.Valid([]byte(got)) {
		t.Fatalf("got %s\nwant %s", got, want)
	}
}

func TestFieldFailuresAndLifecycle(t *testing.T) {
	var out bytes.Buffer
	var reports []error
	l := newLogger(t, &out, WithTimestamp(false), WithErrorHandler(func(e error) { reports = append(reports, e) }))
	cause := errors.New("private value")
	calls := 0
	m := marshalFunc(func() ([]byte, error) { calls++; return nil, cause })
	ctx := l.With().Any("broken", m).Str("shared", "ok")
	child, err := ctx.Logger()
	if !errors.Is(err, ErrFieldEncoding) || !errors.Is(err, cause) || strings.Contains(err.Error(), cause.Error()) || child == nil || calls != 1 {
		t.Fatal(child, err, calls)
	}
	if _, err := ctx.Logger(); err != ErrEventConsumed || calls != 1 {
		t.Fatal(err, calls)
	}
	cycle := map[string]any{}
	cycle["cycle"] = cycle
	var typedNil *panicError
	ev := child.Info().Any("cycle", cycle).Any("panic", marshalFunc(func() ([]byte, error) { panic("secret") })).
		Any("invalid", json.RawMessage("!")).Float64("nan", math.NaN()).Err(typedNil)
	ev.Send()
	if len(reports) != 1 || !errors.Is(reports[0], ErrFieldEncoding) || strings.Contains(reports[0].Error(), "secret") {
		t.Fatal(reports)
	}
	want := `{"level":"info","broken":null,"shared":"ok","cycle":null,"panic":null,"invalid":null,"nan":null,"error":null}` + "\n"
	if out.String() != want {
		t.Fatal(out.String())
	}
	ev.Msg("second")
	if len(reports) != 2 || reports[1] != ErrEventConsumed || out.String() != want {
		t.Fatal(reports, out.String())
	}
	short := newLogger(t, writerFunc(func(p []byte) (int, error) { return len(p) - 1, nil }), WithErrorHandler(func(error) { t.Fatal("unexpected report") }))
	if err := short.Info().Any("bad", m).SendErr(); !errors.Is(err, io.ErrShortWrite) || !errors.Is(err, cause) || !errors.Is(err, ErrFieldEncoding) {
		t.Fatal(err)
	}
}

func TestLevelValidationAndDisabled(t *testing.T) {
	for _, level := range []Level{TraceLevel, DebugLevel, InfoLevel, WarnLevel, ErrorLevel, FatalLevel, DisabledLevel} {
		if got, err := ParseLevel(strings.ToUpper(level.String())); got != level || err != nil {
			t.Fatal(got, err)
		}
	}
	for _, opts := range [][]Option{{WithLevel(1)}, {WithFormat(2)}, {WithColor(true)}, {WithTimeFunc(nil)}, {WithErrorHandler(nil)}, {nil}} {
		if _, err := New(io.Discard, opts...); !errors.Is(err, ErrInvalidConfig) {
			t.Fatal(err)
		}
	}
	l := newLogger(t, writerFunc(func([]byte) (int, error) { t.Fatal("disabled write"); return 0, nil }), WithLevel(DisabledLevel),
		WithTimeFunc(func() time.Time { t.Fatal("disabled clock"); return time.Time{} }),
		WithErrorHandler(func(error) { t.Fatal("disabled handler") }))
	ev := l.Info().Any("x", marshalFunc(func() ([]byte, error) { t.Fatal("disabled encoding"); return nil, nil })).Bytes("data", []byte{1})
	if ev.fields != nil || ev.MsgErr("") != nil || ev.SendErr() != ErrEventConsumed {
		t.Fatal("disabled lifecycle")
	}
	if _, err := l.WithLevel(1); !errors.Is(err, ErrInvalidConfig) {
		t.Fatal(err)
	}
}

func TestConsole(t *testing.T) {
	var out bytes.Buffer
	l := newLogger(t, &out, WithTimestamp(false), WithFormat(ConsoleFormat), WithColor(true), WithLevel(TraceLevel))
	l.Trace().Str("http.method", "GET").Str("space key", "two words").Str("empty", "").Any("object", map[string]string{"x": "a b"}).Msg("x\ny")
	want := "\x1b[90mTRACE\x1b[0m \"x\\ny\" \"http\\\\.method\"=GET \"space key\"=\"two words\" empty=\"\" object=\"{\\\"x\\\":\\\"a b\\\"}\"\n"
	if out.String() != want {
		t.Fatalf("got %q\nwant %q", out.String(), want)
	}
}

type logValueFunc func() slog.Value

func (f logValueFunc) LogValue() slog.Value { return f() }

func TestSlogGroupsAndLevels(t *testing.T) {
	var out bytes.Buffer
	var reports []error
	l := newLogger(t, &out, WithTimestamp(false), WithErrorHandler(func(e error) { reports = append(reports, e) }))
	ctx := context.Background()
	root := NewSlogHandler(l)
	if err := NewSlogHandler(nil).Handle(ctx, slog.Record{}); err != ErrNilLogger {
		t.Fatal(err)
	}
	h := root.WithAttrs([]slog.Attr{slog.String("http", "scalar"), slog.String("level", "user")}).
		WithGroup("http").WithAttrs([]slog.Attr{slog.String("method", "GET")}).
		WithGroup("request").WithGroup("")
	r := slog.NewRecord(time.Time{}, slog.Level(2), "hello", 0)
	r.AddAttrs(slog.Int("id", 7), slog.Group("", slog.String("time", "nested")), slog.Group("empty"),
		slog.Any("bad", logValueFunc(func() slog.Value { panic("secret") })), slog.Any("ordinary", errors.New("ordinary")))
	if err := h.Handle(ctx, r); !errors.Is(err, ErrFieldEncoding) || len(reports) != 1 {
		t.Fatal(err, reports)
	}
	want := `{"level":"info+2","message":"hello","http":{"method":"GET","request":{"id":7,"time":"nested","bad":null,"ordinary":{}}},"fields":{"level":"user"}}` + "\n"
	if out.String() != want {
		t.Fatal(out.String())
	}
	for level := -12; level <= 16; level++ {
		if h.Enabled(ctx, slog.Level(level)) != (level >= 0) {
			t.Fatal(level)
		}
	}
	out.Reset()
	if err := root.Handle(ctx, slog.NewRecord(time.Time{}, slog.LevelDebug, "off", 0)); err != nil || out.Len() != 0 {
		t.Fatal(err, out.String())
	}
	if err := root.Handle(ctx, slog.NewRecord(time.Time{}, 12, "high", 0)); err != nil || !strings.Contains(out.String(), `"level":"error+4"`) {
		t.Fatal(err, out.String())
	}
}

func TestSharedWriterConcurrent(t *testing.T) {
	var out bytes.Buffer
	l := newLogger(t, &out, WithTimestamp(false))
	child, err := l.With().Str("source", "child").Logger()
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				l.Info().Int("id", j).Send()
				child.Info().Int("id", j).Send()
			}
		}()
	}
	wg.Wait()
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != 1200 {
		t.Fatal(len(lines))
	}
	for _, line := range lines {
		if !json.Valid([]byte(line)) {
			t.Fatal(line)
		}
	}
}
