package xstats

import (
	"context"
	"fmt"
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
type Rankings struct{ Store *Store }

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
		if _, err := fmt.Sscanf(k, "%08X/%d", &t, &v); err == nil {
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
	if !ok {
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
