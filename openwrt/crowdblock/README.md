# crowdblock – OpenWrt client

Downloads the list of IP addresses reported by other users from the server and
blocks those reported by at least `min_reports` distinct users, for
`block_time`.

Requires OpenWrt 23.05+ (ucode with `import`). The package is architecture
independent (`PKGARCH:=all`) and takes a few dozen KB – all dependencies
except `ucode-mod-uloop` / `ucode-mod-math` are in the default image.
The web interface is in the separate `luci-app-crowdblock` package.

## Layout

```
Makefile                         package for the OpenWrt SDK
files/crowdblock                 /usr/sbin/crowdblock (CLI + daemon)
files/crowdblock.init            /etc/init.d/crowdblock (procd)
files/crowdblock.config          /etc/config/crowdblock (UCI)
files/lib/config.uc              /usr/share/ucode/crowdblock/ – UCI loading and validation
files/lib/ip.uc                  IP / CIDR parsing, whitelist (+ reserved ranges)
files/lib/api.uc                 server communication (uclient-fetch)
files/lib/nft.uc                 `inet crowdblock` table, atomic set replacement
```

## How it works

1. On start it creates its own `inet crowdblock` table with `v4`/`v6` sets
   (`flags timeout`) and `input`/`forward` chains at priority `filter - 10`.
   A separate table survives `fw4 reload`, and the priority makes the drop
   happen before the fw4 rules.
2. Every `sync_interval` (±10%) it downloads `GET /api/v1/ips`.
3. It drops invalid entries, entries with `reports < min_reports` (it doesn't
   rely on the server-side filter), whitelisted ones, private/reserved ranges,
   and the router's own addresses and DNS servers (from
   `ubus network.interface dump`).
4. An address on the list gets `expires = now + block_time`, i.e. a block
   lasts `block_time` since the address was **last** seen on the list. That's
   why `block_time` must be at least twice `sync_interval` (enforced by both
   the daemon and LuCI) – otherwise addresses get unblocked between syncs.
5. Both sets are replaced in a single `nft -f` transaction (flush + add), with
   no gap. Kernel timeouts are a safety net: if the daemon dies or the server
   is down, blocks still expire.
6. State is kept in `/tmp/crowdblock/state.json` (RAM, no flash wear): expiry,
   number of reporters, country and sources per address.
7. Paused addresses (`crowdblock pause`, LuCI) are stored in UCI
   (`list paused`): they stay in the state but are not blocked.
8. On network errors: backoff 60s → 120s → … (up to `sync_interval`).

## API contract

```
GET /api/v1/ips?min_reporters=5&minutes=360
Authorization: Basic base64(crowdblock:api_key)

200 OK
[
{"ip":"198.51.100.23","distinct_reporters":17,"country":"CN","sources":["auth","fail2ban"]},
{"ip":"2001:db8::1","distinct_reporters":6}
]
```

- One entry per line (still a plain JSON array). The client keeps the list
  packed (`/tmp/crowdblock/list`) and parses it line by line while
  unpacking, so its memory use doesn't grow with the list (5 000 addresses:
  ~8 MiB peak instead of ~17 MiB). A list in a single line (older servers) is
  parsed as a whole.

- The server serves fixed thresholds only, so that every list can be
  precomputed: `min_reporters` 1, 5, 10, 20 or 50 (option `min_reports`) and
  `minutes` 60, 360, 1440 or 10080 (option `report_window`: 1h, 6h, 24h, 7d).
  Other values are rejected with `400`; the daemon refuses them at start.
- `country` (ISO 3166-1 alpha-2) is optional – the server adds it when it has
  a GeoIP database (`server/scripts/update_geoip.sh`, DB-IP Lite, CC BY 4.0).
- `sources` is optional – what detected the attacks in the report window
  (reporter plugin names); shown in LuCI.
- `ETag` is the first 16 hex digits of the SHA-256 of the uncompressed body,
  quoted (`"97f8fef9892e2058"`). The client computes it from the saved list
  (`sha256sum`) and sends `If-None-Match`; on `304 Not Modified` it reuses
  `/tmp/crowdblock/list`, so an unchanged list costs no transfer.
- Delta sync: with `If-None-Match` the client also sends `?delta=1` and may
  get just the changes (`{"etag", "count", "set_hash", "remove", "upsert"}`,
  see `server/README.md`). It rewrites the saved list line by line without the
  removed / changed entries, appends the new ones, checks the count and the set
  hash and, on a mismatch, fetches the full list right away (logged as
  `delta rejected`). The log shows `sync ok (delta: N changed, M removed)`.
- The list is sorted by `distinct_reporters`, descending – when `max_entries`
  is reached, the client keeps the most trusted entries.
- `distinct_reporters` is the number of **distinct** reporters, not reports.
- With `Accept-Encoding: gzip` the server compresses the list (~300 KiB →
  ~45 KiB for 5000 addresses). The client asks for it when its `uclient-fetch`
  supports `--header` (OpenWrt 25.12+) and unpacks it with BusyBox `gunzip`;
  older ones download it uncompressed.
- The server accepts the key in the `X-API-Key` header or as the Basic auth
  password (the user name is ignored). Without credentials it responds `401`
  with `WWW-Authenticate: Basic` – uclient-fetch only sends the password in
  response to that challenge.

## Building

The `openwrt/` directory of the repository is a feed – link it into the SDK
instead of copying:

```sh
# in the OpenWrt SDK directory, once
echo "src-link crowdblock /path/to/crowdblock/openwrt" >> feeds.conf.default
./scripts/feeds update crowdblock
./scripts/feeds install -a -p crowdblock
make defconfig
make -j$(nproc) package/crowdblock/compile    # first build: also builds the dependencies
```

After that, `openwrt/build-crowdblock.sh` and `openwrt/build-luci-app-crowdblock.sh`
rebuild a package in seconds (`NO_DEPS=1`) and print the path of the `.apk`.
Bump `PKG_RELEASE` in the Makefile for every new build that goes to a router –
`apk` doesn't reinstall a package with the same version.

```sh
apk add --allow-untrusted crowdblock-0.1.0-rN.apk luci-app-crowdblock-0.1.0-rN.apk
rm -rf /tmp/luci-*
```

## Usage

```sh
uci set crowdblock.main.server='https://your-server'
uci set crowdblock.main.api_key='sfw_...'       # from POST /api/v1/api-keys
uci set crowdblock.main.enabled='1'
uci commit crowdblock
/etc/init.d/crowdblock enable
/etc/init.d/crowdblock start

crowdblock status          # number of blocks, last sync, drop counters
crowdblock sync            # manual sync
crowdblock pause IP        # stop blocking IP (until resumed)
uci set crowdblock.main.debug=1; uci commit crowdblock   # sync details in the log (LuCI: Advanced → Debug)
crowdblock resume IP       # block a paused IP again
logread -e crowdblock      # daemon log
nft list set inet crowdblock v4
```

`/etc/init.d/crowdblock stop` removes the table, i.e. unblocks everything.

**Testing:** addresses from the documentation ranges (`192.0.2.0/24`,
`198.51.100.0/24`, `203.0.113.0/24`, `2001:db8::/32`) – including the
defaults of `server/scripts/report_ips.sh` – are deliberately ignored as reserved.
To test blocking, report a public address you don't use.

## TODO

- whitelist the server's address (DNS resolution) and NTP servers
- locking between a parallel `crowdblock sync` and the running daemon
- CIDR entries from the server (e.g. a whole /24 with many reports)
