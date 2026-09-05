package xslog

import (
	"errors"
	"fmt"
	"io"
	"iter"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"git.fractalqb.de/fractalqb/xsx"
)

// An Entry is one log record read by a [Reader]. Source is not part of the
// [slog.Record] because a record's PC cannot be reconstructed from the logged
// file and line – and neither can the function name, so [slog.Source.Function]
// stays empty.
type Entry struct {
	slog.Record
	Source *slog.Source
}

// Reader reads the log records written by [Handler], both from the compact and
// from the spaced variant.
//
// Attribute values are XSX atoms and as such do not carry their Go type, so
// Reader infers it: A quoted atom is always a string. A plain symbol becomes
// the first of bool, int64, uint64, float64, [time.Duration] and [time.Time]
// that it can be parsed as – and a string if it is none of them. A bracket
// group becomes a []slog.Value in a [slog.KindAny] value and a brace group
// becomes a [slog.KindGroup] value – no matter if it was written from a group
// attribute or from a Go map.
//
// So the inferred Go types are not necessarily the ones that were logged, but
// writing the entries again with [NewHandler] reproduces the log byte for
// byte. This does not hold for [NewSpaceHandler]: it puts the attributes of a
// group on lines of their own, whereas a map value stays on one line.
type Reader struct {
	scn *xsx.Scanner
	tok *xsx.Token
}

func NewReader(r io.Reader) *Reader {
	scn := xsx.NewScanner(r, nil)
	return &Reader{scn: scn, tok: scn.Token()}
}

// Read returns the next entry from the log. At the end of the log it returns
// [io.EOF].
func (r *Reader) Read() (Entry, error) {
	switch ok, err := r.scn.HasNext(); {
	case err != nil:
		return Entry{}, err
	case !ok:
		return Entry{}, io.EOF
	}
	if r.tok.Type() != xsx.Begin || r.tok.Group() != xsx.Paren {
		return Entry{}, fmt.Errorf("xslog: record starts with %s token '%s'",
			r.tok.Type(), r.tok.Text(),
		)
	}
	var (
		res Entry
		t   time.Time
		lvl slog.Level
	)
	if err := r.next(slog.TimeKey); err != nil {
		return Entry{}, err
	}
	if r.tok.Type() == xsx.Atom { // void means: the record had no time
		if err := xsx.AsTime(&t).Check(r.tok, nil); err != nil {
			return Entry{}, fmt.Errorf("xslog: %s: %w", slog.TimeKey, err)
		}
	}
	if err := r.next(slog.LevelKey); err != nil {
		return Entry{}, err
	}
	if r.tok.Type() == xsx.Atom {
		if err := lvl.UnmarshalText(r.tok.Bytes()); err != nil {
			return Entry{}, fmt.Errorf("xslog: %s: %w", slog.LevelKey, err)
		}
	}
	if err := r.next(slog.MessageKey); err != nil {
		return Entry{}, err
	}
	if r.tok.Meta() { // the source is optional, being meta identifies it
		res.Source = parseSource(r.tok.Text())
		if err := r.next(slog.MessageKey); err != nil {
			return Entry{}, err
		}
	}
	if r.tok.Type() != xsx.Atom {
		return Entry{}, fmt.Errorf("xslog: %s is a %s token",
			slog.MessageKey, r.tok.Type(),
		)
	}
	res.Record = slog.NewRecord(t, lvl, r.tok.Text(), 0)
	attrs, err := r.attrs()
	if err != nil {
		return Entry{}, err
	}
	res.AddAttrs(attrs...)
	return res, nil
}

// Records iterates over the entries of the log. Iteration stops after the
// first error, which is yielded with a zero [Entry]. The end of the log is not
// an error.
func (r *Reader) Records() iter.Seq2[Entry, error] {
	return func(yield func(Entry, error) bool) {
		for {
			e, err := r.Read()
			if errors.Is(err, io.EOF) {
				return
			}
			if !yield(e, err) || err != nil {
				return
			}
		}
	}
}

// next reads the next token of the current group. what names the token that
// was expected in case the record ends instead.
func (r *Reader) next(what string) error {
	switch ok, err := r.scn.GroupNext(); {
	case errors.Is(err, io.EOF):
		return fmt.Errorf("xslog: record ends before %s: %w",
			what, io.ErrUnexpectedEOF,
		)
	case err != nil:
		return err
	case !ok:
		return fmt.Errorf("xslog: record ends before %s", what)
	}
	return nil
}

// attrs reads the ~key value pairs up to the end of the current group.
func (r *Reader) attrs() ([]slog.Attr, error) {
	var res []slog.Attr
	for {
		switch ok, err := r.scn.GroupNext(); {
		case err != nil:
			return nil, err
		case !ok:
			return res, nil
		}
		if r.tok.Type() != xsx.Atom || !r.tok.Meta() {
			return nil, fmt.Errorf("xslog: attribute key is a %s token '%s'",
				r.tok.Type(), r.tok.Text(),
			)
		}
		key := r.tok.Text()
		if err := r.scn.Next(); err != nil {
			if errors.Is(err, io.EOF) {
				err = fmt.Errorf("xslog: missing value of attribute %s: %w",
					key, io.ErrUnexpectedEOF,
				)
			}
			return nil, err
		}
		if r.tok.Type() == xsx.End {
			return nil, fmt.Errorf("xslog: missing value of attribute %s", key)
		}
		v, err := r.value()
		if err != nil {
			return nil, err
		}
		res = append(res, slog.Attr{Key: key, Value: v})
	}
}

func (r *Reader) value() (slog.Value, error) {
	switch r.tok.Type() {
	case xsx.Void:
		return slog.AnyValue(nil), nil
	case xsx.Atom:
		if r.tok.Quoted() {
			return slog.StringValue(r.tok.Text()), nil
		}
		return symbolValue(r.tok), nil
	case xsx.Begin:
		switch r.tok.Group() {
		case xsx.Brace:
			as, err := r.attrs()
			return slog.GroupValue(as...), err
		case xsx.Bracket:
			var vs []slog.Value
			for {
				switch ok, err := r.scn.GroupNext(); {
				case err != nil:
					return slog.Value{}, err
				case !ok:
					return slog.AnyValue(vs), nil
				}
				v, err := r.value()
				if err != nil {
					return slog.Value{}, err
				}
				vs = append(vs, v)
			}
		}
	}
	return slog.Value{}, fmt.Errorf("xslog: unexpected %s token '%s' as value",
		r.tok.Type(), r.tok.Text(),
	)
}

// symbolValue infers the type of an unquoted atom.
func symbolValue(tok *xsx.Token) slog.Value {
	s := tok.Text()
	switch s {
	case xsx.True:
		return slog.BoolValue(true)
	case xsx.False:
		return slog.BoolValue(false)
	}
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		return slog.Int64Value(i)
	}
	if u, err := strconv.ParseUint(s, 10, 64); err == nil {
		return slog.Uint64Value(u)
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return slog.Float64Value(f)
	}
	if d, err := time.ParseDuration(s); err == nil {
		return slog.DurationValue(d)
	}
	var t time.Time
	if xsx.AsTime(&t).Check(tok, nil) == nil {
		return slog.TimeValue(t)
	}
	return slog.StringValue(s)
}

// parseSource splits the file:line source atom. A source without line number
// is taken as the file.
func parseSource(s string) *slog.Source {
	res := &slog.Source{File: s}
	if i := strings.LastIndexByte(s, ':'); i > 0 {
		if l, err := strconv.Atoi(s[i+1:]); err == nil {
			res.File, res.Line = s[:i], l
		}
	}
	return res
}
