package xslog

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func ExampleNewSpaceHandler() {
	log := slog.New(NewSpaceHandler(os.Stdout, &slog.HandlerOptions{
		AddSource: true,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			switch a.Key {
			case slog.TimeKey: // make the example reproducible
				return slog.Time(a.Key, exampleTime)
			case slog.SourceKey: // keep the example output short
				src := a.Value.Any().(*slog.Source)
				src.File = filepath.Base(src.File)
				return a
			}
			return a
		},
	}, ""))
	log = log.With("service", "acme").WithGroup("demo")
	log.Info("the message is always quoted", "foo", []int{4711, 815})

	// Output:
	// (2026-09-04T15:45:52.623+2 INFO ~space_test.go:30 "the message is always quoted"
	//   ~service acme
	//   ~demo {
	//     ~foo [4711 815]})
}

// spaced is [logged] for the spaced Handler variant.
func spaced(t *testing.T, indent string, log func(*slog.Logger)) string {
	t.Helper()
	var buf bytes.Buffer
	log(slog.New(NewSpaceHandler(&buf, &slog.HandlerOptions{
		ReplaceAttr: func(gs []string, a slog.Attr) slog.Attr {
			if len(gs) == 0 && a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	}, indent)))
	return strings.TrimSuffix(buf.String(), "\n")
}

func TestSpaceHandler(t *testing.T) {
	for _, c := range []struct {
		name   string
		indent string
		log    func(*slog.Logger)
		want   string
	}{
		{
			name: "no attrs stays on one line",
			log:  func(l *slog.Logger) { l.Info("hi") },
			want: `(~ INFO "hi")`,
		},
		{
			name: "attrs",
			log:  func(l *slog.Logger) { l.Info("hi", "a", 1, "b", "x y") },
			want: "(~ INFO \"hi\"\n  ~a 1\n  ~b \"x y\")",
		},
		{
			name: "group values stay on one line",
			log: func(l *slog.Logger) {
				l.Info("hi", "a", []int{1, 2}, "b", map[string]int{"k": 3})
			},
			want: "(~ INFO \"hi\"\n  ~a [1 2]\n  ~b {~k 3})",
		},
		{
			name: "with attrs and groups",
			log: func(l *slog.Logger) {
				l.With("a", 1).WithGroup("g").With("b", 2).Info("k", "c", 3)
			},
			want: "(~ INFO \"k\"\n  ~a 1\n  ~g {\n    ~b 2\n    ~c 3})",
		},
		{
			name: "nested group attrs",
			log: func(l *slog.Logger) {
				l.WithGroup("g").Info("k", slog.Group("h", "a", 1), "b", 2)
			},
			want: "(~ INFO \"k\"\n  ~g {\n    ~h {\n      ~a 1}\n    ~b 2})",
		},
		{
			name: "empty groups are dropped",
			log: func(l *slog.Logger) {
				l.With("a", 1).WithGroup("g").WithGroup("h").Info("k")
			},
			want: "(~ INFO \"k\"\n  ~a 1)",
		},
		{
			name:   "custom indent",
			indent: "\t",
			log: func(l *slog.Logger) {
				l.WithGroup("g").Info("k", "a", 1)
			},
			want: "(~ INFO \"k\"\n\t~g {\n\t\t~a 1})",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := spaced(t, c.indent, c.log); got != c.want {
				t.Errorf("\nhave:\n%s\nwant:\n%s", got, c.want)
			}
		})
	}
}

// TestSpaceHandlerParses checks that the spaced records are still valid XSX
// with the very same content as the compact ones.
func TestSpaceHandlerParses(t *testing.T) {
	log := func(l *slog.Logger) {
		l.With("a", 1).WithGroup("g").With("b", 2).
			Info("k", "c", []int{3, 4}, slog.Group("h", "d", "x y"))
	}
	sp, err := parseRecords(spaced(t, "", log))
	if err != nil {
		t.Fatal(err)
	}
	cp, err := parseRecords(logged(t, nil, log))
	if err != nil {
		t.Fatal(err)
	}
	if want, got := fmtMap(cp[0]), fmtMap(sp[0]); want != got {
		t.Errorf("\nhave: %s\nwant: %s", got, want)
	}
}

func fmtMap(m map[string]any) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var sb strings.Builder
	for _, k := range keys {
		if sb.Len() > 0 {
			sb.WriteString(" ")
		}
		if g, ok := m[k].(map[string]any); ok {
			fmt.Fprintf(&sb, "%s{%s}", k, fmtMap(g))
		} else {
			fmt.Fprintf(&sb, "%s=%v", k, m[k])
		}
	}
	return sb.String()
}

func BenchmarkSpaceHandler(b *testing.B) {
	log := slog.New(NewSpaceHandler(nopWriter{}, nil, "")).
		With("service", "acme").
		WithGroup("demo")
	for b.Loop() {
		log.Info("the message", "foo", 4711, "bar", "baz")
	}
}
