# Changelog

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
