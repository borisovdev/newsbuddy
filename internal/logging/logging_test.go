package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestNew_JSONHandler_EveryLineIsValidJSON(t *testing.T) {
	tests := []struct {
		name string
		log  func(l *slog.Logger)
	}{
		{
			name: "simple info line",
			log: func(l *slog.Logger) {
				l.Info(EvStartup, "version", "dev")
			},
		},
		{
			name: "duration_ms as int64",
			log: func(l *slog.Logger) {
				l.Info(EvCycleEnd, slog.Int64("duration_ms", int64(1234)))
			},
		},
		{
			name: "multiple lines",
			log: func(l *slog.Logger) {
				l.Info(EvSourceAdded, "source_id", "abc")
				l.Warn(EvSourceRemovedMid, "source_id", "abc")
				l.Error(EvPostFailed, "error", "boom")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			l := New("json", slog.LevelInfo, &buf)
			tt.log(l)

			lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
			for _, line := range lines {
				if line == "" {
					continue
				}
				var m map[string]any
				if err := json.Unmarshal([]byte(line), &m); err != nil {
					t.Fatalf("line %q is not valid JSON: %v", line, err)
				}
			}
		})
	}
}

func TestNew_JSON_DurationMsIsNumber(t *testing.T) {
	var buf bytes.Buffer
	l := New("json", slog.LevelInfo, &buf)
	l.Info(EvCycleEnd, slog.Int64("duration_ms", int64(4200)))

	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}

	v, ok := m["duration_ms"]
	if !ok {
		t.Fatalf("duration_ms not present in log output: %v", m)
	}
	if _, ok := v.(float64); !ok {
		t.Fatalf("duration_ms = %v (%T), want JSON number", v, v)
	}
}

func TestNew_JSON_ServiceAndVersionAttrs(t *testing.T) {
	var buf bytes.Buffer
	l := New("json", slog.LevelInfo, &buf)
	l.Info(EvStartup)

	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if m["service"] != "news-buddy" {
		t.Errorf("service = %v, want news-buddy", m["service"])
	}
	if m["version"] != Version {
		t.Errorf("version = %v, want %v", m["version"], Version)
	}
	if m["msg"] != EvStartup {
		t.Errorf("msg = %v, want %v", m["msg"], EvStartup)
	}
}

func TestNew_TextFormat_Smoke(t *testing.T) {
	var buf bytes.Buffer
	l := New("text", slog.LevelInfo, &buf)
	l.Info(EvStartup, "version", "dev")

	out := buf.String()
	if out == "" {
		t.Fatal("expected non-empty text output")
	}
	// Text output must not be valid JSON (sanity check we picked the text handler).
	var m map[string]any
	if err := json.Unmarshal([]byte(strings.TrimRight(out, "\n")), &m); err == nil {
		t.Fatalf("text handler output parsed as JSON, expected key=value text: %q", out)
	}
	if !strings.Contains(out, "msg="+EvStartup) && !strings.Contains(out, `msg="`+EvStartup+`"`) {
		t.Errorf("text output missing expected msg: %q", out)
	}
	if !strings.Contains(out, "service=news-buddy") {
		t.Errorf("text output missing service attr: %q", out)
	}
}

func TestNew_LevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	l := New("json", slog.LevelInfo, &buf)

	l.Debug(EvSourceStart, "source_id", "abc")
	if buf.Len() != 0 {
		t.Fatalf("expected debug line to be suppressed at info level, got: %q", buf.String())
	}

	l.Info(EvSourceOK, "source_id", "abc")
	if buf.Len() == 0 {
		t.Fatal("expected info line to be emitted at info level")
	}
}

func TestNew_DefaultsToJSON(t *testing.T) {
	var buf bytes.Buffer
	l := New("", slog.LevelInfo, &buf)
	l.Info(EvStartup)

	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("expected JSON output when format is not \"text\", got: %q (%v)", buf.String(), err)
	}
}
