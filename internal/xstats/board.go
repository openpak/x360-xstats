package xstats

// The adapter over the neutral leaderboards core (docs/adapter-guide.md, "Xbox 360"): this file
// holds every call to it. A view is a board (network "x360", title = the 8-digit hex title id,
// board = the view id), a row is the entry of the player's account, the rating column is the
// entry's score.
//
// Columns: XAM sends one proc per column aggregation (Last, Sum, Min, Max). The core merges
// columns per board, so each posted column is declared once on the board (column_aggregations) and
// every statpost is plain Submits. The core stores int64 only, so a column key carries the wire
// type: "<column id>.<type>" ("12.3" is column 12 as int32; a double is stored as fixed point,
// millionths, as type 5). The rating column (0xFFFE) is the board's score_column and always the
// key "65534", read back as int64.

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/openpak/x360-xstats/internal/leaderboardspb"
)

// Network is the account-core link namespace the core files Xbox 360 entries under.
const Network = "x360"

const (
	ratingKey   = "65534"
	doubleScale = 1e6
	callTimeout = 10 * time.Second
	idCacheMax  = 20000
)

// write is one column proc of a statpost.
type write struct {
	proc uint16
	xuid uint64
	view uint32
	cols []uint16
	vals []Value
}

func titleKey(title uint32) string { return fmt.Sprintf("%08X", title) }

func ref(title, view uint32) *pb.BoardRef {
	return &pb.BoardRef{Network: Network, Title: titleKey(title), Board: strconv.FormatUint(uint64(view), 10)}
}

func aggName(proc uint16) string {
	switch proc {
	case procSum:
		return "sum"
	case procMin:
		return "min"
	case procMax:
		return "max"
	}
	return "latest"
}

// colKey is the core column key of a posted column.
func colKey(id uint16, v Value) string {
	if id == colRating {
		return ratingKey
	}
	return fmt.Sprintf("%d.%d", id, v.Type)
}

// colInt is the value stored in the core.
func colInt(id uint16, v Value) int64 {
	if v.Type == tDouble && id != colRating {
		return int64(math.Round(v.float() * doubleScale))
	}
	return v.Int()
}

// value rebuilds a wire value from a stored one.
func value(typ uint8, n int64) Value {
	switch typ {
	case tDouble:
		return floatValue(float64(n) / doubleScale)
	case tInt32:
		return Value{tInt32, uint64(uint32(int32(n)))}
	}
	return Value{typ, uint64(n)}
}

// columns indexes a stored entry's columns by the wire column id.
func columns(e *pb.Entry) map[uint16]Value {
	out := make(map[uint16]Value, len(e.Columns))
	for k, n := range e.Columns {
		id, typ, ok := strings.Cut(k, ".")
		if !ok {
			continue // the rating key
		}
		c, err1 := strconv.ParseUint(id, 10, 16)
		t, err2 := strconv.ParseUint(typ, 10, 8)
		if err1 == nil && err2 == nil {
			out[uint16(c)] = value(uint8(t), n)
		}
	}
	return out
}

// boardState remembers which column aggregations each board already declares in the core.
type boardState struct {
	mu   sync.Mutex
	aggs map[string]map[string]string // title/view -> column key -> aggregation
}

func (h *Handler) lb() (pb.LeaderboardsClient, error) {
	if h.LB == nil {
		return nil, upstreamErr{fmt.Errorf("no leaderboards core configured")}
	}
	return h.LB, nil
}

func up(err error) error {
	if err == nil {
		return nil
	}
	return upstreamErr{err}
}

func policy(view uint32, aggs map[string]string) *pb.Policy {
	return &pb.Policy{Name: fmt.Sprintf("View %d", view), Kind: "rating", Order: "desc", Aggregation: "latest",
		ScoreColumn: ratingKey, ColumnAggregations: aggs}
}

