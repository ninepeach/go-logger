package logger

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	b, _ := json.Marshal(s)
	return string(b)
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
		b, marshalErr := json.Marshal(v)
		if marshalErr != nil {
			return out, errors.New("non-finite float")
		}
		number = string(b)
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
	index   map[string]int
}

func (o *object) set(n node) {
	if o.index == nil {
		o.index = make(map[string]int)
	}
	if i, ok := o.index[n.key]; ok {
		o.entries[i] = n
		return
	}
	o.index[n.key] = len(o.entries)
	o.entries = append(o.entries, n)
}

func (o *object) descend(path []string) *object {
	for _, key := range path {
		if i, ok := o.index[key]; ok && o.entries[i].group != nil {
			o = o.entries[i].group
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
	_, hasTime := o.index["time"]
	_, hasLevel := o.index["level"]
	_, hasMessage := o.index["message"]
	_, hasFields := o.index["fields"]
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
	var root object
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
	var out strings.Builder
	if c.format == JSONFormat {
		out.WriteByte('{')
		if c.timestamp {
			out.WriteString(`"time":`)
			out.WriteString(jsonString(r.time.Format(time.RFC3339Nano)))
			out.WriteByte(',')
		}
		out.WriteString(`"level":`)
		out.WriteString(jsonString(r.name))
		if r.message != nil {
			out.WriteString(`,"message":`)
			out.WriteString(jsonString(*r.message))
		}
		appendJSON(&out, root, true)
		out.WriteByte('}')
	} else {
		if c.timestamp {
			out.WriteString(r.time.Format(time.RFC3339Nano))
			out.WriteByte(' ')
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
			out.WriteString("\x1b[" + color + "m")
		}
		out.WriteString(strings.ToUpper(r.name))
		if c.color {
			out.WriteString("\x1b[0m")
		}
		if r.message != nil {
			out.WriteByte(' ')
			out.WriteString(consoleToken(utf8String(*r.message)))
		}
		appendConsole(&out, root, "")
	}
	out.WriteByte('\n')
	return []byte(out.String())
}

func appendJSON(out *strings.Builder, o *object, comma bool) {
	for _, n := range o.entries {
		if comma {
			out.WriteByte(',')
		}
		comma = true
		out.WriteString(jsonString(n.key))
		out.WriteByte(':')
		if n.group == nil {
			out.WriteString(n.value.json)
			continue
		}
		out.WriteByte('{')
		appendJSON(out, n.group, false)
		out.WriteByte('}')
	}
}

var pathEscaper = strings.NewReplacer(`\`, `\\`, ".", `\.`)

func appendConsole(out *strings.Builder, o *object, prefix string) {
	for _, n := range o.entries {
		key := prefix + pathEscaper.Replace(n.key)
		if n.group != nil {
			appendConsole(out, n.group, key+".")
			continue
		}
		out.WriteByte(' ')
		out.WriteString(consoleToken(key))
		out.WriteByte('=')
		out.WriteString(n.value.console)
	}
}
