# Xbox 360 Leaderboards Server

OpenPak's Xbox 360 leaderboards server (Live service 7, `xstats`).

| | |
| --- | --- |
| Network | Xbox 360 |
| Role | Leaderboard writes, reads and enumeration (`XSessionWriteStats`, `XUserReadStats`, `XUserCreateStatsEnumeratorBy*`, `XUserEstimateRankForRating`, `XUserResetStatsView`) |
| Depends on | [`x360-common`](https://github.com/openpak/x360-common) (shared Go library, vendored) |
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
| `GET /healthz` | | Health probe |

No per-title configuration: XAM reads the title's own stats views and sends one write per
column aggregation with the column ids, so the server stores columns and ranks rows by the
rating column (0xFFFE). The settings paths under `/xstats` (Live services 34 and 35) are served by
[`x360-xuacs`](https://github.com/openpak/x360-xuacs).

Not served yet: skill (TrueSkill) updates, procs 0x800D/0x800E. Skill reads then find no row and
XAM uses its default skill.

State is one JSON file (`X360_XSTATS_STATE`). Gamertags come from the account core when
`X360_XSTATS_ACCOUNT_CORE_URL` / `_TOKEN` are set.

## Run

```sh
GOWORK=off go run -mod=vendor ./cmd/x360-xstats
```

## Docs

- [Protocol](docs/protocol.md)
- [Evidence](docs/evidence.md)
- [Deploy](docs/deploy.md)

## Licence

AGPL-3.0-only. See [LICENSE](LICENSE).
