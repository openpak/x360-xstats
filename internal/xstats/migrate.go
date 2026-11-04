package xstats

import (
	pb "github.com/openpak/x360-xstats/internal/leaderboardspb"

	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
)

// MigrateStats is the outcome of MigrateLegacy.
type MigrateStats struct {
	Views, Rows, Migrated, Existing, NoAccount int
}

// MigrateLegacy moves the rows the pre-core store kept (Store.Views) into the leaderboards
// core, once. A row whose XUID has no account cannot be ranked by account and stays in the
// document (it is counted, never dropped); a row the core already holds is left alone, so a
// repeat run, or a restart half way, never overwrites newer live data. The document keeps Views
// afterwards so the source stays recoverable.
func (h *Handler) MigrateLegacy(ctx context.Context) (MigrateStats, error) {
	var st MigrateStats
	s := h.Store
	s.mu.Lock()
	done := s.Migrated
	type item struct {
		title, view uint32
		xuid        uint64
		row         *Row
	}
	var items []item
	if !done {
		for k, rows := range s.Views {
			t, v, ok := strings.Cut(k, "/")
			title, err1 := strconv.ParseUint(t, 16, 32)
			view, err2 := strconv.ParseUint(v, 10, 32)
			if !ok || err1 != nil || err2 != nil {
				continue
			}
			for xk, r := range rows {
				x, err := strconv.ParseUint(xk, 16, 64)
				if err == nil {
					items = append(items, item{uint32(title), uint32(view), x, r})
				}
			}
		}
	}
	s.mu.Unlock()
	if done || len(items) == 0 {
		if !done {
			return st, h.markMigrated()
		}
		return st, nil
	}
	if _, err := h.lb(); err != nil {
		return st, err
	}
	// First writer first, so the core's write order keeps the old tie order.
	sort.Slice(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.title != b.title {
			return a.title < b.title
		}
		if a.view != b.view {
			return a.view < b.view
		}
		return a.row.Seq < b.row.Seq
	})
	views := map[[2]uint32]bool{}
	for _, it := range items {
		views[[2]uint32{it.title, it.view}] = true
		st.Rows++
		acct, ok, err := h.accountOf(ctx, it.xuid)
		if err != nil {
			return st, err
		}
		if !ok {
			st.NoAccount++
			continue
		}
		if exists, err := h.hasEntry(ctx, it.title, it.view, acct); err != nil {
			return st, err
		} else if exists {
			st.Existing++
			continue
		}
		cols, score := map[string]int64{}, it.row.rating()
		for c, v := range it.row.Cols {
			if v.Type != tNull {
				cols[colKey(c, v)] = colInt(c, v)
			}
		}
		if err := h.submitMigrated(ctx, it.title, it.view, acct, score, cols, it.row.Gamertag); err != nil {
			return st, err
		}
		st.Migrated++
	}
	st.Views = len(views)
	slog.Info("migrated stats rows to the leaderboards core", "views", st.Views, "rows", st.Rows,
		"migrated", st.Migrated, "existing", st.Existing, "no_account", st.NoAccount)
	return st, h.markMigrated()
}

func (h *Handler) markMigrated() error {
	h.Store.mu.Lock()
	defer h.Store.mu.Unlock()
	h.Store.Migrated = true
	return h.Store.saveLocked()
}

func (h *Handler) hasEntry(ctx context.Context, title, view uint32, acct string) (bool, error) {
	lb, _ := h.lb()
	resp, err := lb.ByAccounts(ctx, &pb.ByAccountsRequest{Ref: ref(title, view), Accounts: []string{acct}})
	if err != nil {
		return false, up(err)
	}
	return len(resp.Entries) > 0, nil
}

// submitMigrated writes one old row. The column aggregations are declared by the first live
// write, so the migration stores the values with the board's current rules ("latest" at
// creation) and the rating column as the score.
func (h *Handler) submitMigrated(ctx context.Context, title, view uint32, acct string, score int64, cols map[string]int64, gamertag string) error {
	lb, _ := h.lb()
	cols[ratingKey] = score
	aggs := map[string]string{}
	for k := range cols {
		aggs[k] = "latest"
	}
	if err := h.declare(ctx, lb, title, view, aggs); err != nil {
		return up(err)
	}
	_, err := lb.Submit(ctx, &pb.SubmitRequest{Ref: ref(title, view), Account: acct, Score: score, Columns: cols,
		DisplayName: gamertag, CreatePolicy: policy(view, aggs)})
	if err != nil {
		return up(fmt.Errorf("submit: %w", err))
	}
	return nil
}
