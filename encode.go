package logger

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type field struct {
	key   string
	value any
}

type anyValue struct{ value any }
type errorValue struct{ err error }

type value struct {
	json    string
	console string
}

func utf8String(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	var out strings.Builder
	for _, r := range s {
		out.WriteRune(r)
	}
	return out.String()
}

func jsonString(s string) string {
	return string(appendJSONString(nil, s))
}

func stringValue(s string) value {
	s = utf8String(s)
	return value{jsonString(s), consoleToken(s)}
}

func consoleToken(s string) string {
	if s == "" {
		return `""`
	}
	for _, b := range []byte(s) {
		if !(b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || strings.ContainsRune("._/@:+-", rune(b))) {
			return strconv.Quote(s)
		}
	}
	return s
}

func snapshot(input any, utc bool) (out value, err error) {
	out = value{"null", "null"}
	defer func() {
		if recover() != nil {
			err = errors.New("field method panicked")
		}
	}()
	var number string
	switch v := input.(type) {
	case value:
		return v, nil
	case nil:
		return out, nil
	case string:
		return stringValue(v), nil
	case bool:
		number = strconv.FormatBool(v)
	case int64:
		number = strconv.FormatInt(v, 10)
	case uint64:
		number = strconv.FormatUint(v, 10)
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return out, errors.New("non-finite float")
		}
		number = strconv.FormatFloat(v, 'g', -1, 64)
	case time.Time:
		if utc {
			v = v.UTC()
		}
		s := v.Format(time.RFC3339Nano)
		return value{jsonString(s), s}, nil
	case time.Duration:
		return stringValue(v.String()), nil
	case []byte:
		s := base64.StdEncoding.EncodeToString(v)
		return value{jsonString(s), consoleToken(s)}, nil
	case errorValue:
		return stringValue(v.err.Error()), nil
	case anyValue:
		b, marshalErr := json.Marshal(v.value)
		if marshalErr != nil {
			return out, marshalFailure{marshalErr}
		}
		// A custom marshaler can supply invalid UTF-8 in a quoted token.
		s := utf8String(string(b))
		switch s[0] {
		case '"':
			var decoded string
			if err := json.Unmarshal([]byte(s), &decoded); err != nil {
				return out, errors.New("invalid JSON string")
			}
			return value{s, consoleToken(decoded)}, nil
		case '{', '[':
			return value{s, compositeToken(s)}, nil
		default:
			return value{s, s}, nil
		}
	default:
		return out, errors.New("unsupported internal field type")
	}
	return value{number, number}, nil
}

func compositeToken(s string) string {
	for _, r := range s {
		if r == ' ' || r < ' ' || r == 0x7f {
			return strconv.Quote(s)
		}
	}
	return s
}

type marshalFailure struct{ cause error }

func (e marshalFailure) Error() string { return "JSON encoding failed" }
func (e marshalFailure) Unwrap() error { return e.cause }

func fieldError(path []string, err error) error {
	return fmt.Errorf("%w: field %q: %w", ErrFieldEncoding, path, err)
}

type node struct {
	key   string
	value value
	group *object
}

type object struct {
	entries []node
}

func (o *object) set(n node) {
	for i := range o.entries {
		if o.entries[i].key == n.key {
			o.entries[i] = n
			return
		}
	}
	o.entries = append(o.entries, n)
}

func (o *object) descend(path []string) *object {
	for _, key := range path {
		found := false
		for i := range o.entries {
			if o.entries[i].key == key && o.entries[i].group != nil {
				o = o.entries[i].group
				found = true
				break
			}
		}
		if found {
			continue
		}
		child := &object{}
		o.set(node{key: key, group: child})
		o = child
	}
	return o
}

func (o *object) relocate() {
	var root, conflicts object
	var hasTime, hasLevel, hasMessage, hasFields bool
	for i := range o.entries {
		switch o.entries[i].key {
		case "time":
			hasTime = true
		case "level":
			hasLevel = true
		case "message":
			hasMessage = true
		case "fields":
			hasFields = true
		}
	}
	if !hasTime && !hasLevel && !hasMessage && !hasFields {
		return
	}
	for _, n := range o.entries {
		switch n.key {
		case "time", "level", "message", "fields":
			if len(conflicts.entries) == 0 {
				root.set(node{key: "fields", group: &conflicts})
			}
			conflicts.set(n)
		default:
			root.set(n)
		}
	}
	*o = root
}

type record struct {
	time    time.Time
	level   int
	name    string
	message *string
	slog    bool
}

type levelWriter interface {
	writeRecord([]byte, record) (int, error)
}

func checkedWrite(n, length int, err error) error {
	if n != length && err == nil {
		return io.ErrShortWrite
	}
	return err
}

func (s *sink) write(p []byte, r record) error {
	// The built-in writers serialize calls themselves. In particular, a
	// rotating writer can report maintenance errors after releasing its mutex.
	switch w := s.writer.(type) {
	case levelWriter:
		n, err := w.writeRecord(p, r)
		return checkedWrite(n, len(p), err)
	case *RotatingWriter:
		n, err := w.Write(p)
		return checkedWrite(n, len(p), err)
	default:
		s.Lock()
		defer s.Unlock()
		n, err := w.Write(p)
		return checkedWrite(n, len(p), err)
	}
}

