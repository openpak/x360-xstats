# Evidence

- Static: schemas of 0x703-0x706 and 0x70D-0x70F dumped from the 17559 XRL
  table; write procs traced through XAM (`FUN_8191ae78` -> `FUN_81922de8` -> `FUN_819223b0`
  -> `FUN_8182df50`), read conversion through `FUN_81922140`. Bodies built by this service's
  tests decode field for field under the XAM codec model (requests 0x705/0x703, replies
  0x703/0x70E).
- Unverified: the
  0x70D/0x70E/0x70F pivot field meanings (from the Live API shapes, not from the builders).
