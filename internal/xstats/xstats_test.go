package xstats

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"net/http/httptest"
	"testing"

	"github.com/openpak/x360-common/wire"
	"github.com/openpak/x360-xstats/internal/fakeleaderboards"
	pb "github.com/openpak/x360-xstats/internal/leaderboardspb"
)

const lbKey = "test-internal-key"

// newHandler is a handler over a fake leaderboards core. A new handler over the same fake and
// store is a restart of the service.
func newHandler(t *testing.T, f *fakeleaderboards.Fake, s *Store) *Handler {
	t.Helper()
	lb, conn, err := DialLeaderboards("passthrough:///fake", lbKey, f.Serve(t)...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return &Handler{Store: s, LB: lb, Names: names{"acc-a": "Alpha", "acc-b": "Bravo"},
		Accounts: accts{0x0009000000000001: "acc-a", 0x0009000000000002: "acc-b"}}
}

const title = 0x4D5307E6

type names map[string]string

func (n names) Handle(_ context.Context, a string) (string, error) { return n[a], nil }

// proc builds one column proc the way FUN_81935f70/FUN_819223b0 do.
func proc(w *writer, id uint16, xuid uint64, view uint32, cols ...any) {
	w.u16(id)
	w.u16(uint16(3 + len(cols)))
	w.value(Value{tU64, xuid})
	w.value(Value{tInt32, uint64(view)})
	w.value(Value{tU16, 0})
	for i := 0; i < len(cols); i += 2 {
		w.value(Value{tU16, uint64(cols[i].(int))})
		w.value(cols[i+1].(Value))
	}
}

func statpost(procs func(*writer), n int) []byte {
	var w writer
	w.u32(title)
	w.u16(uint16(n))
	procs(&w)
	return w
}

func readBody(view uint32, cols []uint16, xuids ...uint64) []byte {
	var w writer
	w.u32(title)
	w.u32(uint32(len(xuids)))
	for _, x := range xuids {
		w.u64(x)
	}
	w.u32(1)
	w.u32(view)
	w.u32(uint32(len(cols)))
	for _, c := range cols {
		w.u16(c)
	}
	return w
}

func enumBody(pv uint64, rows uint32, view uint32, cols []uint16) []byte {
	var w writer
	w.u32(title)
	w.u64(pv)
	w.u32(rows)
	w.u32(1)
	w.u32(view)
	w.u32(uint32(len(cols)))
	for _, c := range cols {
		w.u16(c)
	}
	return w
}

func call(t *testing.T, h *Handler, users, path string, body []byte) []byte {
	t.Helper()
	req := httptest.NewRequest("POST", path, bytes.NewReader(body))
	req.Header.Set(wire.AccountHeader, "console@xbox.com")
	req.Header.Set(wire.UsersHeader, users)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
	}
	return rec.Body.Bytes()
}

func i64(v int64) Value { return Value{tInt64, uint64(v)} }
func i32(v int32) Value { return Value{tInt32, uint64(uint32(v))} }

func TestLeaderboard(t *testing.T) {
	f := fakeleaderboards.New(lbKey)
	s := NewMemoryStore()
	h := newHandler(t, f, s)
	const a, b = 0x0009000000000001, 0x0009000000000002
	users := "0009000000000001:acc-a,0009000000000002:acc-b"

	// a: rating 100 (Max), kills 3 (Sum) twice -> 6; b: rating 250
	for i := 0; i < 2; i++ {
		call(t, h, users, "/xstats/statpost.ashx", statpost(func(w *writer) {
			proc(w, procMax, a, 1, colRating, i64(100))
			proc(w, procSum, a, 1, 2, i32(3))
			proc(w, procLast, b, 1, colRating, i64(250))
			proc(w, 0x800D, a, 0) // skill proc: ignored
		}, 4))
	}
	call(t, h, users, "/xstats/statpost.ashx", statpost(func(w *writer) { proc(w, procMax, a, 1, colRating, i64(50)) }, 1))

	checkRead := func(h *Handler) {
		t.Helper()
		rep := call(t, h, users, "/xstats/xstatread.ashx", readBody(1, []uint16{2, colRank}, a))
		var want writer
		want.u32(1)                     // views
		want.u64(1)                     // view id
		want.u32(2)                     // total rows
		want.u32(1)                     // rows
		want.u64(a)                     // xuid
		want.u32(2)                     // rank
		want.u64(100)                   // rating (Max kept 100 over 50)
		want.u32(5)                     // gamertag
		want = append(want, "Alpha"...) //
		want.u32(2)                     // columns
		want.u16(2)
		want.value(i32(6)) // kills summed
		want.u16(colRank)
		want.value(i32(2))
		if !bytes.Equal(rep, want) {
			t.Fatalf("read:\n got %x\nwant %x", rep, []byte(want))
		}
	}
	checkRead(h)

	rep := call(t, h, users, "/xstats/xstatenumbyrank.ashx", enumBody(1, 10, 1, nil))
	if binary.LittleEndian.Uint16(rep) != 1 || binary.LittleEndian.Uint32(rep[14:]) != 2 ||
		binary.LittleEndian.Uint64(rep[18:]) != b {
		t.Fatalf("enum by rank: %x", rep)
	}
	rep = call(t, h, users, "/xstats/xstatenumbyrating.ashx", enumBody(120, 10, 1, nil))
	if binary.LittleEndian.Uint32(rep[14:]) != 1 || binary.LittleEndian.Uint64(rep[18:]) != a {
		t.Fatalf("enum by rating: %x", rep)
	}
	// by user: a is position 2 of 2, a page of 2 around it starts at 1
	rep = call(t, h, users, "/xstats/xstatenumbyuser.ashx", enumBody(a, 2, 1, nil))
	if binary.LittleEndian.Uint32(rep[14:]) != 2 {
		t.Fatalf("enum by user: %x", rep)
	}

	var est writer
	est.u32(title)
	est.u32(1)
	est.u32(1)
	est.u64(200)
	if rep = call(t, h, users, "/xstats/xstatestimaterankforratings.ashx", est); !bytes.Equal(rep, []byte{1, 0, 0, 0, 2, 0, 0, 0}) {
		t.Fatalf("estimate: %x", rep)
	}

	// restart: a new handler over the same core reads the same rows
	h2 := newHandler(t, f, s)
	checkRead(h2)
	h = h2

	// reset b (signed in) and refuse a foreign XUID
	var rs writer
	rs.u32(title)
	rs.u64(b)
	rs.u32(1)
	call(t, h, users, "/xstats/xstatsreset.ashx", rs)
	rep = call(t, h, users, "/xstats/xstatenumbyrank.ashx", enumBody(1, 10, 1, nil))
	if binary.LittleEndian.Uint32(rep[14:]) != 1 || binary.LittleEndian.Uint64(rep[18:]) != a {
		t.Fatalf("reset kept the row: %x", rep)
	}
	req := httptest.NewRequest("POST", "/xstats/xstatsreset.ashx", bytes.NewReader(rs))
	req.Header.Set(wire.AccountHeader, "other@xbox.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("foreign reset: %d", rec.Code)
	}
}

