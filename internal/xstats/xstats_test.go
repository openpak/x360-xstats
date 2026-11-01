package xstats

import (
	"bytes"
	"context"
	"encoding/binary"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/openpak/x360-common/wire"
)

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
	state := filepath.Join(t.TempDir(), "stats.json")
	s, _ := Open(state)
	h := &Handler{Store: s, Names: names{"acc-a": "Alpha", "acc-b": "Bravo"}}
	const a, b = 0x0009000000000001, 0x0009000000000002
	users := "0009000000000001:acc-a,0009000000000002:acc-b"

	// a: rating 100 (Max), kills 3 (Sum) twice -> 6, name "x" via Last; b: rating 250
	for i := 0; i < 2; i++ {
		call(t, h, users, "/xstats/statpost.ashx", statpost(func(w *writer) {
			proc(w, procMax, a, 1, colRating, i64(100))
			proc(w, procSum, a, 1, 2, i32(3))
			proc(w, procLast, b, 1, colRating, i64(250))
			proc(w, 0x800D, a, 0) // skill proc: ignored
		}, 4))
	}
	call(t, h, users, "/xstats/statpost.ashx", statpost(func(w *writer) { proc(w, procMax, a, 1, colRating, i64(50)) }, 1))

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

	rep = call(t, h, users, "/xstats/xstatenumbyrank.ashx", enumBody(1, 10, 1, nil))
	if binary.LittleEndian.Uint16(rep) != 1 || binary.LittleEndian.Uint32(rep[14:]) != 2 ||
		binary.LittleEndian.Uint64(rep[18:]) != b {
		t.Fatalf("enum by rank: %x", rep)
	}
	rep = call(t, h, users, "/xstats/xstatenumbyrating.ashx", enumBody(120, 10, 1, nil))
	if binary.LittleEndian.Uint32(rep[14:]) != 1 || binary.LittleEndian.Uint64(rep[18:]) != a {
		t.Fatalf("enum by rating: %x", rep)
	}

	var est writer
	est.u32(title)
	est.u32(1)
	est.u32(1)
	est.u64(200)
	if rep = call(t, h, users, "/xstats/xstatestimaterankforratings.ashx", est); !bytes.Equal(rep, []byte{1, 0, 0, 0, 2, 0, 0, 0}) {
		t.Fatalf("estimate: %x", rep)
	}

	// reload from disk, then reset b (signed in) and refuse a foreign XUID
	s2, err := Open(state)
	if err != nil || len(s2.Views[viewKey(title, 1)]) != 2 {
		t.Fatalf("reload: %v %v", err, s2.Views)
	}
	h.Store = s2
	var rs writer
	rs.u32(title)
	rs.u64(b)
	rs.u32(1)
	call(t, h, users, "/xstats/xstatsreset.ashx", rs)
	if len(s2.Views[viewKey(title, 1)]) != 1 {
		t.Fatal("reset kept the row")
	}
	req := httptest.NewRequest("POST", "/xstats/xstatsreset.ashx", bytes.NewReader(rs))
	req.Header.Set(wire.AccountHeader, "other@xbox.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("foreign reset: %d", rec.Code)
	}
}
