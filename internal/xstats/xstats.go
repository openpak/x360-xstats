// Package xstats serves Xbox Live leaderboards (Live service 7): the XRL requests
// XAM sends for XSessionWriteStats/XUserWriteStats (statpost), XUserReadStats and
// the stats enumerators. Layouts come from the XAM 17559 schemas and builders
// (docs/protocol.md).
//
// No per-title configuration is needed: XAM reads the title's own stats views
// (its SPA) and sends one procedure per view column aggregation, with the
// column ids, so the server only stores and ranks.
package xstats

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/openpak/x360-common/wire"
)

// ContentType is the XRL wire type XAM sends and expects.
const ContentType = "xon/5"

// Column aggregations (SPA ViewFieldEntry.aggregation_type), sent as the proc id.
const (
	procLast = 0x8001
	procSum  = 0x8003
	procMin  = 0x8009
	procMax  = 0x800B
	// 0x800D / 0x800E are XAM's skill (TrueSkill) updates, FUN_819189a8: not served,
	// so skill reads find no row and XAM falls back to its default mu/sigma.
)

// Special column ids (Xenia spa_info.h AttributeIdToName).
const (
	colRank     = 0xFFFF
	colRating   = 0xFFFE
	colGamertag = 0xFFFD
)

const (
	maxBody    = 64 << 10
	maxRows    = 100 // per view in one reply
	maxSpecs   = 64  // schema maximum (value table entry 37)
	maxColumns = 64
)

// Row is one user's row in a view.
type Row struct {
	Gamertag string           `json:"gamertag,omitempty"`
	Cols     map[uint16]Value `json:"cols"`
	Seq      uint64           `json:"seq"` // first-write order, breaks rating ties
}

func (r *Row) rating() int64 { return r.Cols[colRating].Int() }

// Store keeps every view in memory and rewrites one JSON file after each change.
// ponytail: whole-file rewrite and a sort per read; move to a database when
// leaderboards get large.
type Store struct {
	mu    sync.Mutex
	path  string
	Views map[string]map[string]*Row `json:"views"` // "TTTTTTTT/view" -> xuid hex -> row
	Next  uint64                     `json:"next"`
}

// Open loads path (empty: memory only).
func Open(path string) (*Store, error) {
	s := &Store{path: path, Views: map[string]map[string]*Row{}}
	if path == "" {
		return s, nil
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, s); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if s.Views == nil {
		s.Views = map[string]map[string]*Row{}
	}
	return s, nil
}

