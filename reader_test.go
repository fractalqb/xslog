package xslog

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func ExampleReader() {
	const log = `(2026-09-04T15:45:52.623+2 INFO ~main.go:11 "starting" ~port 8080)
(2026-09-04T15:45:53+2 ERROR ~db.go:42 "cannot connect" ~db {~host localhost ~retry 1.5s})
`
	for e, err := range NewReader(strings.NewReader(log)).Records() {
		if err != nil {
			fmt.Println("error:", err)
			break
		}
		fmt.Printf("%s %s %q at %s:%d\n",
			e.Time.Format(time.TimeOnly), e.Level, e.Message,
			e.Source.File, e.Source.Line,
		)
		e.Attrs(func(a slog.Attr) bool {
			fmt.Printf("\t%s = %s (%s)\n", a.Key, a.Value, a.Value.Kind())
			return true
		})
	}
	// Output:
	// 15:45:52 INFO "starting" at main.go:11
	//	port = 8080 (Int64)
	// 15:45:53 ERROR "cannot connect" at db.go:42
	//	db = [host=localhost retry=1.5s] (Group)
}

// TestReaderRoundtrip logs records, reads them back and logs them compactly
// again. That has to reproduce the compact log byte for byte, no matter which
// layout was read.
func TestReaderRoundtrip(t *testing.T) {
	log := func(l *slog.Logger) {
		l.Info("plain")
		l.Warn("attrs of all kinds",
			"sym", "acme",
			"str", "two words",
			"empty", "",
			"int", -7,
			"big", uint64(1)<<63,
			"float", 1.5,
			"bool", false,
			"dur", 1500*time.Millisecond,
			"ts", exampleTime,
			"nil", nil,
			"ints", []int{4711, 815},
			"strs", []string{"a", "b c"},
			"map", map[string]int{"b": 2, "a": 1},
			"err", os.ErrNotExist,
		)
		l.With("service", "acme").WithGroup("g").
			Error("groups", "a", 1, slog.Group("h", "b", 2))
		l.Log(context.TODO(), slog.LevelWarn+3, "custom level")
	}

	// Pin the record time so that all the logs below are comparable.
	opts := &slog.HandlerOptions{
		ReplaceAttr: func(gs []string, a slog.Attr) slog.Attr {
			if len(gs) == 0 && a.Key == slog.TimeKey {
				return slog.Time(a.Key, exampleTime)
			}
			return a
		},
	}
	// The compact log is the reference: reading any layout and writing it
	// compactly again has to reproduce it byte for byte.
	var want bytes.Buffer
	log(slog.New(NewHandler(&want, opts)))

	for _, indent := range []string{"", "  ", "\t"} {
		t.Run("indent "+strconv.Quote(indent), func(t *testing.T) {
			var first bytes.Buffer
			if indent == "" {
				log(slog.New(NewHandler(&first, opts)))
			} else {
				log(slog.New(NewSpaceHandler(&first, opts, indent)))
			}

			var second bytes.Buffer
			h := slog.Handler(NewHandler(&second, opts))
			for e, err := range NewReader(bytes.NewReader(first.Bytes())).Records() {
				if err != nil {
					t.Fatal(err)
				}
				if !h.Enabled(context.TODO(), e.Level) {
					t.Fatalf("level %s not enabled", e.Level)
				}
				if err := h.Handle(context.TODO(), e.Record); err != nil {
					t.Fatal(err)
				}
			}
			if second.String() != want.String() {
				t.Errorf("\nhave:\n%s\nwant:\n%s", second.String(), want.String())
			}
		})
	}
}

func TestReaderValues(t *testing.T) {
	for _, c := range []struct {
		atom string
		kind slog.Kind
		want any
	}{
		{`x`, slog.KindString, "x"},
		{`"x"`, slog.KindString, "x"},
		{`"42"`, slog.KindString, "42"},
		{`""`, slog.KindString, ""},
		{`42`, slog.KindInt64, int64(42)},
		{`-42`, slog.KindInt64, int64(-42)},
		{`9223372036854775808`, slog.KindUint64, uint64(1) << 63},
		{`1.5`, slog.KindFloat64, 1.5},
		{`true`, slog.KindBool, true},
		{`false`, slog.KindBool, false},
		{`"true"`, slog.KindString, "true"},
		{`1.5s`, slog.KindDuration, 1500 * time.Millisecond},
		{`2026-09-04T15:45:52.623+2`, slog.KindTime, exampleTime},
		{`~`, slog.KindAny, nil},
		{`0x2a`, slog.KindString, "0x2a"},
	} {
		t.Run(c.atom, func(t *testing.T) {
			e, err := NewReader(strings.NewReader(
				`(~ INFO "m" ~a ` + c.atom + `)`,
			)).Read()
			if err != nil {
				t.Fatal(err)
			}
			var v slog.Value
			e.Attrs(func(a slog.Attr) bool { v = a.Value; return false })
			if v.Kind() != c.kind {
				t.Fatalf("have %s value %v, want %s", v.Kind(), v.Any(), c.kind)
			}
			switch want := c.want.(type) {
			case time.Time: // the zone name is not part of the wire format
				if got := v.Time(); !got.Equal(want) {
					t.Errorf("have %s, want %s", got, want)
				}
			default:
				if got := v.Any(); got != want {
					t.Errorf("have %#v, want %#v", got, want)
				}
			}
		})
	}
}

