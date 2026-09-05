# xslog – eXtended S-eXpression structured logging

[![Go Reference](https://pkg.go.dev/badge/git.fractalqb.de/fractalqb/xslog.svg)](https://pkg.go.dev/git.fractalqb.de/fractalqb/xslog)

`import "git.fractalqb.de/fractalqb/xslog"`

---

**xslog** is a [`log/slog`](https://pkg.go.dev/log/slog) handler that writes log
records as [XSX](https://git.fractalqb.de/fractalqb/xsx) – eXtended
S-eXpressions.

A log is a stream of XSX expressions, i.e. a real syntax with balanced brackets
instead of a line format that only pretends to be structured. That makes a log
both readable in a terminal and parseable without heuristics – and it is
markedly more compact and faster to write than JSON.

```
(2026-09-04T17:22:45.981+2 INFO ~main.go:14 "listening" ~service acme ~addr 0.0.0.0:8080 ~tls false)
(2026-09-04T17:22:45.981+2 WARN ~main.go:15 "slow query" ~service acme ~db {~took 1.5s ~rows [4711 815] ~conn {~host db.internal ~pool 4}})
(2026-09-04T17:22:45.981+2 ERROR ~main.go:20 "shutting down" ~service acme ~err "context deadline exceeded")
```

#### More Human-Readable Messages

xslog is designed to be readable as well as compact and structured. If you
want the message itself to read even more like a sentence, see
[sllm](https://pkg.go.dev/git.fractalqb.de/fractalqb/sllm/v3), or
[qblog](https://pkg.go.dev/git.fractalqb.de/fractalqb/qblog) for its
`log/slog.Handler`. SLLM embeds named values directly in prose, for example
`` `user:alice` requested `path:/api/orders` ``, while qblog places the values
used by the message inline and writes remaining scoped attributes after it.

## Key Features

- **One record, one XSX expression** – nesting is explicit, so a record can be
  read back without guessing where a value ends.
- **Compact and spaced** writers: one record per line for machines,
  indented over several lines for reading in a terminal.
- **A reader** that parses a log back into `slog.Record`s.
- **A command** to pretty print, filter and normalize a log.
- **30–43 % smaller records than `slog.JSONHandler`** and **1.2–1.7 × faster**,
  with zero allocations in the common cases. See [Performance](#performance).
- **A well-behaved handler**: passes the `testing/slogtest` conformance suite,
  honours `ReplaceAttr`, `AddSource` and `Level`, elides empty groups and
  preformats `WithAttrs`.

## The record format

```
(TIME LEVEL ~SOURCE "MESSAGE" ~key value… ~group {~key value…})
```

The record is a paren group whose first fields are positional:

| field | | |
|---|---|---|
| `TIME` | `2026-09-04T17:22:45.981+2` | XSX time atom, or the void token `~` if the record has no time |
| `LEVEL` | `INFO`, `WARN+3` | the level's name as symbol |
| `~SOURCE` | `~main.go:14` | only with `AddSource`; the `~` meta prefix is what distinguishes it from the message |
| `"MESSAGE"` | `"listening"` | **always** a quoted string, which makes it unambiguously the last positional field |

Everything after the message is attributes, written as a `~key` meta atom
followed by its value. A group – from `WithGroup` or from a group attribute –
is a `~name` followed by a brace group. Empty groups are omitted entirely.

## Usage

```go
log := slog.New(xslog.NewHandler(os.Stdout, &slog.HandlerOptions{
    AddSource: true,
}))
log = log.With("service", "acme").WithGroup("db")
log.Warn("slow query",
    "took", 1500*time.Millisecond,
    "rows", []int{4711, 815},
    slog.Group("conn", "host", "db.internal", "pool", 4),
)
```

`NewSpaceHandler(w, opts, indent)` writes the same record with one attribute
per line, indented by one step per group – for reading in a terminal. An empty
indent step selects two spaces.

```
(2026-09-04T17:22:45.981+2 WARN ~main.go:15 "slow query"
  ~service acme
  ~db {
    ~took 1.5s
    ~rows [4711 815]
    ~conn {
      ~host db.internal
      ~pool 4}})
```

Values that are groups themselves – slices, maps and `any` values – stay on one
line, so nesting depth in the output always means attribute group nesting.

### Values

| `slog.Kind` | XSX | example |
|---|---|---|
| `String` | symbol if possible, else quoted string | `acme`, `"two words"` |
| `Int64`, `Uint64`, `Float64` | number atom | `-7`, `1.5` |
| `Bool` | `true` / `false` | `true` |
| `Duration` | symbol | `1.5s` |
| `Time` | XSX time atom | `2026-09-04T17:22:45.981+2` |
| `Group` | brace group | `{~host db.internal ~pool 4}` |
| `Any`: slice, array | bracket group | `[4711 815]` |
| `Any`: map | brace group, keys sorted | `{~a 1 ~b 2}` |
| `Any`: `nil` | void token | `~` |
| `Any`: `error`, `Stringer`, `TextMarshaler` | its text | `"context deadline exceeded"` |
| `Any`: `[]byte` | base86 symbol | |

The message is the only string that is always quoted. A panicking `String()`,
`Error()` or `MarshalText()` is recovered into a `!PANIC: …` value instead of
taking down the log call site.

## Reading a log back

```go
for e, err := range xslog.NewReader(r).Records() {
    if err != nil {
        return err
    }
    fmt.Println(e.Time, e.Level, e.Message)
    e.Attrs(func(a slog.Attr) bool {
        fmt.Println("  ", a.Key, "=", a.Value)
        return true
    })
}
```

`Reader` accepts both layouts. It yields an `Entry`, which embeds the
`slog.Record` and adds the `Source` separately – a record can only tell its
source through its PC, and a PC cannot be reconstructed from a logged
`file:line`. To write an entry including its source, use
`Handler.WriteEntry(e)` rather than `Handler.Handle(ctx, e.Record)`.

XSX atoms do not carry their Go type, so `Reader` infers one: a quoted atom is
always a string, a plain symbol becomes the first of `bool`, `int64`, `uint64`,
`float64`, `time.Duration` and `time.Time` that parses – and a string if it is
none of them. The inferred types are therefore not necessarily the ones that
were logged, but writing the entries again with `NewHandler` reproduces the log
byte for byte.

## The xslog command

```sh
go install git.fractalqb.de/fractalqb/xslog/cmd/xslog@latest
xslog app.log
```

| flag | |
|---|---|
| `-i step` | indent step of nested attributes, default two spaces |
| `-c` | compact output, one record per line |
| `-l level` | drop records below `level`, e.g. `WARN` |
| `-t layout` | reformat the record time with a Go time layout |
| `-b` | shorten the source to the base name of its file |

Without a file – or for the file `-` – the log is read from standard input. The
output is a valid log again, so `xslog log | xslog -c -` reproduces the input
byte for byte. Only `-t` breaks that, as it replaces the time atom with
free-form text.

## Performance

Measured with `go test -bench . -benchmem`, Go 1.27.1, linux/amd64, 12th Gen
Intel i7-1260P, writing to a discarding `io.Writer` so no I/O is included. The
scenarios are in [`bench_test.go`](bench_test.go); run them yourself before
trusting them.

### Time per record

| scenario | xslog | spaced | `JSONHandler` | `TextHandler` | vs JSON |
|---|---|---|---|---|---|
| message only | **262** | 282 | 369 | 442 | 1.41× |
| `With` + `WithGroup`, 2 attrs | **419** | 502 | 549 | 635 | 1.31× |
| 5 attrs | **607** | 689 | 734 | 795 | 1.21× |
| group attribute | **617** | 671 | 742 | 795 | 1.20× |
| `[]int` value | **528** | 549 | 763 | 912 | 1.45× |

ns/op. With `AddSource` the gap widens, because a file path is a plain XSX
symbol but has to be escaped as a JSON string:

| scenario | xslog | `JSONHandler` | vs JSON |
|---|---|---|---|
| message only | **577** | 996 | 1.73× |
| `With` + `WithGroup`, 2 attrs | **708** | 1148 | 1.62× |
| 5 attrs | **897** | 1294 | 1.44× |

### Allocations

Writing a record does not allocate at all in the common cases – neither for a
plain message nor for one with attributes preformatted by `WithAttrs`. The
remaining allocations are shared with the stdlib handlers: boxing in
`slog.Group`, reflection over a slice value, and `slog.TimeValue` of a
monotonic `time.Time`. One is our own: a `time.Duration` attribute costs one
allocation because `Duration.String()` allocates – the price of logging `1.5s`
instead of `1500000000`.

### Record size

| scenario | xslog | `JSONHandler` | `TextHandler` | saved vs JSON |
|---|---|---|---|---|
| message only | **47** | 82 | 64 | 43 % |
| `With` + `WithGroup`, 2 attrs | **100** | 142 | 114 | 30 % |
| 5 attrs | **112** | 162 | 124 | 31 % |
| group attribute | **93** | 134 | 109 | 31 % |
| `[]int` value | **67** | 103 | 85 | 35 % |

Bytes per record. XSX needs no quotes around keys, and none around values that
are already symbol-shaped.

### Reading

Reading 100 records with `Reader` takes 201 µs against 267 µs for
`json.Decoder` into a `map[string]any` – 1.33 × faster, but with more
allocations (5305 vs 2698), as `Reader` builds a `[]slog.Attr` plus a string
per key and value.

## Notes

- A brace group is read back as a `slog.KindGroup` value whether it was written
  from a group attribute or from a Go map. So a map value round-trips byte for
  byte through `NewHandler`, but not through `NewSpaceHandler`, which puts group
  attributes on separate lines while map values stay on one.
- XSX time atoms carry the zone offset, not the zone name: a parsed time has a
  `time.FixedZone("")`, equal in instant but not in `Location`.
- `[]byte` values are encoded base86, xsx's own compact encoding, and read back
  as the encoded text.
