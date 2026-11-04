# Changelog

## v0.9.0 (2026-11-04)

- Account erasure (card #120). `DELETE /internal/accounts/{account_id}/data` with `Authorization: Bearer $ACCOUNT_ERASE_KEY` on a separate listener `ERASE_ADDR` (default `:9099`, container network only, never published; not served while the key is unset). Reply `{"service":"x360-xstats","rows":N}`; one `account erased` log line per erasure. Deletes the account's synced profile (titles, achievements, awards) and any legacy leaderboard rows kept in the `state` document, by XUID (from x360-xuacs, existing `X360_XUACS_INTERNAL_URL`). The live leaderboard rows are in the leaderboards core and are erased by its own step.

## v0.8.0 (2026-11-04)

- Authorization (#101): a statpost may only write XUIDs signed in on the caller's tunnel, or the participants x360-xarb vouches for on an arbitrated result (`X-X360-Arb-Users` with `X-X360-Arb-Key` = `X360_XSTATS_ARB_KEY`). Any other XUID refuses the whole post with 403 before anything is written. Without the key configured only the caller's own XUIDs are accepted.
- Tests: own XUID, other XUID, mixed post, participant, non-participant, wrong key, no key configured.

## v0.7.1 (2026-11-04)

- Docs only: the core serves a Xbox 360 title at `/t/x360:<title id>` (not `/n/x360/t/...`, which takes a neutral key).

## v0.7.0 (2026-11-04)

- Leaderboard rows move into the neutral leaderboards core (gRPC, `X360_XSTATS_LEADERBOARDS_ADDR` / `_KEY`, required): a view is a board,
  a player's row the entry of the player's account, the rating column the score column, columns merged by the core. The in-process
  store, the `rankingsapi` copy and `RANKINGS_ADDR` are gone; the website reads `/t/x360:<title id>` from the core.
- One-time, idempotent migration of the old rows into the core at start (rows without an account stay in the Postgres document; the document keeps the old rows).
- Reads need x360-xuacs to turn accounts back into XUIDs. A core outage answers 503.
- Logging: no more request bodies or user lists in the log; the request line (method, path, status, ms) comes from httplog.

## v0.6.1 (2026-11-03)

- Every request logs one stderr line, `http <method> <path> -> <status> <duration>ms` (x360-common v0.5.1 `httplog`): no query, headers, cookies, tokens or bodies; `/healthz` skipped.

## v0.6.0 (2026-11-03)

- State moves to Postgres (schema `x360_xstats` in database `x360`, `X360_XSTATS_DATABASE_URL`, required: the service
  refuses to start without it). An existing `stats.json` (`X360_XSTATS_STATE`) is imported once on first start and left in place.

## v0.5.0

- Rankings per title: `/t/<title>/internal/rankings/...` on `RANKINGS_ADDR` serves only that title's boards
  (8 characters = hex title id such as `4D5307E6`, anything else = the decimal id of the board ids), so the
  website maps one catalogue title to one URL. The unscoped `/internal/rankings/` is unchanged.

## v0.4.0

- Services 34/35 title, achievement and avatar-award sync (0x707-0x70b, 0x713-0x716) is real: per-profile
  stores in the JSON state with monotonic list versions, delta replies, paged enumerates. 0x715 is logged
  only, 0x716 keeps its 1-byte reply. Catalogue strings stay empty until x360-progress.

## v0.3.0

- Rankings contract (`/internal/rankings/boards`, `/boards/{id}`) over the stored leaderboard views,
  on its own listener when `RANKINGS_ADDR` is set (empty = off). Rows carry the XUID as `pid`,
  network `x360`, with an `around` slice.

## v0.2.0

- Live services 34 (profile settings, account-core backed) and 35 (titles, achievements, avatars: empty
  answers) moved here from x360-xuacs v0.9.7, behaviour unchanged; one route for all of `/xstats`.

## v0.1.0

- Leaderboards (Live service 7): column writes, reads, enumerators, rank estimate, reset; JSON state.
