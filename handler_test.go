package xslog

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// exampleTime pins the record time so that the example output is
// reproducible.
var exampleTime = time.Date(2026, 9, 4, 15, 45, 52, 623000000,
	time.FixedZone("CEST", 2*3600),
)

func ExampleHandler() {
	log := slog.New(NewHandler(os.Stdout, &slog.HandlerOptions{
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
	}))
	log = log.With("service", "acme").WithGroup("demo")
	log.Info("the message is always quoted", "foo", []int{4711, 815})

	// Output:
	// (2026-09-04T15:45:52.623+2 INFO ~handler_test.go:36 "the message is always quoted" ~service acme ~demo {~foo [4711 815]})
}

// logged returns the record written by log for later comparison. The record
// time is dropped, records without attributes are stripped down to their
// message.
func logged(t *testing.T, opts *slog.HandlerOptions, log func(*slog.Logger)) string {
	t.Helper()
	var buf bytes.Buffer
	if opts == nil {
		opts = new(slog.HandlerOptions)
	}
	rep := opts.ReplaceAttr
	o := *opts
	o.ReplaceAttr = func(gs []string, a slog.Attr) slog.Attr {
		if rep != nil {
			a = rep(gs, a)
		}
		if len(gs) == 0 && a.Key == slog.TimeKey {
			return slog.Attr{}
		}
		return a
	}
	log(slog.New(NewHandler(&buf, &o)))
	return strings.TrimSuffix(buf.String(), "\n")
}

func TestHandler(t *testing.T) {
	for _, c := range []struct {
		name string
		opts *slog.HandlerOptions
		log  func(*slog.Logger)
		want string
	}{
		{
			name: "plain message",
			log:  func(l *slog.Logger) { l.Info("hi") },
			want: `(~ INFO "hi")`,
		},
		{
			name: "message needs quoting",
			log:  func(l *slog.Logger) { l.Warn(`say "hi"`) },
			want: `(~ WARN "say ""hi""")`,
		},
		{
			name: "custom level",
			opts: &slog.HandlerOptions{Level: slog.LevelDebug},
			log:  func(l *slog.Logger) { l.Log(context.TODO(), slog.LevelWarn+3, "x") },
			want: `(~ WARN+3 "x")`,
		},
		{
			name: "value kinds",
			log: func(l *slog.Logger) {
				l.Info("k",
					"str", "sym",
					"quoted", "two words",
					"int", -7,
					"uint", uint64(7),
					"float", 1.5,
					"bool", true,
					"dur", 1500*time.Millisecond,
					"ts", exampleTime,
					"nil", nil,
				)
			},
			want: `(~ INFO "k" ~str sym ~quoted "two words" ~int -7 ~uint 7` +
				` ~float 1.5 ~bool true ~dur 1.5s ~ts 2026-09-04T15:45:52.623+2 ~nil ~)`,
		},
		{
			name: "key needs quoting",
			log:  func(l *slog.Logger) { l.Info("k", "a key", 1) },
			want: `(~ INFO "k" ~"a key" 1)`,
		},
		{
			name: "slice and map values",
			log: func(l *slog.Logger) {
				l.Info("k",
					"ints", []int{4711, 815},
					"strs", [2]string{"a", "b c"},
					"map", map[string]int{"b": 2, "a": 1},
					"nested", [][]int{{1}, {2, 3}},
				)
			},
			want: `(~ INFO "k" ~ints [4711 815] ~strs [a "b c"]` +
				` ~map {~a 1 ~b 2} ~nested [[1] [2 3]])`,
		},
		{
			name: "error value",
			log:  func(l *slog.Logger) { l.Info("k", "err", os.ErrNotExist) },
			want: `(~ INFO "k" ~err "file does not exist")`,
		},
		{
			name: "with attrs",
			log: func(l *slog.Logger) {
				l.With("a", 1).With("b", 2).Info("k", "c", 3)
			},
			want: `(~ INFO "k" ~a 1 ~b 2 ~c 3)`,
		},
		{
			name: "with group",
			log: func(l *slog.Logger) {
				l.With("a", 1).WithGroup("g").With("b", 2).Info("k", "c", 3)
			},
			want: `(~ INFO "k" ~a 1 ~g {~b 2 ~c 3})`,
		},
		{
			name: "nested groups",
			log: func(l *slog.Logger) {
				l.WithGroup("g").WithGroup("h").Info("k", "a", 1)
			},
			want: `(~ INFO "k" ~g {~h {~a 1}})`,
		},
		{
			name: "empty groups are dropped",
			log: func(l *slog.Logger) {
				l.WithGroup("g").WithGroup("h").Info("k")
			},
			want: `(~ INFO "k")`,
		},
		{
			name: "group attr",
			log: func(l *slog.Logger) {
				l.Info("k",
					slog.Group("g", "a", 1, "b", 2),
					slog.Group("empty"),
					"c", 3,
				)
			},
			want: `(~ INFO "k" ~g {~a 1 ~b 2} ~c 3)`,
		},
		{
			name: "group attr inside with-group",
			log: func(l *slog.Logger) {
				l.WithGroup("g").Info("k", slog.Group("h", "a", 1), "b", 2)
			},
			want: `(~ INFO "k" ~g {~h {~a 1} ~b 2})`,
		},
		{
			name: "group attr with empty key is inlined",
			log: func(l *slog.Logger) {
				l.Info("k", slog.Group("", "a", 1), "b", 2)
			},
			want: `(~ INFO "k" ~a 1 ~b 2)`,
		},
		{
			name: "empty attrs are dropped",
			log: func(l *slog.Logger) {
				l.With(slog.Attr{}).Info("k", slog.Attr{}, "a", 1)
			},
			want: `(~ INFO "k" ~a 1)`,
		},
		{
			name: "log valuer is resolved",
			log:  func(l *slog.Logger) { l.Info("k", "v", valuer{}) },
			want: `(~ INFO "k" ~v 42)`,
		},
		{
			name: "replace attr gets group path",
			opts: &slog.HandlerOptions{
				ReplaceAttr: func(gs []string, a slog.Attr) slog.Attr {
					if a.Key == "a" {
						return slog.String("a", strings.Join(gs, "/"))
					}
					return a
				},
			},
			log: func(l *slog.Logger) {
				l.WithGroup("g").WithGroup("h").Info("k", "a", 0)
			},
			want: `(~ INFO "k" ~g {~h {~a g/h}})`,
		},
		{
			name: "replace attr drops attr",
			opts: &slog.HandlerOptions{
				ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
					if a.Key == "drop" {
						return slog.Attr{}
					}
					return a
				},
			},
			log: func(l *slog.Logger) {
				l.WithGroup("g").Info("k", "drop", 1)
			},
			want: `(~ INFO "k")`,
		},
		{
			name: "replace attr drops level and message",
			opts: &slog.HandlerOptions{
				ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
					if a.Key == slog.LevelKey || a.Key == slog.MessageKey {
						return slog.Attr{}
					}
					return a
				},
			},
			log:  func(l *slog.Logger) { l.Info("k") },
			want: `(~ ~ "")`,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := logged(t, c.opts, c.log); got != c.want {
				t.Errorf("\nhave: %s\nwant: %s", got, c.want)
			}
		})
	}
}

