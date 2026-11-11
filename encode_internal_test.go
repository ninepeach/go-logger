package logger

import (
	"encoding/json"
	"testing"
)

func TestAppendJSONStringMatchesEncodingJSON(t *testing.T) {
	for _, s := range []string{
		"",
		"plain ASCII",
		"quote \" slash \\ controls\n\r\t\b\f",
		"<script>&",
		"中文 🚀",
		"line\u2028paragraph\u2029",
		"invalid \xff utf8 \xc0\xaf",
	} {
		want, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		got := appendJSONString(nil, s)
		if string(got) != string(want) {
			t.Fatalf("appendJSONString(%q) = %q, want %q", s, got, want)
		}
	}
}
