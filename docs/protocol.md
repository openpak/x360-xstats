# xstats leaderboard protocol (XAM 17559, Live service 7)

Bodies are XRL "xon/5": schema fields back to back, little-endian. Requests are POST.

## Typed values

`u8 type` + value. Types (XAM `FUN_819359b0` on write, `FUN_81922140` on read): 0 null (u8),
1 u8, 2 u16, 3 int32, 4 int64, 5 double, 7 u64 (XUID in procs). On read XAM maps 1-3 to
XUSER int32, 4 and 7 to int64, 5 to double, 0 to null.

## 0x705 statpost (`FUN_8182df50`, procs built by `FUN_819223b0` / `FUN_81935f70`)

`u32 title`, `u16 n`, `n` x `{u16 proc, u16 m, m x value}`.

Column procs: proc id = the view column's SPA aggregation (0x8001 Last, 0x8003 Sum,
0x8009 Min, 0x800B Max; enum in Xenia `spa_info.h`). Params: `XUID (7)`, `view id (3)`,
`u16 0`, then `(u16 column id, value)` pairs, merged per (proc, XUID, view) up to 256
params. Context-sourced columns are written with proc 0x8001 and an int32 value.

Skill procs (`FUN_819189a8`): 0x800D, or 0x800E when team/score contexts (0x1000800C,
0x1000800D) are set. Header params: session context u32 (`0xFFF00000 | 0xF0000|0xE0000 |
game mode`), u16 0, for 0x800E three doubles or nulls; then per player `XUID (7)`,
game mode 0x1000800B (3), game type 0x1000800A (3), for 0x800E a packed u64 (4) or null.

Special columns: 0xFFFF rank, 0xFFFE rating, 0xFFFD gamertag. XAM reads skill with columns
0x3F/0x40 (mu, sigma, doubles; `FUN_81922758`).

## 0x703 read (`FUN_8182e138`, caller `FUN_819225d8`)

`u32 title` (0 = current: XAM substitutes the service-7 logon title), `u32 n`, `u64 xuid[n]`
(at most 101), specs.

Spec list: `u32 n` (at most 64), `n` x `{u32 view, u32 m, u16 column[m]}`.

Reply: `u32 views`, then views. View: `u64 view id`, `u32 total rows`, `u32 n`, rows.
Row: `u64 xuid`, `u32 rank`, `i64 rating`, `u32 gamertag length`, gamertag bytes (XAM keeps
15), `u32 columns`, `{u16 column id, value}`... (`FUN_81922140`: wire view +0 -> dwViewId,
+8 -> dwTotalViewRows, +0xc -> dwNumRows).

## 0x70E / 0x70D / 0x70F enumerate

`u32 title`, `u64 pivot` (start rank / XUID / rating), `u32 rows`, specs. Reply: `u16 views`,
then views as above.

## 0x704 estimate rank

`u32 title`, `u32 n` (at most 101), `n` x `{u32 view, i64 rating}`. Reply: `u32 n`, `u32 rank[n]`.

## 0x706 reset

`u32 title`, `u64 xuid`, `u32 view`. No reply.

## Profile settings, services 34 and 35 (moved from x360-xuacs)

`readsettings.ashx` (0x711): `u32 flags, u64 caller, u16 n, n x u64 target, u16 m, m x u32 setting id`. Reply
`u16 count`, count x `{u32 source (1), u64 xuid, u32 id, u16 byte length, UTF-16LE value with NUL}`. Targets
the caller may not discover (account core) are left out. Served ids: name 0x41040040 (display name), location
0x40520041 (country), bio 0x43E80043, motto 0x402C0011.

`syncsettings.ashx` (0x710): `u32 title, u64 caller, u64 sync version, u16 start, u16 n`, n entries as above.
Uploads apply only when the console's version is current (or 0); the reply is `u64 version, u16 0, u16 n` and
all four values when the console was not current. The version is the core's profile version, so a website
edit reaches the console and a stale console cannot overwrite it.

## Title, achievement and avatar sync, service 35 (sync.go)

All little-endian, no envelope. Every caller XUID must be on the gateway's users header (else 403). State is
per XUID in the JSON state file (`profiles`). Each list (titles; achievements per title; awards per title) has a
monotonic u64 version; every accepted change takes the next value as its revision, a re-upload of an unchanged
row changes nothing. 0 means never synced. Full layouts and the XAM addresses: Wharf note `re-xstats-sync.md`.

- `synctitles` 0x707: `u64 xuid, u16, u16, u64 version, u16 n (<=32)`, n title records. Reply `u64 version,
  u16 0, u16 n`, title records changed since `version` and not uploaded in this request (at most 32; a cut
  reply reports the last returned revision). Title record: `u32 id, u16 nameLen, name, u64 lastPlayed, u32 gs,
  u16 achUnlocked, u16 achTotal, u32 gsTotal, u32 stamp, u32 0`. Merge: union by id, larger values win.
- `enumtitles` 0x708: `u64 xuid, u64 target, u16, u16 start, u16 max`; reply `u16 n`, records by id (256 per
  page). A target not on the ticket answers empty.
- `cleartitle` 0x709: `u64 xuid, u32 id`; drops the title, achievements and awards. No body.
- `syncachievements` 0x70a / `syncavatarassets` 0x713: `u32 title, u64 xuid, u64 version, u16, u16 start,
  u16 n`, records; reply as 0x707 with achievement/award records. A non-zero count makes the console run the
  full enumerate. The console-local pending-upload flag (0x100000) is not stored. Strings, image ids and
  gamerscore are stored only as uploaded (empty from the console); the catalogue comes later from x360-progress.
- `xachievementenum` 0x70b / `xavatarassetenum` 0x714: `u32 title, u64 xuid, u64 target, u16, u16 start,
  u16 max (<=200)`; reply `u16 n`, records by id.
- `uploadconsoleauditlist` 0x715: accepted and logged, nothing stored. No body.
- `validateavatarmanifest` 0x716: one zero byte.

## Derivation

Schemas of 0x703-0x706 and 0x70D-0x70F were dumped from the 17559 XRL table. Write procs were traced through XAM (`FUN_8191ae78` -> `FUN_81922de8` -> `FUN_819223b0` -> `FUN_8182df50`), read conversion through `FUN_81922140`. Bodies built by the tests decode field for field under the XAM codec model (requests 0x705/0x703, replies 0x703/0x70E).
