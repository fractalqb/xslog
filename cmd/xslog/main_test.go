package main

import (
	"bytes"
	"log/slog"
	"math"
	"strings"
	"testing"
)

const sample = `(2026-09-04T15:45:52.623+2 DEBUG ~/a/b/main.go:16 "warm" ~n 128)
(2026-09-04T15:45:52.623+2 WARN ~/a/b/db.go:18 "slow" ~db {~took 1.5s ~rows [1 2]})
`

func TestPretty(t *testing.T) {
	for _, c := range []struct {
		name string
		cfg  func()
		want string
	}{
		{
			name: "default indents the attributes",
			cfg:  func() {},
			want: `(2026-09-04T15:45:52.623+2 DEBUG ~/a/b/main.go:16 "warm"
  ~n 128)
(2026-09-04T15:45:52.623+2 WARN ~/a/b/db.go:18 "slow"
  ~db {
    ~took 1.5s
    ~rows [1 2]})
`,
		},
		{
			name: "compact reproduces the input",
			cfg:  func() { cfg.compact = true },
			want: sample,
		},
		{
			name: "level drops records",
			cfg:  func() { cfg.compact, cfg.level = true, "WARN" },
			want: `(2026-09-04T15:45:52.623+2 WARN ~/a/b/db.go:18 "slow" ~db {~took 1.5s ~rows [1 2]})
`,
		},
		{
			name: "time layout and base name",
			cfg:  func() { cfg.compact, cfg.time, cfg.base = true, "15:04:05", true },
			want: `(15:45:52 DEBUG ~main.go:16 "warm" ~n 128)
(15:45:52 WARN ~db.go:18 "slow" ~db {~took 1.5s ~rows [1 2]})
`,
		},
		{
			name: "custom indent",
			cfg:  func() { cfg.indent = "\t" },
			want: `(2026-09-04T15:45:52.623+2 DEBUG ~/a/b/main.go:16 "warm"
	~n 128)
(2026-09-04T15:45:52.623+2 WARN ~/a/b/db.go:18 "slow"
	~db {
		~took 1.5s
		~rows [1 2]})
`,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			cfg.indent, cfg.compact, cfg.level, cfg.time, cfg.base = "  ", false, "", "", false
			c.cfg()
			minLevel := slog.Level(math.MinInt32)
			if cfg.level != "" {
				if err := minLevel.UnmarshalText([]byte(cfg.level)); err != nil {
					t.Fatal(err)
				}
			}
			var buf bytes.Buffer
			if err := pretty(newHandler(&buf), strings.NewReader(sample), minLevel); err != nil {
				t.Fatal(err)
			}
			if buf.String() != c.want {
				t.Errorf("\nhave:\n%swant:\n%s", buf.String(), c.want)
			}
		})
	}
}

func TestPrettyError(t *testing.T) {
	var buf bytes.Buffer
	err := pretty(newHandler(&buf), strings.NewReader(`(~ INFO "m" ~a)`), 0)
	if err == nil {
		t.Fatal("no error")
	}
	if !strings.Contains(err.Error(), "missing value of attribute a") {
		t.Errorf("unexpected error %q", err)
	}
}
