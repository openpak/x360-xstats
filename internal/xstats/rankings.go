package xstats

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/openpak/x360-xstats/internal/rankingsapi"
)

// Rankings is the rankings contract over the leaderboard views the store already keeps: one
// board per title and view, ranked the way the game's own enumerators rank (best rating first,
// first write wins a tie). Board ids are "<title id in decimal>-<view>". Rows carry pid, the
// player's XUID in decimal exactly as the store keys its rows (an online XUID; the store holds
// no other player id). The store keeps no write times, so UpdatedAt is left zero. There is no
// title name list, so boards are named from the title id and the view number.
//
// Only, when set, scopes the boards to one title; RankingsHandler serves both the whole store and one
// title per path (/t/<title>/internal/rankings/..., like the leaderboards core).
type Rankings struct {
	Store *Store
	Only  *uint32
}

// TitleID reads the {title} of a /t/ path: exactly 8 characters is the Xbox 360 hex title id
// (4D5307E6), anything else is the decimal id the board ids use (1297287142).
func TitleID(s string) (uint32, bool) {
	base := 10
	if len(s) == 8 {
		base = 16
	}
	v, err := strconv.ParseUint(s, base, 32)
	return uint32(v), err == nil
}

// RankingsHandler is the rankings contract for all titles at /internal/rankings/ and for one title at
// /t/<title>/internal/rankings/.
func RankingsHandler(network string, store *Store) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/internal/rankings/", rankingsapi.Handler(network, Rankings{Store: store}))
	mux.HandleFunc("/t/{title}/{rest...}", func(w http.ResponseWriter, r *http.Request) {
		t, ok := TitleID(r.PathValue("title"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/" + r.PathValue("rest")
		rankingsapi.Handler(network, Rankings{Store: store, Only: &t}).ServeHTTP(w, r2)
	})
	return mux
}

func boardKey(id string) (title, view uint32, ok bool) {
	t, v, found := strings.Cut(id, "-")
	t64, err1 := strconv.ParseUint(t, 10, 32)
	v64, err2 := strconv.ParseUint(v, 10, 32)
	return uint32(t64), uint32(v64), found && err1 == nil && err2 == nil
}

func (r Rankings) Boards(context.Context) ([]rankingsapi.Board, error) {
	s := r.Store
	s.mu.Lock()
	type tv struct{ title, view uint32 }
	var keys []tv
	for k, rows := range s.Views {
		if len(rows) == 0 {
			continue
		}
		var t, v uint32
		if _, err := fmt.Sscanf(k, "%08X/%d", &t, &v); err == nil && (r.Only == nil || *r.Only == t) {
			keys = append(keys, tv{t, v})
		}
	}
	s.mu.Unlock()
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].title != keys[j].title {
			return keys[i].title < keys[j].title
		}
		return keys[i].view < keys[j].view
	})
	boards := make([]rankingsapi.Board, 0, len(keys))
	for _, k := range keys {
		boards = append(boards, rankingsapi.Board{
			ID:    fmt.Sprintf("%d-%d", k.title, k.view),
			Name:  fmt.Sprintf("Game %d, %s", k.title, rankingsapi.NeutralName(uint64(k.view))),
			Kind:  rankingsapi.KindRating,
			Order: rankingsapi.OrderDesc,
		})
	}
	return boards, nil
}

func (r Rankings) Rows(_ context.Context, id string, q rankingsapi.Query) (rankingsapi.Page, error) {
	title, view, ok := boardKey(id)
	if !ok || (r.Only != nil && *r.Only != title) {
		return rankingsapi.Page{}, rankingsapi.ErrNoBoard
	}
	s := r.Store
	s.mu.Lock()
	all := s.sorted(title, view)
	s.mu.Unlock()
	if len(all) == 0 {
		return rankingsapi.Page{}, rankingsapi.ErrNoBoard
	}
	rows := make([]rankingsapi.Row, len(all))
	for i, e := range all {
		score := e.row.rating()
		rows[i] = rankingsapi.Row{PID: strconv.FormatUint(e.xuid, 10), Score: score, UpdatedAt: time.Time{}}
	}
	return rankingsapi.Window(rankingsapi.KindRating, rows, q), nil
}
