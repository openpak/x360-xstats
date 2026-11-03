package xstats

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/openpak/x360-xstats/internal/rankingsapi"
)

func rankedStore() *Store {
	s := NewMemoryStore()
	rate := func(n int64) map[uint16]Value { return map[uint16]Value{colRating: {Type: tInt64, Bits: uint64(n)}} }
	s.Views[viewKey(title, 2)] = map[string]*Row{
		xuidKey(0xE000000000000001): {Cols: rate(500), Seq: 1},
		xuidKey(0xE000000000000002): {Cols: rate(900), Seq: 2},
		xuidKey(0xE000000000000003): {Cols: rate(500), Seq: 3},
	}
	s.Views[viewKey(title, 5)] = map[string]*Row{}
	return s
}

func TestRankingBoards(t *testing.T) {
	b, err := Rankings{Store: rankedStore()}.Boards(context.Background())
	if err != nil || len(b) != 1 || b[0].ID != "1297287142-2" || b[0].Name != "Game 1297287142, Board 2" ||
		b[0].Kind != "rating" || b[0].Order != "desc" {
		t.Fatalf("%v %+v", err, b)
	}
}

func TestRankingRows(t *testing.T) {
	h := rankingsapi.Handler("x360", Rankings{Store: rankedStore()})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/internal/rankings/boards/1297287142-2?limit=2&around=16140901064495857667", nil))
	var p rankingsapi.Page
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || rec.Code != 200 {
		t.Fatalf("%d %v %s", rec.Code, err, rec.Body)
	}
	if p.Total != 3 || len(p.Rows) != 2 || p.Rows[0].PID != "16140901064495857666" || p.Rows[0].Score != 900 ||
		p.Rows[1].PID != "16140901064495857665" || len(p.Around) != 3 || p.Around[2].Rank != 3 {
		t.Fatalf("page: %+v", p)
	}
	for _, id := range []string{"1297287142-5", "1297287142-9", "x", "1297287142"} {
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/internal/rankings/boards/"+id, nil))
		if rec.Code != 404 {
			t.Fatalf("%s: %d", id, rec.Code)
		}
	}
}

func TestRankingsPerTitle(t *testing.T) {
	st := rankedStore()
	other := uint32(0x4D53085B)
	st.Views[viewKey(other, 1)] = map[string]*Row{xuidKey(0xE000000000000009): {Cols: map[uint16]Value{colRating: {Type: tInt64, Bits: 7}}, Seq: 1}}
	h := RankingsHandler("x360", st)
	get := func(path string, v any) int {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if v != nil {
			json.Unmarshal(rec.Body.Bytes(), v)
		}
		return rec.Code
	}
	var bl rankingsapi.BoardList
	if get("/internal/rankings/boards", &bl) != 200 || len(bl.Boards) != 2 {
		t.Fatalf("all: %+v", bl)
	}
	if get("/t/4D5307E6/internal/rankings/boards", &bl) != 200 || len(bl.Boards) != 1 || bl.Boards[0].ID != "1297287142-2" || bl.Network != "x360" {
		t.Fatalf("hex: %+v", bl)
	}
	if get("/t/1297287259/internal/rankings/boards", &bl) != 200 || len(bl.Boards) != 1 || bl.Boards[0].ID != "1297287259-1" {
		t.Fatalf("decimal: %+v", bl)
	}
	var pg rankingsapi.Page
	if get("/t/4D53085B/internal/rankings/boards/1297287259-1", &pg) != 200 || pg.Total != 1 {
		t.Fatalf("rows: %+v", pg)
	}
	if get("/t/4D53085B/internal/rankings/boards/1297287142-2", nil) != 404 {
		t.Fatal("another title's board must be 404 here")
	}
	if get("/t/zzz/internal/rankings/boards", nil) != 404 {
		t.Fatal("bad title")
	}
	if get("/t/00000001/internal/rankings/boards", &bl) != 200 || len(bl.Boards) != 0 {
		t.Fatalf("empty title: %+v", bl)
	}
}
