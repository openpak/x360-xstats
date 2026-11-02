package xstats

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/openpak/x360-xstats/internal/rankingsapi"
)

func rankedStore() *Store {
	s, _ := Open("")
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
	b, err := Rankings{rankedStore()}.Boards(context.Background())
	if err != nil || len(b) != 1 || b[0].ID != "1297287142-2" || b[0].Name != "Game 1297287142, Board 2" ||
		b[0].Kind != "rating" || b[0].Order != "desc" {
		t.Fatalf("%v %+v", err, b)
	}
}

func TestRankingRows(t *testing.T) {
	h := rankingsapi.Handler("x360", Rankings{rankedStore()})
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