// TestBoardPolicy: the core board is created from the first write with the rating as score column
// and the posted aggregations.
func TestBoardPolicy(t *testing.T) {
	f := fakeleaderboards.New(lbKey)
	h := newHandler(t, f, NewMemoryStore())
	const a = 0x0009000000000001
	call(t, h, "0009000000000001:acc-a", "/xstats/statpost.ashx", statpost(func(w *writer) {
		proc(w, procMax, a, 3, colRating, i64(9))
		proc(w, procSum, a, 3, 4, Value{tDouble, math.Float64bits(1.5)})
	}, 2))
	p := f.Policy("4D5307E6", "3")
	if p == nil || p.ScoreColumn != "65534" || p.Order != "desc" || p.ColumnAggregations["65534"] != "max" || p.ColumnAggregations["4.5"] != "sum" {
		t.Fatalf("policy: %+v", p)
	}
}

// TestCoreDown: the core being unreachable is a 503, not a 400, and nothing is stored locally.
func TestCoreDown(t *testing.T) {
	f := fakeleaderboards.New(lbKey)
	f.Down = true
	h := newHandler(t, f, NewMemoryStore())
	req := httptest.NewRequest("POST", "/xstats/xstatenumbyrank.ashx", bytes.NewReader(enumBody(1, 10, 1, nil)))
	req.Header.Set(wire.AccountHeader, "console@xbox.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 503 {
		t.Fatalf("down: %d", rec.Code)
	}
}

// TestMigrateLegacy: old rows move into the core once, rows without an account stay in the
// document, a repeat (or a restart) changes nothing and never overwrites live data.
func TestMigrateLegacy(t *testing.T) {
	f := fakeleaderboards.New(lbKey)
	s := NewMemoryStore()
	rate := func(n int64) map[uint16]Value { return map[uint16]Value{colRating: i64(n), 2: i32(7)} }
	s.Views[viewKey(title, 1)] = map[string]*Row{
		xuidKey(0x0009000000000001): {Gamertag: "Alpha", Cols: rate(100), Seq: 1},
		xuidKey(0x0009000000000002): {Gamertag: "Bravo", Cols: rate(250), Seq: 2},
		xuidKey(0x0009000000000099): {Gamertag: "Ghost", Cols: rate(5), Seq: 3}, // no account
	}
	h := newHandler(t, f, s)
	st, err := h.MigrateLegacy(context.Background())
	if err != nil || st.Rows != 3 || st.Migrated != 2 || st.NoAccount != 1 || st.Views != 1 || !s.Migrated {
		t.Fatalf("%v %+v", err, st)
	}
	if len(s.Views[viewKey(title, 1)]) != 3 {
		t.Fatal("legacy rows were dropped")
	}
	// live data after the migration: a improves, a restart migrates nothing again
	const a = 0x0009000000000001
	users := "0009000000000001:acc-a"
	call(t, h, users, "/xstats/statpost.ashx", statpost(func(w *writer) { proc(w, procMax, a, 1, colRating, i64(900)) }, 1))
	h2 := newHandler(t, f, s)
	if st, err = h2.MigrateLegacy(context.Background()); err != nil || st.Rows != 0 {
		t.Fatalf("repeat: %v %+v", err, st)
	}
	// even with the marker lost, an existing core row is not overwritten
	s.Migrated = false
	if st, err = h2.MigrateLegacy(context.Background()); err != nil || st.Existing != 2 || st.Migrated != 0 {
		t.Fatalf("existing: %v %+v", err, st)
	}
	rep := call(t, h2, users, "/xstats/xstatenumbyrank.ashx", enumBody(1, 10, 1, []uint16{2}))
	if binary.LittleEndian.Uint32(rep[14:]) != 2 || binary.LittleEndian.Uint64(rep[18:]) != a {
		t.Fatalf("top row is not the live 900: %x", rep)
	}
	_ = pb.Entry{}
}
