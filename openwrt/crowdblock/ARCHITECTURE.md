# crowdblock client: how it works

Written for: engineers maintaining the crowdblock OpenWrt package.

The client does one thing: download the blocklist from the crowdblock server
and load it into nftables. There is no whitelist, no reporting. The client
keeps the blocked list in memory and asks the server for changes only
(delta sync); the full list is fetched when no delta is possible.

## Files

| File                     | Role                                                     |
|--------------------------|----------------------------------------------------------|
| `files/crowdblock`       | Entry point: commands, sync, daemon loop                 |
| `files/lib/config.uc`    | Reads and validates `/etc/config/crowdblock`             |
| `files/lib/api.uc`       | Downloads the list or a delta (`uclient-fetch`), parses it |
| `files/lib/nft.uc`       | The nftables table, its sets and the atomic list load    |
| `files/lib/logging.uc`   | `info`, `warn_log`, `error_log`, `debug` (syslog)        |
| `files/crowdblock.init`  | procd service; calls `teardown` when stopped             |
| `files/crowdblock.config`| Default UCI configuration                                |

## Commands

| Command        | What it does                                                   |
|----------------|----------------------------------------------------------------|
| `run`          | Daemon: sync at once, then every `sync_interval`               |
| `sync`         | One sync; exit code 0 on success, 1 on failure                 |
| `teardown`     | Deletes the nftables table (unblocks everything)               |

## Data flow of one sync

1. If the table `inet crowdblock` is missing (for example after `fw4 stop`),
   create it with `nft.setup()`.
2. `api.fetch_list(cfg, etag)` asks with `If-None-Match` and `delta=1` when it
   holds a list (etag from the last successful sync). The answer is one of:
   - `304`: nothing changed, the in-memory map is kept;
   - a delta `{...}`: `remove` and `upsert` are applied to the map, then the
     map's count and `set_hash` must equal the server's (`apply_delta()`);
     on mismatch the full list is fetched instead;
   - a full list `[...]`: downloaded to `/tmp/crowdblock/list.gz` or
     `list.json` and loaded into the map (`load_full()`).
   The etag is saved only after the nftables update succeeded. After a daemon
   restart there is no etag, so the first sync is a full list.
3. `api.each_entry()` reads the list line by line while unpacking it. The
   loader keeps only addresses that:
   - are valid IP addresses,
   - have `distinct_reporters >= min_reports` (checked again on the client),
   - fit within `max_entries`.
4. nftables is updated in one of two ways, each as a single `nft -f` transaction
   (applied completely or not at all). The script is kept at
   `/tmp/crowdblock/batch.nft` (RAM) for debugging; the next run overwrites it.
   - Full refresh, `nft.load()`: `flush set` for `v4` and `v6`, then `add element`
     for every address with `timeout <block_time>s`. Used for a full list, a
     refresh that is due, a recreated table, and as the fallback below.
   - Delta refresh, `nft.load_changes()`: only the changed addresses. `delete
     element` for those that were in the set before the sync, then `add element`
     for those still listed (an address that changed and stays gets a fresh
     timeout). The cost follows the size of the change, not of the whole list.
     A failed batch falls back to the full refresh.

The chains (`input` and `forward`, priority `filter - 10`) drop packets from
addresses in `v4` and `v6`. They run before fw4.

## Daemon

`run` starts a uloop timer: the first sync after 1 s, then every
`sync_interval` seconds. A failed sync logs an error and waits for the next
interval; there is no backoff.

## Why the block time works this way

When the list changes, an address that leaves the server list is unblocked at
that sync: a full list refills the sets, a delta removes its `remove` addresses
from them. When the list is unchanged (`304`), nftables is left alone until half
of `block_time` has passed since the last full refill (`nft_loaded_at` in
`state.json`); then all elements are re-added with a fresh timeout. `block_time` is the kernel timeout
of each element: if the daemon dies, blocks expire on their own after that
time. `config.uc` requires `block_time >= 2 * sync_interval`, so a healthy
daemon refreshes in time.

## Known issues and limits

- Existing routers: a table created by the previous version still has the
  whitelist chains and sets. `setup()` does not rebuild an existing table, so
  run `crowdblock teardown` once after upgrading, then sync again.
- The LuCI app (`luci-app-crowdblock`) still uses removed features: `status`,
  `report`, `pause`/`resume`, the state file and the whitelist options. It will
  not work correctly until it is updated.
- `crowdblock/README.md` still describes the removed features.
- Jest suites in `openwrt/test` for `api.uc` and `config.uc` failed to start
  before this change (Jest cannot mock the ucode modules `math` and `uci`).
  They were trimmed to the functions that still exist and are not run.