func (l *Logger) submit(r record, fields []field, extra func(*object) error) error {
	if r.time.IsZero() {
		r.time = l.config.now()
	}
	if l.config.utc {
		r.time = r.time.UTC()
	}
	root := object{entries: make([]node, 0, len(l.fields)+len(fields))}
	var failures []error
	add := func(fields []field) {
		for _, f := range fields {
			key := utf8String(f.key)
			v, err := snapshot(f.value, l.config.utc)
			if err != nil {
				failures = append(failures, fieldError([]string{key}, err))
			}
			root.set(node{key: key, value: v})
		}
	}
	add(l.fields)
	add(fields)
	if extra != nil {
		failures = append(failures, extra(&root))
	}
	root.relocate()
	p := encodeRecord(r, l.config, &root)
	if err := l.sink.write(p, r); err != nil {
		failures = append(failures, fmt.Errorf("logger: write record: %w", err))
	}
	return errors.Join(failures...)
}

func encodeRecord(r record, c config, root *object) []byte {
	out := make([]byte, 0, 128)
	if c.format == JSONFormat {
		out = append(out, '{')
		if c.timestamp {
			out = append(out, `"time":`...)
			out = appendJSONString(out, r.time.Format(time.RFC3339Nano))
			out = append(out, ',')
		}
		out = append(out, `"level":`...)
		out = appendJSONString(out, r.name)
		if r.message != nil {
			out = append(out, `,"message":`...)
			out = appendJSONString(out, *r.message)
		}
		out = appendJSON(out, root, true)
		out = append(out, '}')
	} else {
		if c.timestamp {
			out = r.time.AppendFormat(out, time.RFC3339Nano)
			out = append(out, ' ')
		}
		if c.color {
			color := "31"
			switch {
			case r.level <= int(TraceLevel):
				color = "90"
			case r.level < int(InfoLevel):
				color = "36"
			case r.level < int(WarnLevel):
				color = "32"
			case r.level < int(ErrorLevel):
				color = "33"
			case !r.slog && r.level == int(FatalLevel):
				color = "91"
			}
			out = append(out, "\x1b["...)
			out = append(out, color...)
			out = append(out, 'm')
		}
		out = append(out, strings.ToUpper(r.name)...)
		if c.color {
			out = append(out, "\x1b[0m"...)
		}
		if r.message != nil {
			out = append(out, ' ')
			out = append(out, consoleToken(utf8String(*r.message))...)
		}
		out = appendConsole(out, root, "")
	}
	return append(out, '\n')
}

func appendJSON(out []byte, o *object, comma bool) []byte {
	for _, n := range o.entries {
		if comma {
			out = append(out, ',')
		}
		comma = true
		out = appendJSONString(out, n.key)
		out = append(out, ':')
		if n.group == nil {
			out = append(out, n.value.json...)
			continue
		}
		out = append(out, '{')
		out = appendJSON(out, n.group, false)
		out = append(out, '}')
	}
	return out
}

// appendJSONString matches encoding/json's string escaping, including its
// HTML-safe escapes and replacement of invalid UTF-8 with U+FFFD.
func appendJSONString(out []byte, s string) []byte {
	const hex = "0123456789abcdef"
	out = append(out, '"')
	start := 0
	for i := 0; i < len(s); {
		if b := s[i]; b < utf8.RuneSelf {
			if b >= 0x20 && b != '\\' && b != '"' && b != '<' && b != '>' && b != '&' {
				i++
				continue
			}
			out = append(out, s[start:i]...)
			switch b {
			case '\\', '"':
				out = append(out, '\\', b)
			case '\b':
				out = append(out, '\\', 'b')
			case '\f':
				out = append(out, '\\', 'f')
			case '\n':
				out = append(out, '\\', 'n')
			case '\r':
				out = append(out, '\\', 'r')
			case '\t':
				out = append(out, '\\', 't')
			default:
				out = append(out, '\\', 'u', '0', '0', hex[b>>4], hex[b&0xf])
			}
			i++
			start = i
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			out = append(out, s[start:i]...)
			out = append(out, "\\ufffd"...)
			i++
			start = i
			continue
		}
		if r == '\u2028' || r == '\u2029' {
			out = append(out, s[start:i]...)
			out = append(out, '\\', 'u', '2', '0', '2', hex[r&0xf])
			i += size
			start = i
			continue
		}
		i += size
	}
	out = append(out, s[start:]...)
	return append(out, '"')
}

var pathEscaper = strings.NewReplacer(`\`, `\\`, ".", `\.`)

func appendConsole(out []byte, o *object, prefix string) []byte {
	for _, n := range o.entries {
		key := prefix + pathEscaper.Replace(n.key)
		if n.group != nil {
			out = appendConsole(out, n.group, key+".")
			continue
		}
		out = append(out, ' ')
		out = append(out, consoleToken(key)...)
		out = append(out, '=')
		out = append(out, n.value.console...)
	}
	return out
}