func TestReaderStructuredValues(t *testing.T) {
	e, err := NewReader(strings.NewReader(
		`(~ INFO "m" ~l [1 "a b" [2 3] {~k v}] ~g {~a 1 ~h {~b 2}})`,
	)).Read()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	e.Attrs(func(a slog.Attr) bool {
		got = append(got, fmt.Sprintf("%s=%s:%v", a.Key, a.Value.Kind(), a.Value))
		return true
	})
	want := []string{
		`l=Any:[1 a b [2 3] [k=v]]`,
		`g=Group:[a=1 h=[b=2]]`,
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("\nhave: %s\nwant: %s", got, want)
	}
}

func TestReaderErrors(t *testing.T) {
	for _, c := range []struct {
		log  string
		want string
	}{
		{`~`, "record starts with Void token"},
		{`[~ INFO "m"]`, "record starts with Begin token"},
		{`(`, "record ends before time: unexpected EOF"},
		{`(~)`, "record ends before level"},
		{`(~ INFO)`, "record ends before msg"},
		{`(~ INFO ~src.go:1)`, "record ends before msg"},
		{`(nonsense INFO "m")`, "time: time token"},
		{`(~ NOLEVEL "m")`, "level: slog: level string"},
		{`(~ INFO {~a 1})`, "msg is a Begin token"},
		{`(~ INFO "m" a 1)`, "attribute key is a Atom token 'a'"},
		{`(~ INFO "m" ~a)`, "missing value of attribute a"},
		{`(~ INFO "m" ~a (1))`, "unexpected Begin token '(' as value"},
	} {
		t.Run(c.log, func(t *testing.T) {
			_, err := NewReader(strings.NewReader(c.log)).Read()
			if err == nil {
				t.Fatal("no error")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("have %q, want it to contain %q", err, c.want)
			}
		})
	}
}

func TestReaderEOF(t *testing.T) {
	r := NewReader(strings.NewReader("(~ INFO \"m\")\n\n"))
	if _, err := r.Read(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Read(); !errors.Is(err, io.EOF) {
		t.Errorf("have %v, want %v", err, io.EOF)
	}
	var n int
	for range NewReader(strings.NewReader("")).Records() {
		n++
	}
	if n != 0 {
		t.Errorf("%d entries from an empty log", n)
	}
}

func TestReaderSource(t *testing.T) {
	for _, c := range []struct {
		atom      string
		file      string
		line      int
		wantEmpty bool
	}{
		{`~/a/b/main.go:42`, "/a/b/main.go", 42, false},
		{`~main.go`, "main.go", 0, false},
		{`~"my file.go:7"`, "my file.go", 7, false},
		{``, "", 0, true}, // no source at all
	} {
		t.Run("source "+c.atom, func(t *testing.T) {
			e, err := NewReader(strings.NewReader(
				`(~ INFO ` + c.atom + ` "m")`,
			)).Read()
			if err != nil {
				t.Fatal(err)
			}
			if c.wantEmpty {
				if e.Source != nil {
					t.Fatalf("have source %v, want none", e.Source)
				}
				return
			}
			if e.Source.File != c.file || e.Source.Line != c.line {
				t.Errorf("have %s:%d, want %s:%d",
					e.Source.File, e.Source.Line, c.file, c.line,
				)
			}
		})
	}
}

// TestReaderSourceRoundtrip checks the byte exact round trip of records with
// a source, which needs [Handler.WriteEntry] because a slog.Record's PC – and
// with it its source – cannot be reconstructed.
func TestReaderSourceRoundtrip(t *testing.T) {
	const in = `(2026-09-04T15:45:52.623+2 INFO ~main.go:42 "m" ~a 1)` + "\n" +
		`(~ WARN ~"/a b/x.go:7" "n" ~g {~b 2})` + "\n" +
		`(~ ERROR ~main.go "no line" ~c 3)` + "\n"
	var buf bytes.Buffer
	h := NewHandler(&buf, &slog.HandlerOptions{AddSource: true})
	for e, err := range NewReader(strings.NewReader(in)).Records() {
		if err != nil {
			t.Fatal(err)
		}
		if err := h.WriteEntry(e); err != nil {
			t.Fatal(err)
		}
	}
	if buf.String() != in {
		t.Errorf("\nhave:\n%swant:\n%s", buf.String(), in)
	}
}

// TestHandleDropsSource documents that Handle cannot write the source of an
// entry: it is not part of the slog.Record.
func TestHandleDropsSource(t *testing.T) {
	var buf bytes.Buffer
	h := NewHandler(&buf, &slog.HandlerOptions{AddSource: true})
	e, err := NewReader(strings.NewReader(`(~ INFO ~main.go:42 "m")`)).Read()
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Handle(context.TODO(), e.Record); err != nil {
		t.Fatal(err)
	}
	if want := "(~ INFO \"m\")\n"; buf.String() != want {
		t.Errorf("have %q, want %q", buf.String(), want)
	}
}