// declare makes the board's column aggregations include want. The core replaces the whole map on
// an update, so the stored map is read once per board and then merged here.
func (h *Handler) declare(ctx context.Context, lb pb.LeaderboardsClient, title, view uint32, want map[string]string) error {
	bs := &h.board
	bs.mu.Lock()
	defer bs.mu.Unlock()
	k := fmt.Sprintf("%08X/%d", title, view)
	if bs.aggs == nil {
		bs.aggs = map[string]map[string]string{}
	}
	cur, known := bs.aggs[k]
	if !known {
		cur = map[string]string{}
		info, err := lb.BoardInfo(ctx, &pb.BoardInfoRequest{Ref: ref(title, view)})
		switch {
		case err == nil:
			for c, a := range info.GetBoard().GetPolicy().GetColumnAggregations() {
				cur[c] = a
			}
		case status.Code(err) != codes.NotFound:
			return err
		}
	}
	changed := false
	for c, a := range want {
		if cur[c] != a {
			cur[c], changed = a, true
		}
	}
	if changed {
		if _, err := lb.UpsertBoard(ctx, &pb.UpsertBoardRequest{Ref: ref(title, view), Policy: policy(view, cur)}); err != nil {
			return err
		}
	}
	bs.aggs[k] = cur
	return nil
}

// idCache maps XUIDs and accounts to each other. It is a cache of the identity service, not state.
type idCache struct {
	mu     sync.Mutex
	toAcct map[uint64]string
	toXUID map[string]uint64
}

func (c *idCache) put(x uint64, acct string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.toAcct == nil || len(c.toAcct) > idCacheMax {
		c.toAcct, c.toXUID = map[uint64]string{}, map[string]uint64{}
	}
	c.toAcct[x], c.toXUID[acct] = acct, x
}

// accountOf is the account behind an XUID; ok is false for an XUID nobody owns.
func (h *Handler) accountOf(ctx context.Context, x uint64) (string, bool, error) {
	h.ids.mu.Lock()
	a, ok := h.ids.toAcct[x]
	h.ids.mu.Unlock()
	if ok {
		return a, true, nil
	}
	if h.Accounts == nil {
		return "", false, upstreamErr{fmt.Errorf("no account service configured")}
	}
	a, ok, err := h.Accounts.AccountByXUID(ctx, x)
	if err != nil {
		return "", false, upstreamErr{err}
	}
	if ok && a != "" {
		h.ids.put(x, a)
		return a, true, nil
	}
	return "", false, nil
}

// xuidOf is an account's online XUID.
func (h *Handler) xuidOf(ctx context.Context, acct string) (uint64, error) {
	h.ids.mu.Lock()
	x, ok := h.ids.toXUID[acct]
	h.ids.mu.Unlock()
	if ok {
		return x, nil
	}
	if h.Accounts == nil {
		return 0, upstreamErr{fmt.Errorf("no account service configured")}
	}
	x, err := h.Accounts.OnlineXUID(ctx, acct)
	if err != nil {
		return 0, upstreamErr{err}
	}
	h.ids.put(x, acct)
	return x, nil
}

// writeStats applies a statpost's column writes: one Submit per proc, the core merges the columns.
func (h *Handler) writeStats(ctx context.Context, title uint32, users map[uint64]string, writes []write) error {
	if len(writes) == 0 {
		return nil
	}
	lb, err := h.lb()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	names := map[string]string{}
	for _, wr := range writes {
		acct := users[wr.xuid]
		if acct == "" {
			var ok bool
			if acct, ok, err = h.accountOf(ctx, wr.xuid); err != nil {
				return err
			}
			if !ok {
				h.log().Warn("statpost for an XUID without an account", "title", titleKey(title))
				continue
			}
		} else {
			h.ids.put(wr.xuid, acct)
		}
		gt, done := names[acct]
		if !done && h.Names != nil {
			var nerr error
			if gt, nerr = h.Names.Handle(ctx, acct); nerr != nil {
				h.log().Warn("gamertag lookup", "err", nerr)
			}
			names[acct] = gt
		}
		cols, aggs := map[string]int64{}, map[string]string{}
		var score int64
		for i, c := range wr.cols {
			v := wr.vals[i]
			if v.Type == tNull {
				continue
			}
			k := colKey(c, v)
			cols[k], aggs[k] = colInt(c, v), aggName(wr.proc)
			if c == colRating {
				score = v.Int()
			}
		}
		if len(cols) == 0 {
			continue
		}
		if err := h.declare(ctx, lb, title, wr.view, aggs); err != nil {
			return up(err)
		}
		_, err := lb.Submit(ctx, &pb.SubmitRequest{Ref: ref(title, wr.view), Account: acct, Score: score, Columns: cols,
			DisplayName: gt, CreatePolicy: policy(wr.view, aggs)})
		if err != nil {
			return up(err)
		}
	}
	return nil
}

