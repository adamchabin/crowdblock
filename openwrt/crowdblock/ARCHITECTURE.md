# crowdblock client: how it works

Written for: engineers maintaining the crowdblock OpenWrt package.

The client does one thing: download the blocklist from the crowdblock server
and load it into nftables. There is no whitelist, no state file, no reporting
and no delta sync.

## Files

| File                     | Role                                                     |
|--------------------------|----------------------------------------------------------|
| `files/crowdblock`       | Entry point: commands, sync, daemon loop                 |
| `files/lib/config.uc`    | Reads and validates `/etc/config/crowdblock`             |
| `files/lib/api.uc`       | Downloads the full list (`uclient-fetch`), parses it     |
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
2. `api.fetch_list()` downloads the full list (RAM). The file is named by
   its content: `/tmp/crowdblock/list.gz` if gzipped, else
   `/tmp/crowdblock/list.json`. The previous list is removed first. Every
   request is a full list; there is no ETag and no `304`.
3. `api.each_entry()` reads the list line by line while unpacking it. The
   loader keeps only addresses that:
   - are valid IP addresses,
   - have `distinct_reporters >= min_reports` (checked again on the client),
   - fit within `max_entries`.
4. `nft.load()` builds one nft script: `flush set` for `v4` and `v6`, then
   `add element` for every address with `timeout <block_time>s`. It runs as a
   single `nft -f` transaction, so the list is replaced completely or not at all.
   The script is kept at `/tmp/crowdblock/batch.nft` (RAM) for debugging; the
   next run overwrites it.

The chains (`input` and `forward`, priority `filter - 10`) drop packets from
addresses in `v4` and `v6`. They run before fw4.

## Daemon

`run` starts a uloop timer: the first sync after 1 s, then every
`sync_interval` seconds. A failed sync logs an error and waits for the next
interval; there is no backoff.

## Why the block time works this way

Each sync flushes and refills the sets, so an address that leaves the server
list is unblocked at the next sync. `block_time` is only the kernel timeout of
each element: if the daemon dies, blocks expire on their own after that time.
`config.uc` requires `block_time >= 2 * sync_interval`, so a healthy daemon
always refreshes elements before they expire.

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
