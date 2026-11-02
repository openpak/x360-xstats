# Xbox 360 Leaderboards Server

OpenPak's Xbox 360 stats server: leaderboards (Live service 7) and the other `/xstats` services, profile
settings (34) and titles/achievements/avatars (35).

| | |
| --- | --- |
| Network | Xbox 360 |
| Role | Leaderboard writes, reads and enumeration (`XSessionWriteStats`, `XUserReadStats`, `XUserCreateStatsEnumeratorBy*`, `XUserEstimateRankForRating`, `XUserResetStatsView`) |
| Depends on | [`x360-common`](https://github.com/openpak/x360-common) (shared Go library, vendored); the account core (gamertags, profile settings); [`x360-xuacs`](https://github.com/openpak/x360-xuacs) internal API (XUID to account) |
| Image | `ghcr.io/openpak/x360-xstats` |
| Used by | XAM through the security gateway |
| Licence | AGPL-3.0-only |

## What it serves

| Path | XRL | What |
| --- | --- | --- |
| `POST /xstats/statpost.ashx` | 0x705 | Column writes (Last, Sum, Min, Max) |
| `POST /xstats/xstatread.ashx` | 0x703 | Rows of given users |
| `POST /xstats/xstatenumbyrank.ashx` | 0x70E | Page from a rank |
| `POST /xstats/xstatenumbyuser.ashx` | 0x70D | Page around a user |
| `POST /xstats/xstatenumbyrating.ashx` | 0x70F | Page from a rating |
| `POST /xstats/xstatestimaterankforratings.ashx` | 0x704 | Rank a rating would get |
| `POST /xstats/xstatsreset.ashx` | 0x706 | Clear a signed-in user's row |
| `POST /xstats/readsettings.ashx` | 0x711 | Profile settings (name, location, bio, motto) of users the caller may see |
| `POST /xstats/syncsettings.ashx` | 0x710 | Sync the caller's profile settings with the account core |
| `POST /xstats/synctitles.ashx`, `enumtitles`, `syncachievements`, `xachievementenum`, ... | 0x707-0x716 | Service 35 and the rest of 34: empty answers |
| `GET /healthz` | | Health probe |

No per-title configuration: XAM reads the title's own stats views and sends one write per
column aggregation with the column ids, so the server stores columns and ranks rows by the
rating column (0xFFFE). Services 34 and 35 moved here from x360-xuacs (one repository
for the firmware's `xstats` front end); their settings come from the account core, so an edit on the
website shows on the console and the other way round.

Not served yet: skill (TrueSkill) updates, procs 0x800D/0x800E. Skill reads then find no row and
XAM uses its default skill.

Rankings contract: set `RANKINGS_ADDR` (for example `:9090`) to serve `/internal/rankings/boards` and
`/boards/{id}` on the container network; empty means off. One board per title and view (`<title id in
decimal>-<view>`), best rating first; rows carry `pid`, the player's XUID in decimal, network `x360`.
The store keeps no write times, so `updated_at` is the zero time.

State is one JSON file (`X360_XSTATS_STATE`). Gamertags come from the account core when
`X360_XSTATS_ACCOUNT_CORE_URL` / `_TOKEN` are set; profile settings need them too, and
`X360_XUACS_INTERNAL_URL` / `_KEY` to read other players' settings.

## Run

```sh
GOWORK=off go run -mod=vendor ./cmd/x360-xstats
```

## Docs

- [Protocol](docs/protocol.md)
- [Deploy](docs/deploy.md)

## Licence

AGPL-3.0-only. See [LICENSE](LICENSE).