type valuer struct{}

func (valuer) LogValue() slog.Value { return slog.IntValue(42) }

func TestHandlerEnabled(t *testing.T) {
	h := NewHandler(nil, &slog.HandlerOptions{Level: slog.LevelWarn})
	for _, c := range []struct {
		l    slog.Level
		want bool
	}{
		{slog.LevelDebug, false},
		{slog.LevelInfo, false},
		{slog.LevelWarn, true},
		{slog.LevelError, true},
	} {
		if got := h.Enabled(context.TODO(), c.l); got != c.want {
			t.Errorf("%s: have %t, want %t", c.l, got, c.want)
		}
	}
	if def := NewHandler(nil, nil); def.Enabled(context.TODO(), slog.LevelDebug) {
		t.Error("debug enabled by default")
	}
}

// TestHandlerReuse checks that neither WithAttrs nor WithGroup change the
// handler they were called on.
func TestHandlerReuse(t *testing.T) {
	var buf bytes.Buffer
	h := NewHandler(&buf, &slog.HandlerOptions{
		ReplaceAttr: func(gs []string, a slog.Attr) slog.Attr {
			if len(gs) == 0 && a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	})
	base := slog.New(h).With("a", 1)
	x := base.WithGroup("g").With("b", 2)
	y := base.With("c", 3)

	x.Info("x")
	y.Info("y")
	base.Info("z")
	x.Info("x2", "d", 4)

	want := `(~ INFO "x" ~a 1 ~g {~b 2})
(~ INFO "y" ~a 1 ~c 3)
(~ INFO "z" ~a 1)
(~ INFO "x2" ~a 1 ~g {~b 2 ~d 4})
`
	if got := buf.String(); got != want {
		t.Errorf("\nhave:\n%s\nwant:\n%s", got, want)
	}
}

func TestHandlerConcurrent(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(NewHandler(&buf, nil)).With("a", 1)
	done := make(chan struct{})
	for i := range 8 {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := range 50 {
				log.WithGroup("g").Info("msg", "i", i, "j", j)
			}
		}()
	}
	for range 8 {
		<-done
	}
	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(lines) != 8*50 {
		t.Fatalf("have %d lines, want %d", len(lines), 8*50)
	}
	for _, l := range lines {
		if !strings.HasPrefix(l, "(2") || !strings.HasSuffix(l, `})`) {
			t.Fatalf("garbled line: %s", l)
		}
	}
}

func BenchmarkHandler(b *testing.B) {
	log := slog.New(NewHandler(nopWriter{}, nil)).
		With("service", "acme").
		WithGroup("demo")
	for b.Loop() {
		log.Info("the message", "foo", 4711, "bar", "baz")
	}
}

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }
