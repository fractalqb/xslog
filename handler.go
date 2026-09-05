// Package xslog implements a [log/slog] handler that writes log records as
// XSX expressions, see [git.fractalqb.de/fractalqb/xsx]. [NewHandler] writes
// one record per line, [NewSpaceHandler] spreads a record over indented lines
// for reading in a terminal. [Reader] parses such records back into
// [log/slog.Record]s.
package xslog

import (
	"bytes"
	"context"
	"encoding"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"

	"git.fractalqb.de/fractalqb/xsx"
)

// Handler is a [slog.Handler] that writes each record as one XSX paren group
// followed by a newline:
//
//	(TIME LEVEL ~SOURCE "MESSAGE" ~key value…)
//
// The first fields are positional:
//
//   - TIME is the record time as XSX time atom. It is the void token ~ if the
//     record has no time.
//   - LEVEL is the level's name as symbol, e.g. INFO or WARN+3.
//   - SOURCE is the meta atom ~file:line. It is only present if
//     [slog.HandlerOptions.AddSource] is set and the record has a source. Being
//     meta distinguishes it from MESSAGE.
//   - MESSAGE is the record message. It is always written as quoted string,
//     even when it would be a valid symbol. This makes it unambiguously the
//     last positional field.
//
// The message is followed by the attributes as pairs of a meta key and its
// value. Groups – be it from [Handler.WithGroup] or from group attributes –
// are written as a meta key followed by a brace group with the group's
// attributes. Empty groups are omitted. Attribute values become the XSX atom
// that fits their [slog.Kind]; string values are written as symbol if
// possible and as quoted string otherwise. Slices and arrays become bracket
// groups, maps become brace groups with sorted keys.
//
// [NewSpaceHandler] returns a variant that spreads a record over several
// indented lines to make it easier to read in a terminal.
type Handler struct {
	w    io.Writer
	mu   *sync.Mutex
	opts slog.HandlerOptions

	// spc is the indentation of the spaced variant. It is nil for the compact
	// variant that writes one record per line.
	spc *spacing

	// pre are the attributes from WithAttrs, already encoded as XSX. It starts
	// with the separating space and is written verbatim after the message.
	pre []byte

	// groups are the group names from WithGroup in the order of their nesting.
	// The first opened of them are already written to pre, i.e. their brace
	// groups are left open by pre. The remaining groups are still pending:
	// they are written only when an attribute actually goes into them.
	groups []string
	opened int
}

var _ slog.Handler = (*Handler)(nil)

// NewHandler returns a new [Handler] writing to w. A nil opts is equivalent to
// the zero [slog.HandlerOptions].
func NewHandler(w io.Writer, opts *slog.HandlerOptions) *Handler {
	if w == nil {
		w = io.Discard
	}
	h := &Handler{w: w, mu: new(sync.Mutex)}
	if opts != nil {
		h.opts = *opts
	}
	return h
}

// NewSpaceHandler returns a new [Handler] that writes each attribute of a
// record on a line of its own, indented by one step per group:
//
//	(2026-09-04T15:45:52.623+2 INFO ~handler.go:35 "the message"
//	  ~service acme
//	  ~demo {
//	    ~foo [4711 815]})
//
// Values that are groups themselves – slices, maps and any values – stay on
// one line. An empty indent step selects two spaces.
func NewSpaceHandler(w io.Writer, opts *slog.HandlerOptions, indent string) *Handler {
	h := NewHandler(w, opts)
	if indent == "" {
		indent = "  "
	}
	h.spc = newSpacing(indent)
	return h
}

func (h *Handler) Enabled(_ context.Context, l slog.Level) bool {
	lvl := slog.LevelInfo
	if h.opts.Level != nil {
		lvl = h.opts.Level.Level()
	}
	return l >= lvl
}

func (h *Handler) clone() *Handler {
	res := *h
	return &res
}

func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	res := h.clone()
	e := h.encoder()
	defer e.release()
	for _, a := range attrs {
		e.attr(a)
	}
	if e.err != nil || e.buf.Len() == 0 {
		return res // keep pre and opened consistent with each other
	}
	res.pre = make([]byte, 0, len(h.pre)+e.buf.Len())
	res.pre = append(append(res.pre, h.pre...), e.buf.Bytes()...)
	res.opened = h.opened + e.opened
	return res
}

func (h *Handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	res := h.clone()
	res.groups = append(slices.Clip(h.groups), name)
	return res
}

func (h *Handler) Handle(_ context.Context, r slog.Record) error {
	return h.write(r, nil)
}

