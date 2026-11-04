# Deploy

Primary host, quadlet `deploy/openpak-x360-xstats.container` (IP pinned in openpak.network),
the old `stats.json` in `~/openpak/deploy/x360-xstats/state` (read once, never needed again), leaderboards core address and key in `~/openpak/deploy/x360-xstats/lb.env` (`X360_XSTATS_LEADERBOARDS_ADDR`, `X360_XSTATS_LEADERBOARDS_KEY` = the `LEADERBOARDS_INTERNAL_KEY`, mode 0600, never committed), account-core env in
`~/openpak/deploy/x360-xstats/core.env` (copy of the xuacs values under the X360_XSTATS_ names).
Traefik routes from `deploy/traefik.yml`, priority above the xuacs `/xstats` prefix. Consoles
reach it once service 7 is in the KDC's D7 list (`X360_KDC_D7_SERVICES`).
