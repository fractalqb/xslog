// Command xslog pretty prints logs written by the xslog [slog.Handler].
//
// Usage:
//
//	xslog [flags] [file…]
//
// Without a file – or for the file "-" – xslog reads the log from standard
// input. Records are read with [xslog.Reader], so they may come in any layout
// that [xslog.Handler] writes.
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"syscall"

	"git.fractalqb.de/fractalqb/xslog"
)

var cfg = struct {
	indent  string
	compact bool
	level   string
	time    string
	base    bool
}{
	indent: "  ",
}

func main() {
	flag.StringVar(&cfg.indent, "i", cfg.indent, "Indent `step` of nested attributes")
	flag.BoolVar(&cfg.compact, "c", cfg.compact, "Compact output, i.e. one record per line")
	flag.StringVar(&cfg.level, "l", cfg.level, "Drop records below `level`, e.g. WARN or ERROR")
	flag.StringVar(&cfg.time, "t", cfg.time, "Reformat the record time with a Go time `layout`")
	flag.BoolVar(&cfg.base, "b", cfg.base, "Shorten the source to the base name of its file")
	flag.Usage = usage
	flag.Parse()

	if err := run(flag.Args()); err != nil {
		if errors.Is(err, syscall.EPIPE) {
			return // e.g. our output goes into head
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func usage() {
	w := flag.CommandLine.Output()
	fmt.Fprintf(w, `xslog pretty prints logs written by the xslog slog handler.

Usage: %s [flags] [file…]

Without a file – or for the file "-" – the log is read from standard input.
The output is a valid log again – xslog -c even reproduces the input byte for
byte – except with -t, which replaces the record time by free-form text.

Flags:
`, filepath.Base(os.Args[0]))
	flag.PrintDefaults()
}

func run(files []string) error {
	minLevel := slog.Level(math.MinInt32)
	if cfg.level != "" {
		if err := minLevel.UnmarshalText([]byte(cfg.level)); err != nil {
			return err
		}
	}
	out := bufio.NewWriter(os.Stdout)
	h := newHandler(out)
	defer out.Flush()

	if len(files) == 0 {
		files = []string{"-"}
	}
	for _, f := range files {
		if err := prettyFile(h, f, minLevel); err != nil {
			return err
		}
	}
	return out.Flush()
}

// newHandler returns the handler that writes the pretty printed records.
func newHandler(w io.Writer) *xslog.Handler {
	opts := &slog.HandlerOptions{
		AddSource:   true, // keep the source of the records that have one
		ReplaceAttr: replace,
	}
	if cfg.compact {
		return xslog.NewHandler(w, opts)
	}
	return xslog.NewSpaceHandler(w, opts, cfg.indent)
}

func replace(groups []string, a slog.Attr) slog.Attr {
	if len(groups) > 0 {
		return a
	}
	switch a.Key {
	case slog.TimeKey:
		if cfg.time == "" {
			break
		}
		if t := a.Value.Time(); !t.IsZero() {
			return slog.String(a.Key, t.Format(cfg.time))
		}
	case slog.SourceKey:
		if !cfg.base {
			break
		}
		if src, ok := a.Value.Any().(*slog.Source); ok {
			return slog.Any(a.Key, &slog.Source{
				Function: src.Function,
				File:     filepath.Base(src.File),
				Line:     src.Line,
			})
		}
	}
	return a
}

func prettyFile(h *xslog.Handler, file string, minLevel slog.Level) error {
	if file == "-" {
		if err := pretty(h, os.Stdin, minLevel); err != nil {
			return fmt.Errorf("<stdin>: %w", err)
		}
		return nil
	}
	r, err := os.Open(file)
	if err != nil {
		return err
	}
	defer r.Close()
	if err := pretty(h, r, minLevel); err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	return nil
}

func pretty(h *xslog.Handler, r io.Reader, minLevel slog.Level) error {
	for e, err := range xslog.NewReader(r).Records() {
		if err != nil {
			return err
		}
		if e.Level < minLevel {
			continue
		}
		if err := h.WriteEntry(e); err != nil {
			return err
		}
	}
	return nil
}