// WriteEntry writes an entry as read by a [Reader], including its
// [Entry.Source]. Passing [Entry.Record] to [Handler.Handle] would drop the
// source because a record can only tell its source through its PC.
func (h *Handler) WriteEntry(e Entry) error {
	return h.write(e.Record, e.Source)
}

// write writes record r. When src is not nil it is written as the record's
// source instead of the source of r's PC.
func (h *Handler) write(r slog.Record, src *slog.Source) error {
	e := h.encoder()
	defer e.release()
	e.chk(e.xw.Begin(xsx.Paren, false))

	// Without ReplaceAttr the positional fields go to the writer directly.
	// Wrapping them in an Attr just to unwrap it again would box the time and
	// the level in an any – one heap allocation per record.
	rep := h.opts.ReplaceAttr

	switch {
	case r.Time.IsZero():
		e.chk(e.xw.Void())
	case rep == nil:
		e.chk(e.xw.AtomTime(r.Time, false))
	default:
		if v, ok := h.builtin(slog.Time(slog.TimeKey, r.Time)); ok {
			e.value(v)
		} else {
			e.chk(e.xw.Void())
		}
	}

	if rep == nil {
		e.chk(e.xw.AtomSymStr(r.Level.String(), false))
	} else if v, ok := h.builtin(slog.Any(slog.LevelKey, r.Level)); ok {
		e.level(v)
	} else {
		e.chk(e.xw.Void())
	}

	if h.opts.AddSource {
		if src == nil && r.PC != 0 {
			src = source(r.PC)
		}
		if src != nil {
			if rep == nil {
				e.sourceOf(src)
			} else if v, ok := h.builtin(slog.Any(slog.SourceKey, src)); ok {
				e.source(v)
			}
		}
	}

	if rep == nil {
		e.chk(e.xw.AtomString(r.Message, false))
	} else if v, ok := h.builtin(slog.String(slog.MessageKey, r.Message)); ok {
		e.message(v)
	} else {
		e.chk(e.xw.AtomString("", false))
	}

	if e.err == nil && len(h.pre) > 0 {
		e.buf.Write(h.pre)
	}
	r.Attrs(func(a slog.Attr) bool {
		e.attr(a)
		return e.err == nil
	})
	if e.err != nil {
		return e.err
	}
	// Close the groups opened for this record's attributes, then the ones that
	// came in verbatim from pre – those are unknown to e.xw.
	for e.opened > 0 {
		e.end()
	}
	for range h.opened {
		e.buf.WriteByte('}')
	}
	e.chk(e.xw.End())
	if e.err != nil {
		return e.err
	}
	e.buf.WriteByte('\n')

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := h.w.Write(e.buf.Bytes())
	return err
}

// builtin applies [slog.HandlerOptions.ReplaceAttr] to a positional record
// field a and reports whether the field is to be written at all.
func (h *Handler) builtin(a slog.Attr) (slog.Value, bool) {
	if h.opts.ReplaceAttr != nil {
		a = h.opts.ReplaceAttr(nil, a)
	}
	v := a.Value.Resolve()
	return v, !isEmpty(a.Key, v)
}

func source(pc uintptr) *slog.Source {
	fs := runtime.CallersFrames([]uintptr{pc})
	f, _ := fs.Next()
	return &slog.Source{Function: f.Function, File: f.File, Line: f.Line}
}

// isEmpty tells if an attribute with key and value v has to be dropped, i.e.
// if both are the zero value.
func isEmpty(key string, v slog.Value) bool {
	return key == "" && v.Kind() == slog.KindAny && v.Any() == nil
}

const maxAnyDepth = 8

// spacing holds the indentation of the spaced [Handler] variant with the
// prefixes of the first few nesting levels precomputed.
type spacing struct {
	step string
	nl   []string
}

func newSpacing(step string) *spacing {
	s := &spacing{step: step, nl: make([]string, 16)}
	for i := range s.nl {
		s.nl[i] = "\n" + strings.Repeat(step, i)
	}
	return s
}

// sep returns the whitespace that separates the attributes of nesting level
// lvl from each other.
func (s *spacing) sep(lvl int) string {
	if lvl < len(s.nl) {
		return s.nl[lvl]
	}
	return "\n" + strings.Repeat(s.step, lvl)
}