// ranked is one entry of a view with its XUID.
type ranked struct {
	xuid uint64
	e    *pb.Entry
	cols map[uint16]Value
}

func (h *Handler) rank(ctx context.Context, es []*pb.Entry) ([]ranked, error) {
	out := make([]ranked, 0, len(es))
	for _, e := range es {
		if e.Sub != 0 {
			continue
		}
		x, err := h.xuidOf(ctx, e.Account)
		if err != nil {
			return nil, err
		}
		out = append(out, ranked{x, e, columns(e)})
	}
	return out, nil
}

// view is a window of a view: the entries, the view's total rows.
type view struct {
	rows  []ranked
	total int
}

func (h *Handler) rangeView(ctx context.Context, lb pb.LeaderboardsClient, title, v uint32, start int64, n int) (view, error) {
	resp, err := lb.Range(ctx, &pb.RangeRequest{Ref: ref(title, v), Start: start, Limit: int64(n)})
	if err != nil {
		return view{}, up(err)
	}
	rows, err := h.rank(ctx, resp.Entries)
	return view{rows, int(resp.Total)}, err
}

// firstAtOrBelow is the 1-based position of the first row rated at or below rating (total+1
// when none is): a rating's estimated rank, and where a by-rating page starts.
func firstAtOrBelow(ctx context.Context, lb pb.LeaderboardsClient, title, v uint32, total int64, rating int64) (int64, error) {
	lo, hi := int64(1), total+1
	for lo < hi {
		mid := (lo + hi) / 2
		resp, err := lb.Range(ctx, &pb.RangeRequest{Ref: ref(title, v), Start: mid, Limit: 1})
		if err != nil {
			return 0, up(err)
		}
		if len(resp.Entries) == 0 || resp.Entries[0].Score <= rating {
			hi = mid
		} else {
			lo = mid + 1
		}
	}
	return lo, nil
}

func (h *Handler) total(ctx context.Context, lb pb.LeaderboardsClient, title, v uint32) (int64, error) {
	resp, err := lb.Total(ctx, &pb.TotalRequest{Ref: ref(title, v)})
	if err != nil {
		return 0, up(err)
	}
	return resp.Total, nil
}

// resetBoards clears an account's rows in one view, or in every view of the title (view 0).
func (h *Handler) resetBoards(ctx context.Context, title, v uint32, acct string) error {
	lb, err := h.lb()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	views := []string{strconv.FormatUint(uint64(v), 10)}
	if v == 0 {
		views = nil
		resp, err := lb.ListBoards(ctx, &pb.ListBoardsRequest{Network: Network, Title: titleKey(title)})
		if err != nil && status.Code(err) != codes.NotFound {
			return up(err)
		}
		for _, b := range resp.GetBoards() {
			views = append(views, b.Id)
		}
	}
	for _, id := range views {
		_, err := lb.Delete(ctx, &pb.DeleteRequest{Ref: &pb.BoardRef{Network: Network, Title: titleKey(title), Board: id}, Account: acct})
		if err != nil && status.Code(err) != codes.NotFound {
			return up(err)
		}
	}
	return nil
}
