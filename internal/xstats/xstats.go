// Package xstats serves Xbox Live leaderboards (Live service 7): the XRL requests
// XAM sends for XSessionWriteStats/XUserWriteStats (statpost), XUserReadStats and
// the stats enumerators. Layouts come from the XAM 17559 schemas and builders
// (docs/protocol.md).
//
// No per-title configuration is needed: XAM reads the title's own stats views
// (its SPA) and sends one procedure per view column aggregation, with the
// column ids, so the server only translates. The rows live in the neutral
// leaderboards core (board.go); this package keeps the wire.
package xstats

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	pb "github.com/openpak/x360-xstats/internal/leaderboardspb"

	"github.com/openpak/x360-common/pgstate"
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

// Row is one user's row in a view as the pre-core store kept it. Only the one-time migration
// into the leaderboards core reads these (migrate.go).
type Row struct {
	Gamertag string           `json:"gamertag,omitempty"`
	Cols     map[uint16]Value `json:"cols"`
	Seq      uint64           `json:"seq"` // first-write order, breaks rating ties
}

func (r *Row) rating() int64 { return r.Cols[colRating].Int() }

// Store writes the profile documents (service 34/35 sync, sync.go) as one document to
// Postgres (schema x360_xstats) after each change; a nil pg is memory only (tests). The
// leaderboard rows are not here any more: they live in the leaderboards core. Views, Next
// and Migrated are the old rows, kept in the document after their migration and never dropped.
type Store struct {
	mu       sync.Mutex
	pg       *pgstate.Store
	Views    map[string]map[string]*Row `json:"views,omitempty"` // legacy "TTTTTTTT/view" -> xuid hex -> row
	Next     uint64                     `json:"next,omitempty"`
	Migrated bool                       `json:"lb_migrated,omitempty"` // Views are in the core
	Profiles map[string]*Profile        `json:"profiles,omitempty"`    // xuid hex -> synced titles, achievements, awards (sync.go)
}

// Schema is the Postgres schema xstats owns inside the x360 database.
const (
	Schema   = "x360_xstats"
	stateKey = "state"
)

// NewMemoryStore is a store that forgets on exit (tests).
func NewMemoryStore() *Store { return &Store{Views: map[string]map[string]*Row{}} }

// Open opens the Postgres store in schema. When the database holds no state yet
// and legacyFile exists, it is imported once and left in place. An empty dsn is
// an error.
func Open(ctx context.Context, dsn, schema, legacyFile string) (*Store, error) {
	pg, err := pgstate.Open(ctx, dsn, schema)
	if err != nil {
		return nil, err
	}
	s := NewMemoryStore()
	s.pg = pg
	found, err := pg.Load(ctx, stateKey, s)
	if err == nil && !found && legacyFile != "" {
		b, rerr := os.ReadFile(legacyFile)
		switch {
		case rerr == nil:
			if err = json.Unmarshal(b, s); err == nil {
				err = s.saveLocked()
			}
			if err == nil {
				slog.Info("imported stats.json into Postgres", "views", len(s.Views), "profiles", len(s.Profiles))
			}
		case !os.IsNotExist(rerr):
			err = rerr
		}
	}
	if err != nil {
		pg.Close()
		return nil, err
	}
	if s.Views == nil {
		s.Views = map[string]map[string]*Row{}
	}
	return s, nil
}

// Close releases the database pool.
func (s *Store) Close() {
	if s.pg != nil {
		s.pg.Close()
	}
}

func (s *Store) saveLocked() error {
	if s.pg == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return s.pg.Save(ctx, stateKey, s)
}

func viewKey(title, view uint32) string { return fmt.Sprintf("%08X/%d", title, view) }
func xuidKey(x uint64) string           { return fmt.Sprintf("%016X", x) }

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
	// LB is the leaderboards core (gRPC); the leaderboard requests answer 503 without it.
	LB pb.LeaderboardsClient
	// Profile settings (service 34, settings.go): the account core and the
	// XUID-to-account map; settings answer 503 without Profiles. Leaderboard reads need
	// Accounts to turn core accounts back into XUIDs.
	Profiles Profiles
	Accounts Accounts

	board boardState
	ids   idCache
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

