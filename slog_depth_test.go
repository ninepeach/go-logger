package logger

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"
)

func nestedGroup(depth int) slog.Attr {
	a := slog.String("leaf", "ok")
	for i := 0; i < depth; i++ {
		a = slog.Group("g", a)
	}
	return a
}

func TestSlogGroupDepth(t *testing.T) {
	for _, tc := range []struct {
		name      string
		depth     int
		wantError bool
	}{
		{"limit", 64, false},
		{"over_limit", 65, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			l := newLogger(t, &out, WithTimestamp(false))
			h := NewSlogHandler(l)

			r := slog.NewRecord(time.Time{}, slog.LevelInfo, "test", 0)
			r.AddAttrs(nestedGroup(tc.depth))

			err := h.Handle(context.Background(), r)
			if got := errors.Is(err, ErrFieldEncoding); got != tc.wantError {
				t.Fatalf("depth=%d: err=%v", tc.depth, err)
			}
			if !json.Valid(out.Bytes()) {
				t.Fatalf("invalid JSON: %s", out.String())
			}
		})
	}
}

func TestSlogWithAttrsDepth(t *testing.T) {
	for _, tc := range []struct {
		depth     int
		wantError bool
	}{
		{64, false},
		{65, true},
	} {
		var out bytes.Buffer
		l := newLogger(t, &out, WithTimestamp(false))
		h := NewSlogHandler(l).WithAttrs(
			[]slog.Attr{nestedGroup(tc.depth)},
		)

		r := slog.NewRecord(time.Time{}, slog.LevelInfo, "test", 0)
		err := h.Handle(context.Background(), r)

		if got := errors.Is(err, ErrFieldEncoding); got != tc.wantError {
			t.Fatalf("depth=%d: err=%v", tc.depth, err)
		}
		if !json.Valid(out.Bytes()) {
			t.Fatalf("depth=%d: invalid JSON: %s", tc.depth, out.String())
		}
	}
}

func TestSlogWithAttrsGroupSnapshot(t *testing.T) {
	var out bytes.Buffer
	l := newLogger(t, &out, WithTimestamp(false))

	nested := []slog.Attr{slog.String("value", "original")}
	attrs := []slog.Attr{
		slog.Attr{
			Key:   "group",
			Value: slog.GroupValue(nested...),
		},
	}

	h := NewSlogHandler(l).WithAttrs(attrs)

	nested[0] = slog.String("value", "modified")

	r := slog.NewRecord(time.Time{}, slog.LevelInfo, "test", 0)
	if err := h.Handle(context.Background(), r); err != nil {
		t.Fatal(err)
	}

	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}

	group, ok := got["group"].(map[string]any)
	if !ok || group["value"] != "original" {
		t.Fatalf("snapshot changed: %s", out.String())
	}
}