func (s *Store) saveLocked() error {
	if s.path == "" {
		return nil
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp := s.path + ".new"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func viewKey(title, view uint32) string { return fmt.Sprintf("%08X/%d", title, view) }
func xuidKey(x uint64) string           { return fmt.Sprintf("%016X", x) }

type ranked struct {
	xuid uint64
	row  *Row
}

// sorted returns a view's rows, best rating first.
func (s *Store) sorted(title, view uint32) []ranked {
	var out []ranked
	for k, r := range s.Views[viewKey(title, view)] {
		x, _ := strconv.ParseUint(k, 16, 64)
		out = append(out, ranked{x, r})
	}
	sort.Slice(out, func(i, j int) bool {
		if a, b := out[i].row.rating(), out[j].row.rating(); a != b {
			return a > b
		}
		return out[i].row.Seq < out[j].row.Seq
	})
	return out
}

func aggregate(proc uint16, old Value, have bool, v Value) Value {
	if !have || proc == procLast || old.Type == tNull {
		return v
	}
	if v.Type == tNull {
		return old
	}
	float := old.isFloat() || v.isFloat()
	f := func(x Value) float64 {
		if x.isFloat() {
			return x.float()
		}
		return float64(x.Int())
	}
	switch proc {
	case procSum:
		if float {
			return floatValue(f(old) + f(v))
		}
		return Value{v.Type, uint64(old.Int() + v.Int())}
	case procMin:
		if (float && f(v) < f(old)) || (!float && v.Int() < old.Int()) {
			return v
		}
	case procMax:
		if (float && f(v) > f(old)) || (!float && v.Int() > old.Int()) {
			return v
		}
	}
	return old
}

// Names looks up a gamertag for an OpenPak account (the account core handle).
type Names interface {
	Handle(ctx context.Context, account string) (string, error)
}

// Handler routes the /xstats leaderboard paths. Wrap it with wire.Guard: every
// request needs the gateway's AccountHeader.
type Handler struct {
	Store *Store
	Names Names // optional
	Log   *slog.Logger
	// Profile settings (service 34, settings.go): the account core and the
	// XUID-to-account map; settings answer 503 without Profiles.
	Profiles Profiles
	Accounts Accounts
}

func (h *Handler) log() *slog.Logger {
	if h.Log != nil {
		return h.Log
	}
	return slog.Default()
}

// signedIn maps the tunnel's users ("<xuid hex>:<account>,...") by XUID.
func signedIn(r *http.Request) map[uint64]string {
	out := map[uint64]string{}
	for _, u := range strings.Split(r.Header.Get(wire.UsersHeader), ",") {
		x, acct, ok := strings.Cut(strings.TrimSpace(u), ":")
		if v, err := strconv.ParseUint(x, 16, 64); ok && err == nil {
			out[v] = acct
		}
	}
	return out
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.ToLower(r.URL.Path)
	if path == "/healthz" {
		return
	}
	if r.Header.Get(wire.AccountHeader) == "" {
		http.Error(w, "no console account", http.StatusForbidden)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil || len(body) > maxBody || r.Method != http.MethodPost {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var reply []byte
	switch path {
	case "/xstats/statpost.ashx":
		err = h.post(r, body)
	case "/xstats/xstatread.ashx":
		reply, err = h.read(body)
	case "/xstats/xstatenumbyrank.ashx":
		reply, err = h.enum(body, byRank)
	case "/xstats/xstatenumbyuser.ashx":
		reply, err = h.enum(body, byUser)
	case "/xstats/xstatenumbyrating.ashx":
		reply, err = h.enum(body, byRating)
	case "/xstats/xstatestimaterankforratings.ashx":
		reply, err = h.estimate(body)
	case "/xstats/xstatsreset.ashx":
		err = h.reset(r, body)
	default:
		if h.profileSettings(w, r, body) || h.xstats(w, r, body) {
			return
		}
		h.log().Info("unhandled", "path", r.URL.Path, "body", hex.EncodeToString(body))
		http.NotFound(w, r)
		return
	}
	h.log().Info("xstats", "path", r.URL.Path, "users", r.Header.Get(wire.UsersHeader),
		"body", hex.EncodeToString(body), "reply_len", len(reply), "err", err)
	if err != nil {
		code := http.StatusBadRequest
		if err == errNotYours {
			code = http.StatusForbidden
		}
		http.Error(w, err.Error(), code)
		return
	}
	w.Header().Set("Content-Type", ContentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(reply)))
	w.Write(reply)
}

type httpErr string

func (e httpErr) Error() string { return string(e) }

const errNotYours = httpErr("user is not signed in on this console")

// post is XRL 0x705: u32 title, u16 n, n x {u16 proc, u16 m, m x value}. A column
// proc's params are XUID (7), view (3), u16 0, then (u16 column, value) pairs
// (FUN_81935f70, FUN_819223b0).
// ponytail: any console may write any XUID's row, as Live let a session host
// write its players' stats; arbitration checks would go here.
func (h *Handler) post(r *http.Request, body []byte) error {
	rd := &reader{b: body}
	title := rd.u32()
	n := int(rd.u16())
	type write struct {
		proc uint16
		xuid uint64
		view uint32
		cols []uint16
		vals []Value
	}
	var writes []write
	for i := 0; i < n && rd.err == nil; i++ {
		proc := rd.u16()
		m := int(rd.u16())
		ps := make([]Value, 0, m)
		for j := 0; j < m && rd.err == nil; j++ {
			ps = append(ps, rd.value())
		}
		switch proc {
		case procLast, procSum, procMin, procMax:
		default:
			h.log().Info("statpost proc not served", "proc", fmt.Sprintf("%#x", proc), "params", len(ps))
			continue
		}
		if len(ps) < 3 || ps[0].Type != tU64 || ps[1].Type != tInt32 || (len(ps)-3)%2 != 0 {
			return fmt.Errorf("xstats: proc %#x: bad params", proc)
		}
		wr := write{proc: proc, xuid: ps[0].Bits, view: uint32(ps[1].Bits)}
		for j := 3; j < len(ps); j += 2 {
			if ps[j].Type != tU16 {
				return fmt.Errorf("xstats: proc %#x: column id type %d", proc, ps[j].Type)
			}
			wr.cols = append(wr.cols, uint16(ps[j].Bits))
			wr.vals = append(wr.vals, ps[j+1])
		}
		writes = append(writes, wr)
	}
	if rd.err != nil {
		return rd.err
	}
	users := signedIn(r)
	names := map[uint64]string{}
	if h.Names != nil {
		for _, wr := range writes {
			if acct, ok := users[wr.xuid]; ok && acct != "" {
				if _, done := names[wr.xuid]; !done {
					gt, err := h.Names.Handle(r.Context(), acct)
					if err != nil {
						h.log().Warn("gamertag lookup", "account", acct, "err", err)
					}
					names[wr.xuid] = gt
				}
			}
		}
	}
	s := h.Store
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, wr := range writes {
		k := viewKey(title, wr.view)
		if s.Views[k] == nil {
			s.Views[k] = map[string]*Row{}
		}
		row := s.Views[k][xuidKey(wr.xuid)]
		if row == nil {
			s.Next++
			row = &Row{Cols: map[uint16]Value{}, Seq: s.Next}
			s.Views[k][xuidKey(wr.xuid)] = row
		}
		if gt := names[wr.xuid]; gt != "" {
			row.Gamertag = gt
		}
		for i, c := range wr.cols {
			old, have := row.Cols[c]
			row.Cols[c] = aggregate(wr.proc, old, have, wr.vals[i])
		}
	}
	return s.saveLocked()
}

type spec struct {
	view uint32
	cols []uint16
}

func (rd *reader) specs() []spec {
	n := rd.count(8)
	if n > maxSpecs {
		rd.err = httpErr("xstats: too many stats specs")
		return nil
	}
	out := make([]spec, 0, n)
	for i := 0; i < n && rd.err == nil; i++ {
		sp := spec{view: rd.u32()}
		m := rd.count(2)
		if m > maxColumns {
			rd.err = httpErr("xstats: too many columns")
			return nil
		}
		for j := 0; j < m && rd.err == nil; j++ {
			sp.cols = append(sp.cols, rd.u16())
		}
		out = append(out, sp)
	}
	return out
}

// writeView writes {u64 view, u32 total rows, u32 n, rows}.
func writeView(w *writer, sp spec, total int, rows []ranked, firstRank int) {
	w.u64(uint64(sp.view))
	w.u32(uint32(total))
	w.u32(uint32(len(rows)))
	for i, e := range rows {
		writeRow(w, sp, e, firstRank+i)
	}
}

// writeRow writes {u64 xuid, u32 rank, i64 rating, u32 gamertag length, gamertag,
// u32 columns, columns}, the columns in the spec's order (null when the row has none).
func writeRow(w *writer, sp spec, e ranked, rank int) {
	w.u64(e.xuid)
	w.u32(uint32(rank))
	w.u64(uint64(e.row.rating()))
	gt := e.row.Gamertag
	if len(gt) > 15 {
		gt = gt[:15]
	}
	w.u32(uint32(len(gt)))
	*w = append(*w, gt...)
	w.u32(uint32(len(sp.cols)))
	for _, c := range sp.cols {
		w.u16(c)
		v, ok := e.row.Cols[c]
		switch {
		case c == colRank:
			v = Value{tInt32, uint64(uint32(rank))}
		case c == colRating:
			v = Value{tInt64, uint64(e.row.rating())}
		case !ok || c == colGamertag:
			v = Value{}
		}
		w.value(v)
	}
}

// read is XRL 0x703: u32 title, u32 n, u64 xuids[n], specs. Reply: u32 views.
func (h *Handler) read(body []byte) ([]byte, error) {
	rd := &reader{b: body}
	title := rd.u32()
	n := rd.count(8)
	if n > 101 {
		return nil, httpErr("xstats: too many users")
	}
	want := map[uint64]bool{}
	for i := 0; i < n && rd.err == nil; i++ {
		want[rd.u64()] = true
	}
	specs := rd.specs()
	if rd.err != nil {
		return nil, rd.err
	}
	h.Store.mu.Lock()
	defer h.Store.mu.Unlock()
	var w writer
	w.u32(uint32(len(specs)))
	for _, sp := range specs {
		all := h.Store.sorted(title, sp.view)
		n := 0
		for _, e := range all {
			if want[e.xuid] {
				n++
			}
		}
		w.u64(uint64(sp.view))
		w.u32(uint32(len(all)))
		w.u32(uint32(n))
		for i, e := range all {
			if want[e.xuid] {
				writeRow(&w, sp, e, i+1)
			}
		}
	}
	return w, nil
}

type pivot int

const (
	byRank pivot = iota
	byUser
	byRating
)

// enum is XRL 0x70E/0x70D/0x70F: u32 title, u64 pivot (start rank, XUID or
// rating), u32 rows, specs. Reply: u16 views.
func (h *Handler) enum(body []byte, kind pivot) ([]byte, error) {
	rd := &reader{b: body}
	title := rd.u32()
	pv := rd.u64()
	n := int(rd.u32())
	specs := rd.specs()
	if rd.err != nil {
		return nil, rd.err
	}
	if n <= 0 || n > maxRows {
		n = maxRows
	}
	h.Store.mu.Lock()
	defer h.Store.mu.Unlock()
	var w writer
	w.u16(uint16(len(specs)))
	for _, sp := range specs {
		all := h.Store.sorted(title, sp.view)
		start := 0
		switch kind {
		case byRank:
			if pv > 0 {
				start = int(min(pv, uint64(len(all)+1))) - 1
			}
		case byUser:
			start = len(all) // a user without a row gets an empty page
			for i, e := range all {
				if e.xuid == pv {
					start = max(0, i-n/2)
					break
				}
			}
		case byRating:
			start = sort.Search(len(all), func(i int) bool { return all[i].row.rating() <= int64(pv) })
		}
		end := min(len(all), start+n)
		writeView(&w, sp, len(all), all[start:end], start+1)
	}
	return w, nil
}

// estimate is XRL 0x704: u32 title, u32 n, n x {u32 view, i64 rating}. Reply:
// u32 n, u32 ranks[n] (1 + rows rated higher).
func (h *Handler) estimate(body []byte) ([]byte, error) {
	rd := &reader{b: body}
	title := rd.u32()
	n := rd.count(12)
	if n > 101 {
		return nil, httpErr("xstats: too many ratings")
	}
	type q struct {
		view   uint32
		rating int64
	}
	var qs []q
	for i := 0; i < n && rd.err == nil; i++ {
		qs = append(qs, q{rd.u32(), int64(rd.u64())})
	}
	if rd.err != nil {
		return nil, rd.err
	}
	h.Store.mu.Lock()
	defer h.Store.mu.Unlock()
	var w writer
	w.u32(uint32(len(qs)))
	for _, x := range qs {
		rank := 1
		for _, r := range h.Store.Views[viewKey(title, x.view)] {
			if r.rating() > x.rating {
				rank++
			}
		}
		w.u32(uint32(rank))
	}
	return w, nil
}

// reset is XRL 0x706: u32 title, u64 xuid, u32 view (0 = every view of the
// title). Only a user signed in on the requesting console can be reset.
func (h *Handler) reset(r *http.Request, body []byte) error {
	rd := &reader{b: body}
	title := rd.u32()
	xuid := rd.u64()
	view := rd.u32()
	if rd.err != nil {
		return rd.err
	}
	if _, ok := signedIn(r)[xuid]; !ok {
		return errNotYours
	}
	s := h.Store
	s.mu.Lock()
	defer s.mu.Unlock()
	prefix := fmt.Sprintf("%08X/", title)
	for k, rows := range s.Views {
		if k == viewKey(title, view) || (view == 0 && strings.HasPrefix(k, prefix)) {
			delete(rows, xuidKey(xuid))
		}
	}
	return s.saveLocked()
}
