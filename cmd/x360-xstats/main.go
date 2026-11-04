// Command x360-xstats serves Xbox Live leaderboards, Live service 7 (see internal/xstats).
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/openpak/x360-common/accountcore"
	"github.com/openpak/x360-common/httplog"
	"github.com/openpak/x360-common/wire"
	"github.com/openpak/x360-common/xuacsapi"
	"github.com/openpak/x360-xstats/internal/xstats"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	addr := os.Getenv("X360_XSTATS_ADDR")
	if addr == "" {
		addr = ":10007"
	}
	dbCtx, dbCancel := context.WithTimeout(context.Background(), 30*time.Second)
	// X360_XSTATS_STATE is only the old stats.json, imported once when the database is empty.
	legacy := os.Getenv("X360_XSTATS_STATE")
	store, err := xstats.Open(dbCtx, os.Getenv("X360_XSTATS_DATABASE_URL"), xstats.Schema, legacy)
	dbCancel()
	if err != nil {
		log.Error("state: X360_XSTATS_DATABASE_URL is required and must be reachable", "err", err)
		os.Exit(2)
	}
	defer store.Close()
	log.Info("state in Postgres", "schema", xstats.Schema)
	h := &xstats.Handler{Store: store, Log: log, ArbKey: os.Getenv("X360_XSTATS_ARB_KEY")}
	// Gamertags for leaderboard rows come from the account core (the account handle).
	if u := os.Getenv("X360_XSTATS_ACCOUNT_CORE_URL"); u != "" {
		core, err := accountcore.New(u, os.Getenv("X360_XSTATS_ACCOUNT_CORE_TOKEN"), nil)
		if err != nil {
			log.Error("account core", "err", err)
			os.Exit(2)
		}
		h.Names, h.Profiles = core, core
	}
	// Profile settings look up other players' accounts by online XUID in x360-xuacs.
	if u := os.Getenv("X360_XUACS_INTERNAL_URL"); u != "" {
		h.Accounts = &xuacsapi.Client{URL: u, Key: os.Getenv("X360_XUACS_INTERNAL_KEY")}
	}
	// The leaderboard rows live in the neutral leaderboards core (gRPC).
	lbAddr, lbKey := os.Getenv("X360_XSTATS_LEADERBOARDS_ADDR"), os.Getenv("X360_XSTATS_LEADERBOARDS_KEY")
	if lbAddr == "" || lbKey == "" {
		log.Error("X360_XSTATS_LEADERBOARDS_ADDR and X360_XSTATS_LEADERBOARDS_KEY are required")
		os.Exit(2)
	}
	lb, lbConn, err := xstats.DialLeaderboards(lbAddr, lbKey)
	if err != nil {
		log.Error("leaderboards", "err", err)
		os.Exit(2)
	}
	defer lbConn.Close()
	h.LB = lb
	// Rows from the pre-core store move into the core once (rows without an account stay in the document).
	mctx, mcancel := context.WithTimeout(context.Background(), 5*time.Minute)
	st, err := h.MigrateLegacy(mctx)
	mcancel()
	if err != nil {
		log.Error("migrate stats rows to the leaderboards core", "err", err)
		os.Exit(1)
	}
	log.Info("legacy stats rows", "views", st.Views, "rows", st.Rows, "migrated", st.Migrated, "existing", st.Existing, "no_account", st.NoAccount)
	srv := &http.Server{Addr: addr, Handler: httplog.Wrap(wire.Guard(os.Getenv("X360_GATEWAY_KEY"), h)), ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		sc, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(sc)
	}()
	log.Info("x360-xstats listening", "addr", addr, "gamertags", h.Names != nil, "xuacs", h.Accounts != nil)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("serve", "err", err)
		os.Exit(1)
	}
}
