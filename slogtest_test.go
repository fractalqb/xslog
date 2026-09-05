package xslog

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"testing/slogtest"

	"git.fractalqb.de/fractalqb/xsx"
)

func TestSlogtest(t *testing.T) {
	for _, c := range []struct {
		name string
		new  func(io.Writer, *slog.HandlerOptions) *Handler
	}{
		{"compact", NewHandler},
		{"spaced", func(w io.Writer, o *slog.HandlerOptions) *Handler {
			return NewSpaceHandler(w, o, "")
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			h := c.new(&buf, &slog.HandlerOptions{AddSource: true})
			err := slogtest.TestHandler(h, func() []map[string]any {
				res, err := parseRecords(buf.String())
				if err != nil {
					t.Fatalf("%s\n%s", err, buf.String())
				}
				return res
			})
			if err != nil {
				t.Error(err)
			}
		})
	}
}

// parseRecords reads all XSX log records written by [Handler] into the
// map[string]any representation expected by [slogtest.TestHandler].
func parseRecords(s string) ([]map[string]any, error) {
	scn := xsx.NewStringScanner(s, nil)
	var res []map[string]any
	for {
		ok, err := scn.HasNext()
		if err != nil {
			return nil, err
		}
		if !ok {
			return res, nil
		}
		m, err := parseRecord(scn)
		if err != nil {
			return nil, err
		}
		res = append(res, m)
	}
}

// parseRecord reads one record. The scanner's current token must be the
// record's opening paren.
func parseRecord(scn *xsx.Scanner) (map[string]any, error) {
	tok := scn.Token()
	if tok.Type() != xsx.Begin || tok.Group() != xsx.Paren {
		return nil, fmt.Errorf("record starts with %s token", tok.Type())
	}
	m := make(map[string]any)
	for _, key := range []string{slog.TimeKey, slog.LevelKey} {
		if ok, err := scn.GroupNext(); err != nil {
			return nil, err
		} else if !ok {
			return nil, fmt.Errorf("record ends before %s", key)
		}
		if tok.Type() == xsx.Atom { // void means: not logged
			m[key] = tok.Text()
		}
	}
	if ok, err := scn.GroupNext(); err != nil {
		return nil, err
	} else if !ok {
		return nil, fmt.Errorf("record ends before %s", slog.MessageKey)
	}
	if tok.Meta() { // the optional source comes as meta atom
		m[slog.SourceKey] = tok.Text()
		if ok, err := scn.GroupNext(); err != nil {
			return nil, err
		} else if !ok {
			return nil, fmt.Errorf("record ends before %s", slog.MessageKey)
		}
	}
	if !tok.Quoted() {
		return nil, fmt.Errorf("%s is not a quoted string", slog.MessageKey)
	}
	m[slog.MessageKey] = tok.Text()
	return m, parseAttrs(scn, m)
}

// parseAttrs reads the ~key value pairs up to the end of the current group.
func parseAttrs(scn *xsx.Scanner, into map[string]any) error {
	tok := scn.Token()
	for {
		ok, err := scn.GroupNext()
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		if tok.Type() != xsx.Atom || !tok.Meta() {
			return fmt.Errorf("attribute key is a %s token", tok.Type())
		}
		key := tok.Text()
		if err := scn.Next(); err != nil {
			return err
		}
		if into[key], err = parseValue(scn); err != nil {
			return err
		}
	}
}

func parseValue(scn *xsx.Scanner) (any, error) {
	tok := scn.Token()
	switch tok.Type() {
	case xsx.Void:
		return nil, nil
	case xsx.Atom:
		return tok.Text(), nil
	case xsx.Begin:
		switch tok.Group() {
		case xsx.Brace:
			m := make(map[string]any)
			return m, parseAttrs(scn, m)
		case xsx.Bracket:
			xs := []any{}
			for {
				ok, err := scn.GroupNext()
				if err != nil {
					return nil, err
				}
				if !ok {
					return xs, nil
				}
				x, err := parseValue(scn)
				if err != nil {
					return nil, err
				}
				xs = append(xs, x)
			}
		}
	}
	return nil, fmt.Errorf("unexpected %s token as value", tok.Type())
}