// encoder encodes one XSX record – or one chunk of preformatted attributes –
// into its own buffer.
type encoder struct {
	buf bytes.Buffer
	// xw writes compactly as long as Space is not called, i.e. as long as spc
	// is nil.
	xw  *xsx.SpaceWriter
	spc *spacing
	lvl int // nesting level of the attributes written next
	rep func([]string, slog.Attr) slog.Attr
	err error

	// pending are the group names whose brace group is not opened yet, opened
	// counts the brace groups opened by this encoder. So the current group
	// nesting depth is opened + len(pending).
	pending []string
	opened  int

	path  []string // group path passed to rep
	depth int      // recursion depth of any values
}

var encPool = sync.Pool{New: func() any {
	e := new(encoder)
	e.xw = xsx.NewSpaceWriter(&e.buf)
	return e
}}

func (h *Handler) encoder() *encoder {
	e := encPool.Get().(*encoder)
	e.buf.Reset()
	e.xw.Reset(nil)
	e.spc = h.spc
	// The groups already open in pre are what the attributes written next are
	// nested in – plus the record's own paren group.
	e.lvl = 1 + h.opened
	e.rep = h.opts.ReplaceAttr
	e.err = nil
	e.pending = append(e.pending[:0], h.groups[h.opened:]...)
	e.opened = 0
	e.path = append(e.path[:0], h.groups...)
	e.depth = 0
	return e
}

// space writes the separator in front of the next attribute or group key.
func (e *encoder) space() {
	if e.spc == nil {
		// The writer separates atoms by itself – except at the start of a
		// preformatted chunk of attributes, which is appended verbatim right
		// behind an atom.
		if e.buf.Len() == 0 {
			e.buf.WriteByte(' ')
		}
		return
	}
	e.chk(e.xw.Space(e.spc.sep(e.lvl)))
}

func (e *encoder) release() {
	if e.buf.Cap() > 64*1024 {
		return // do not keep oversized buffers around
	}
	e.rep = nil
	encPool.Put(e)
}

func (e *encoder) chk(_ int, err error) {
	if err != nil && e.err == nil {
		e.err = err
	}
}

// flush opens the brace groups of all pending groups.
func (e *encoder) flush() {
	if len(e.pending) == 0 {
		return
	}
	for _, g := range e.pending {
		e.space()
		e.chk(e.xw.AtomSymStr(g, true))
		e.chk(e.xw.Begin(xsx.Brace, false))
		if e.err != nil {
			break
		}
		e.opened++
		e.lvl++
	}
	e.pending = e.pending[:0]
}

func (e *encoder) end() {
	e.chk(e.xw.End())
	e.opened--
	e.lvl--
}

func (e *encoder) attr(a slog.Attr) {
	if e.err != nil {
		return
	}
	v := a.Value.Resolve()
	if v.Kind() != slog.KindGroup && e.rep != nil {
		a = e.rep(e.path, slog.Attr{Key: a.Key, Value: v})
		v = a.Value.Resolve()
	}
	if v.Kind() == slog.KindGroup {
		gas := v.Group()
		if len(gas) == 0 {
			return // drop empty groups
		}
		if a.Key == "" { // inline groups with empty key
			for _, ga := range gas {
				e.attr(ga)
			}
			return
		}
		depth := e.opened + len(e.pending)
		e.pending = append(e.pending, a.Key)
		e.path = append(e.path, a.Key)
		for _, ga := range gas {
			e.attr(ga)
		}
		e.path = e.path[:len(e.path)-1]
		// Unwind to the group nesting we came in with. Groups that were never
		// opened just vanish from pending, opened ones have to be closed.
		for e.err == nil && e.opened+len(e.pending) > depth {
			if n := len(e.pending); n > 0 {
				e.pending = e.pending[:n-1]
			} else {
				e.end()
			}
		}
		return
	}
	if isEmpty(a.Key, v) {
		return
	}
	e.flush()
	e.space()
	e.chk(e.xw.AtomSymStr(a.Key, true))
	e.value(v)
}

func (e *encoder) value(v slog.Value) {
	if e.err != nil {
		return
	}
	switch v.Kind() {
	case slog.KindString:
		e.chk(e.xw.AtomSymStr(v.String(), false))
	case slog.KindInt64:
		e.chk(e.xw.AtomInt64(v.Int64(), false))
	case slog.KindUint64:
		e.chk(e.xw.AtomUint64(v.Uint64(), false))
	case slog.KindFloat64:
		e.chk(e.xw.AtomFloat64(v.Float64(), false))
	case slog.KindBool:
		e.chk(e.xw.AtomBool(v.Bool(), false))
	case slog.KindDuration:
		e.chk(e.xw.AtomSymStr(v.Duration().String(), false))
	case slog.KindTime:
		e.chk(e.xw.AtomTime(v.Time(), false))
	case slog.KindGroup:
		// Attribute groups do not come here, they are handled by attr. This is
		// for groups nested in some any value.
		e.chk(e.xw.Begin(xsx.Brace, false))
		for _, a := range v.Group() {
			if e.err != nil {
				break
			}
			e.chk(e.xw.AtomSymStr(a.Key, true))
			e.value(a.Value.Resolve())
		}
		e.chk(e.xw.End())
	default:
		e.any(v.Any())
	}
}

