package logger

import (
        "fmt"
	"context"
	"errors"
	"log/slog"
	"strings"
)

const maxSlogGroupDepth = 64

type slogHandler struct {
	logger *Logger
	groups []string
	attrs  []scopedAttrs
	err    error
}

type scopedAttrs struct {
	path  []string
	attrs []slog.Attr
}

// NewSlogHandler adapts a Logger for use with log/slog.
func NewSlogHandler(l *Logger) slog.Handler { return &slogHandler{logger: l} }

func (h *slogHandler) Enabled(_ context.Context, level slog.Level) bool {
	return h.logger.enabled(int(level))
}

func (h *slogHandler) Handle(ctx context.Context, r slog.Record) error {
	if h.logger == nil {
		return ErrNilLogger
	}
	if !h.Enabled(ctx, r.Level) {
		return nil
	}
	name := strings.ToLower(r.Level.String())
	if r.Level == slog.Level(TraceLevel) {
		name = "trace"
	}
	err := h.logger.submit(record{time: r.Time, level: int(r.Level), name: name, message: &r.Message, slog: true}, nil, func(root *object) error {
		var failures []error
		for _, entry := range h.attrs {
			h.apply(root, entry.path, entry.attrs, 0, &failures)
		}
		attrs := make([]slog.Attr, 0, r.NumAttrs())
		r.Attrs(func(a slog.Attr) bool { attrs = append(attrs, a); return true })
		h.apply(root, h.groups, attrs, 0, &failures)
return errors.Join(
    h.err,
    errors.Join(failures...),
)
	})
	if err != nil {
		h.logger.config.report(err)
	}
	return err
}

func (h *slogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	derived := *h

	cloned, err := cloneAttrs(attrs, 0)
	derived.err = errors.Join(h.err, err)

	derived.attrs = append(
		append([]scopedAttrs(nil), h.attrs...),
		scopedAttrs{
			path:  append([]string(nil), h.groups...),
			attrs: cloned,
		},
	)

	return &derived
}

func cloneAttrs(attrs []slog.Attr, depth int) ([]slog.Attr, error) {
	result := append([]slog.Attr(nil), attrs...)

	var failures []error

	for i := range result {
		if result[i].Value.Kind() != slog.KindGroup {
			continue
		}

		if depth >= maxSlogGroupDepth {
			failures = append(
				failures,
				fmt.Errorf(
					"%w: slog group depth exceeds %d",
					ErrFieldEncoding,
					maxSlogGroupDepth,
				),
			)
			result[i].Value = slog.GroupValue()
			continue
		}

		nested, err := cloneAttrs(
			result[i].Value.Group(),
			depth+1,
		)

		if err != nil {
			failures = append(failures, err)
		}

		result[i].Value = slog.GroupValue(nested...)
	}

	return result, errors.Join(failures...)
}

func (h *slogHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	derived := *h
	derived.groups = append(append([]string(nil), h.groups...), utf8String(name))
	return &derived
}

func appendPath(path []string, key string) []string {
	return append(append([]string(nil), path...), key)
}

func (h *slogHandler) apply(root *object, path []string, attrs []slog.Attr, depth int, failures *[]error) {
	for _, a := range attrs {
		if a.Key == "" && a.Value.Kind() == slog.KindAny && a.Value.Any() == nil {
			continue
		}
		key := utf8String(a.Key)
		resolved, err := resolve(a.Value)
		if err == nil && resolved.Kind() == slog.KindGroup {
			if depth >= maxSlogGroupDepth {
				*failures = append(
					*failures,
					fieldError(
						appendPath(path, key),
						ErrFieldEncoding,
					),
				)
				continue
			}

			groupPath := path
			if key != "" {
				groupPath = appendPath(path, key)
			}

			h.apply(
				root,
				groupPath,
				resolved.Group(),
				depth+1,
				failures,
			)
			continue
		}
		v := value{"null", "null"}
		if err == nil {
			var input any
			switch resolved.Kind() {
			case slog.KindAny:
				input = anyValue{resolved.Any()}
			default:
				input = resolved.Any()
			}
			v, err = snapshot(input, h.logger.config.utc)
		}
		if err != nil {
			*failures = append(*failures, fieldError(appendPath(path, key), err))
		}
		root.descend(path).set(node{key: key, value: v})
	}
}

func resolve(v slog.Value) (out slog.Value, err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("LogValue panicked")
		}
	}()
	for i := 0; v.Kind() == slog.KindLogValuer; i++ {
		if i == 100 {
			return slog.Value{}, errors.New("LogValue resolution limit exceeded")
		}
		v = v.LogValuer().LogValue()
	}
	return v, nil
}
