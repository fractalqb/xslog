package xslog

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"
)

// The scenarios both the xslog and the stdlib handlers are measured with.
var benchCases = []struct {
	name string
	log  func(*slog.Logger)
}{
	{
		name: "plain",
		log:  func(l *slog.Logger) { l.Info("the message") },
	},
	{
		name: "5attrs",
		log: func(l *slog.Logger) {
			l.Info("the message",
				"service", "acme",
				"addr", "0.0.0.0:8080",
				"count", 4711,
				"took", 1500*time.Millisecond,
				"ok", true,
			)
		},
	},
	{
		name: "withattrs",
		log: func(l *slog.Logger) {
			l.Info("the message", "count", 4711, "ok", true)
		},
	},
	{
		name: "group",
		log: func(l *slog.Logger) {
			l.Info("the message",
				"count", 4711,
				slog.Group("conn", "host", "db.internal", "pool", 4),
			)
		},
	},
	{
		name: "slice",
		log: func(l *slog.Logger) {
			l.Info("the message", "rows", []int{4711, 815, 42})
		},
	},
}

// benchLogger prepares the logger of case name for handler h.
func benchLogger(name string, h slog.Handler) *slog.Logger {
	l := slog.New(h)
	if name == "withattrs" {
		l = l.With("service", "acme", "version", 3).WithGroup("req")
	}
	return l
}

func BenchmarkHandlers(b *testing.B) {
	for _, hc := range []struct {
		name string
		new  func(io.Writer, *slog.HandlerOptions) slog.Handler
	}{
		{"xslog", func(w io.Writer, o *slog.HandlerOptions) slog.Handler {
			return NewHandler(w, o)
		}},
		{"xslog-space", func(w io.Writer, o *slog.HandlerOptions) slog.Handler {
			return NewSpaceHandler(w, o, "")
		}},
		{"json", func(w io.Writer, o *slog.HandlerOptions) slog.Handler {
			return slog.NewJSONHandler(w, o)
		}},
		{"text", func(w io.Writer, o *slog.HandlerOptions) slog.Handler {
			return slog.NewTextHandler(w, o)
		}},
	} {
		for _, src := range []bool{false, true} {
			opts := &slog.HandlerOptions{AddSource: src}
			for _, c := range benchCases {
				name := hc.name + "/" + c.name
				if src {
					name += "+source"
				}
				b.Run(name, func(b *testing.B) {
					l := benchLogger(c.name, hc.new(nopWriter{}, opts))
					b.ResetTimer()
					for b.Loop() {
						c.log(l)
					}
				})
			}
		}
	}
}

// BenchmarkRecordSize reports the bytes each handler needs per record. It is
// not a speed benchmark – only the bytes/record metric is meaningful.
func BenchmarkRecordSize(b *testing.B) {
	for _, hc := range []struct {
		name string
		new  func(io.Writer) slog.Handler
	}{
		{"xslog", func(w io.Writer) slog.Handler { return NewHandler(w, nil) }},
		{"json", func(w io.Writer) slog.Handler { return slog.NewJSONHandler(w, nil) }},
		{"text", func(w io.Writer) slog.Handler { return slog.NewTextHandler(w, nil) }},
	} {
		for _, c := range benchCases {
			b.Run(hc.name+"/"+c.name, func(b *testing.B) {
				var cnt countWriter
				l := benchLogger(c.name, hc.new(&cnt))
				for b.Loop() {
					c.log(l)
				}
				b.ReportMetric(float64(cnt.n)/float64(b.N), "bytes/record")
				b.ReportMetric(0, "ns/op")
			})
		}
	}
}

type countWriter struct{ n int }

func (w *countWriter) Write(p []byte) (int, error) {
	w.n += len(p)
	return len(p), nil
}

// BenchmarkRead compares reading a log back with [Reader] to unmarshalling the
// equivalent JSON records into a map.
func BenchmarkRead(b *testing.B) {
	const records = 100
	var xsxLog, jsonLog []byte
	{
		var w countingBuf
		l := slog.New(NewHandler(&w, nil)).With("service", "acme")
		for range records {
			l.Info("the message", "count", 4711, "took", time.Second,
				slog.Group("conn", "host", "db.internal", "pool", 4))
		}
		xsxLog = w.b
	}
	{
		var w countingBuf
		l := slog.New(slog.NewJSONHandler(&w, nil)).With("service", "acme")
		for range records {
			l.Info("the message", "count", 4711, "took", time.Second,
				slog.Group("conn", "host", "db.internal", "pool", 4))
		}
		jsonLog = w.b
	}

	b.Run("xslog", func(b *testing.B) {
		for b.Loop() {
			for _, err := range NewReader(bytes.NewReader(xsxLog)).Records() {
				if err != nil {
					b.Fatal(err)
				}
			}
		}
	})
	b.Run("json", func(b *testing.B) {
		for b.Loop() {
			dec := json.NewDecoder(bytes.NewReader(jsonLog))
			for {
				m := make(map[string]any)
				if err := dec.Decode(&m); err != nil {
					break
				}
			}
		}
	})
}

type countingBuf struct{ b []byte }

func (w *countingBuf) Write(p []byte) (int, error) {
	w.b = append(w.b, p...)
	return len(p), nil
}