// The request line (method, path, status, ms) is written by httplog around this handler. Bodies,
// users and queries are never logged.
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
		reply, err = h.read(r.Context(), body)
	case "/xstats/xstatenumbyrank.ashx":
		reply, err = h.enum(r.Context(), body, byRank)
	case "/xstats/xstatenumbyuser.ashx":
		reply, err = h.enum(r.Context(), body, byUser)
	case "/xstats/xstatenumbyrating.ashx":
		reply, err = h.enum(r.Context(), body, byRating)
	case "/xstats/xstatestimaterankforratings.ashx":
		reply, err = h.estimate(r.Context(), body)
	case "/xstats/xstatsreset.ashx":
		err = h.reset(r, body)
	default:
		if h.profileSettings(w, r, body) || h.xstats(w, r, body) {
			return
		}
		http.NotFound(w, r)
		return
	}
	if err != nil {
		code := http.StatusBadRequest
		var ue upstreamErr
		switch {
		case err == errNotYours:
			code = http.StatusForbidden
		case errors.As(err, &ue):
			code = http.StatusServiceUnavailable
			h.log().Warn("leaderboards core", "path", r.URL.Path, "err", ue.err)
			err = errors.New("leaderboards unavailable")
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

// upstreamErr marks a failure of the core or the account service (answered 503).
type upstreamErr struct{ err error }

func (e upstreamErr) Error() string { return e.err.Error() }
func (e upstreamErr) Unwrap() error { return e.err }

// post is XRL 0x705: u32 title, u16 n, n x {u16 proc, u16 m, m x value}. A column
// proc's params are XUID (7), view (3), u16 0, then (u16 column, value) pairs
// (FUN_81935f70, FUN_819223b0).
// ponytail: any console may write any XUID's row, as Live let a session host
// write its players' stats; arbitration checks would go here.
func (h *Handler) post(r *http.Request, body []byte) error {
	rd := &reader{b: body}
	title := rd.u32()
	n := int(rd.u16())
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
			continue // not served: skill procs
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
	return h.writeStats(r.Context(), title, signedIn(r), writes)
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
func writeView(w *writer, sp spec, total int, rows []ranked) {
	w.u64(uint64(sp.view))
	w.u32(uint32(total))
	w.u32(uint32(len(rows)))
	for _, e := range rows {
		writeRow(w, sp, e)
	}
}

// writeRow writes {u64 xuid, u32 rank, i64 rating, u32 gamertag length, gamertag,
// u32 columns, columns}, the columns in the spec's order (null when the row has none). The
// rank is the entry's unique position, the order XAM's enumerators page by.
func writeRow(w *writer, sp spec, e ranked) {
	rank := int(e.e.Position)
	w.u64(e.xuid)
	w.u32(uint32(rank))
	w.u64(uint64(e.e.Score))
	gt := e.e.DisplayName
	if len(gt) > 15 {
		gt = gt[:15]
	}
	w.u32(uint32(len(gt)))
	*w = append(*w, gt...)
	w.u32(uint32(len(sp.cols)))
	for _, c := range sp.cols {
		w.u16(c)
		v, ok := e.cols[c]
		switch {
		case c == colRank:
			v = Value{tInt32, uint64(uint32(rank))}
		case c == colRating:
			v = Value{tInt64, uint64(e.e.Score)}
		case !ok || c == colGamertag:
			v = Value{}
		}
		w.value(v)
	}
}

// read is XRL 0x703: u32 title, u32 n, u64 xuids[n], specs. Reply: u32 views.
func (h *Handler) read(ctx context.Context, body []byte) ([]byte, error) {
	rd := &reader{b: body}
	title := rd.u32()
	n := rd.count(8)
	if n > 101 {
		return nil, httpErr("xstats: too many users")
	}
	var xuids []uint64
	for i := 0; i < n && rd.err == nil; i++ {
		xuids = append(xuids, rd.u64())
	}
	specs := rd.specs()
	if rd.err != nil {
		return nil, rd.err
	}
	lb, err := h.lb()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	var accts []string
	for _, x := range xuids {
		a, ok, err := h.accountOf(ctx, x)
		if err != nil {
			return nil, err
		}
		if ok {
			accts = append(accts, a)
		}
	}
	var w writer
	w.u32(uint32(len(specs)))
	for _, sp := range specs {
		var rows []ranked
		var total int64
		if len(accts) > 0 {
			resp, err := lb.ByAccounts(ctx, &pb.ByAccountsRequest{Ref: ref(title, sp.view), Accounts: accts})
			if err != nil {
				return nil, up(err)
			}
			total = resp.Total
			if rows, err = h.rank(ctx, resp.Entries); err != nil {
				return nil, err
			}
		} else if total, err = h.total(ctx, lb, title, sp.view); err != nil {
			return nil, err
		}
		w.u64(uint64(sp.view))
		w.u32(uint32(total))
		w.u32(uint32(len(rows)))
		for _, e := range rows {
			writeRow(&w, sp, e)
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
func (h *Handler) enum(ctx context.Context, body []byte, kind pivot) ([]byte, error) {
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
	lb, err := h.lb()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	var userAcct string
	var userKnown bool
	if kind == byUser {
		if userAcct, userKnown, err = h.accountOf(ctx, pv); err != nil {
			return nil, err
		}
	}
	var w writer
	w.u16(uint16(len(specs)))
	for _, sp := range specs {
		total, err := h.total(ctx, lb, title, sp.view)
		if err != nil {
			return nil, err
		}
		start := int64(1) // 1-based position
		switch kind {
		case byRank:
			if pv > 0 {
				start = int64(min(pv, uint64(total+1)))
			}
		case byUser:
			start = total + 1 // a user without a row gets an empty page
			if userKnown {
				resp, err := lb.ByAccounts(ctx, &pb.ByAccountsRequest{Ref: ref(title, sp.view), Accounts: []string{userAcct}})
				if err != nil {
					return nil, up(err)
				}
				for _, e := range resp.Entries {
					if e.Sub == 0 {
						start = max(1, e.Position-int64(n/2))
						break
					}
				}
			}
		case byRating:
			if start, err = firstAtOrBelow(ctx, lb, title, sp.view, total, int64(pv)); err != nil {
				return nil, err
			}
		}
		var rows []ranked
		if start <= total {
			v, err := h.rangeView(ctx, lb, title, sp.view, start, n)
			if err != nil {
				return nil, err
			}
			rows = v.rows
		}
		writeView(&w, sp, int(total), rows)
	}
	return w, nil
}

// estimate is XRL 0x704: u32 title, u32 n, n x {u32 view, i64 rating}. Reply:
// u32 n, u32 ranks[n] (1 + rows rated higher).
func (h *Handler) estimate(ctx context.Context, body []byte) ([]byte, error) {
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
	lb, err := h.lb()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	var w writer
	w.u32(uint32(len(qs)))
	for _, x := range qs {
		total, err := h.total(ctx, lb, title, x.view)
		if err != nil {
			return nil, err
		}
		rank, err := firstAtOrBelow(ctx, lb, title, x.view, total, x.rating)
		if err != nil {
			return nil, err
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
	acct, ok := signedIn(r)[xuid]
	if !ok {
		return errNotYours
	}
	return h.resetBoards(r.Context(), title, view, acct)
}