func (e *encoder) level(v slog.Value) {
	if l, ok := v.Any().(slog.Leveler); ok {
		e.chk(e.xw.AtomSymStr(l.Level().String(), false))
		return
	}
	e.value(v)
}

func (e *encoder) source(v slog.Value) {
	switch src := v.Any().(type) {
	case *slog.Source:
		if src != nil {
			e.sourceOf(src)
		}
	case slog.Source:
		e.sourceOf(&src)
	case string:
		e.chk(e.xw.AtomSymStr(src, true))
	default:
		e.chk(e.xw.AtomSymStr(fmt.Sprint(src), true))
	}
}

func (e *encoder) sourceOf(src *slog.Source) {
	s := src.File
	if src.Line > 0 {
		s += ":" + strconv.Itoa(src.Line)
	}
	e.chk(e.xw.AtomSymStr(s, true))
}

func (e *encoder) message(v slog.Value) {
	if v.Kind() == slog.KindString {
		e.chk(e.xw.AtomString(v.String(), false))
		return
	}
	e.value(v)
}

func (e *encoder) any(a any) {
	if e.err != nil {
		return
	}
	switch x := a.(type) {
	case nil:
		e.chk(e.xw.Void())
	case xsx.Marshaler:
		e.chk(e.xw.WriteAny(x, false))
	case xsx.Symbol:
		e.chk(e.xw.AtomSymbol(x, false))
	case xsx.SymStr:
		e.chk(e.xw.AtomSymStr(string(x), false))
	case []byte:
		e.chk(e.xw.AtomBytes(x, nil, false))
	case error:
		e.chk(e.xw.AtomSymStr(safeString(x.Error), false))
	case fmt.Stringer:
		e.chk(e.xw.AtomSymStr(safeString(x.String), false))
	case encoding.TextMarshaler:
		e.chk(e.xw.AtomSymStr(safeString(func() string {
			b, err := x.MarshalText()
			if err != nil {
				return "!ERROR: " + err.Error()
			}
			return string(b)
		}), false))
	default:
		e.reflect(a)
	}
}

func (e *encoder) reflect(a any) {
	rv := reflect.ValueOf(a)
	if e.depth >= maxAnyDepth {
		e.chk(e.xw.AtomSymStr(safeString(func() string { return fmt.Sprint(a) }), false))
		return
	}
	e.depth++
	defer func() { e.depth-- }()
	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		e.chk(e.xw.Begin(xsx.Bracket, false))
		for i := range rv.Len() {
			if e.err != nil {
				break
			}
			e.value(slog.AnyValue(rv.Index(i).Interface()))
		}
		e.chk(e.xw.End())
	case reflect.Map:
		keys := rv.MapKeys()
		strs := make([]string, len(keys))
		for i, k := range keys {
			strs[i] = fmt.Sprint(k.Interface())
		}
		order := make([]int, len(keys))
		for i := range order {
			order[i] = i
		}
		slices.SortFunc(order, func(a, b int) int {
			return strings.Compare(strs[a], strs[b])
		})
		e.chk(e.xw.Begin(xsx.Brace, false))
		for _, i := range order {
			if e.err != nil {
				break
			}
			e.chk(e.xw.AtomSymStr(strs[i], true))
			e.value(slog.AnyValue(rv.MapIndex(keys[i]).Interface()))
		}
		e.chk(e.xw.End())
	case reflect.Pointer, reflect.Interface:
		if rv.IsNil() {
			e.chk(e.xw.Void())
		} else {
			e.any(rv.Elem().Interface())
		}
	default:
		e.chk(e.xw.AtomSymStr(safeString(func() string { return fmt.Sprint(a) }), false))
	}
}

// safeString keeps a panicking String, Error or MarshalText method from taking
// down the caller of the log statement.
func safeString(f func() string) (res string) {
	defer func() {
		if p := recover(); p != nil {
			res = fmt.Sprintf("!PANIC: %v", p)
		}
	}()
	return f()
}
