package logger

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"
)

func BenchmarkDisabled(b *testing.B) {
	l := newLogger(b, io.Discard, WithLevel(DisabledLevel))
	b.Run("Chain", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			l.Info().Str("service", "gateway").Msg("started")
		}
	})
	b.Run("MsgErr", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if err := l.Info().MsgErr("started"); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkRecord(b *testing.B) {
	for _, workload := range []struct {
		name     string
		format   Format
		fields   bool
		any      bool
		parallel bool
	}{
		{"JSON", JSONFormat, false, false, false},
		{"Console", ConsoleFormat, false, false, false},
		{"Primitives", JSONFormat, true, false, false},
		{"Any", JSONFormat, true, true, false},
		{"Parallel", JSONFormat, true, false, true},
	} {
		b.Run(workload.name, func(b *testing.B) {
			l := newLogger(b, io.Discard, WithFormat(workload.format))
			payload := map[string]int{"connections": 100}
			write := func() {
				e := l.Info()
				if workload.fields {
					e.Str("service", "gateway").Int("connections", 100).Bool("ready", true)
				}
				if workload.any {
					e.Any("payload", payload)
				}
				e.Msg("started")
			}
			b.ReportAllocs()
			b.ResetTimer()
			if workload.parallel {
				b.RunParallel(func(pb *testing.PB) {
					for pb.Next() {
						write()
					}
				})
				return
			}
			for i := 0; i < b.N; i++ {
				write()
			}
		})
	}
}

func BenchmarkRotatingWriter(b *testing.B) {
	for _, tc := range []struct {
		name  string
		limit int64
	}{{"Append", 0}, {"Rotate", 64}} {
		b.Run(tc.name, func(b *testing.B) {
			w, err := NewRotatingWriter(FileConfig{Filename: filepath.Join(b.TempDir(), "app"), MaxSize: tc.limit, MaxBackups: 1})
			if err != nil {
				b.Fatal(err)
			}
			defer w.Close()
			p := []byte("one complete log record\n")
			b.ReportAllocs()
			b.SetBytes(int64(len(p)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := w.Write(p); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkSlog(b *testing.B) {
	l := newLogger(b, io.Discard)
	sl := slog.New(NewSlogHandler(l)).With("service", "gateway")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sl.Info("started", "connections", 100, "ready", true)
	}
}
