# Changelog

## v0.3.0

- Rankings contract (`/internal/rankings/boards`, `/boards/{id}`) over the stored leaderboard views,
  on its own listener when `RANKINGS_ADDR` is set (empty = off). Rows carry the XUID as `pid`,
  network `x360`, with an `around` slice.

## v0.2.0

- Live services 34 (profile settings, account-core backed) and 35 (titles, achievements, avatars: empty
  answers) moved here from x360-xuacs v0.9.7, behaviour unchanged; one route for all of `/xstats`.

## v0.1.0

- Leaderboards (Live service 7): column writes, reads, enumerators, rank estimate, reset; JSON state.
