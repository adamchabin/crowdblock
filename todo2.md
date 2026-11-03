# crowdblock — planned features (todo2.md)

This list was compiled based on analysis of the current code state (ucode daemon, LuCI, Go server). Features are ordered from most practical to more ambitious.

---

## 1. Local attack reporting from routers (client-side reporting)

Routers currently only download lists from the server. Adding the ability to report attacks detected by the router itself — e.g. from firewall logs (`drop` on WAN), fail2ban, or custom nftables rules.

**Benefit:** The router becomes not just a consumer but also a producer of data — strengthening the crowdblock network.

**Where to add:**
- New command `crowdblock report <ip> <source>` in `crowdblock`
- Sending `POST /api/v1/reports` to the server (in `api.uc`)
- Optional: background scanning of `logread` and reporting recurring attack sources

---

## 2. ✅ Local static blocklist — DONE

Sometimes you want to block an address permanently, independently of the server (e.g. a known bad range from your own logs).

**Implemented:**
- Config option: `list static_block '1.2.3.4'` in `/etc/config/crowdblock` (`config.uc`) with IP/CIDR validation
- In `merge()` in `crowdblock` — `static_block` addresses added to `st.entries` with `block_time` on every sync, whitelisted ones skipped
- New field in LuCI (Advanced tab, `settings.js`, `DynamicList` with `datatype = 'ipaddr'`)

**Usage:** `list static_block '1.2.3.4'` in `/etc/config/crowdblock`, then `crowdblock sync`.

---

## 3. Metrics / Prometheus endpoint or simple stats HTTP

Status is currently only available in `logread` and LuCI. Adding a simple endpoint allows external monitoring (e.g. Prometheus, Grafana, custom dashboard).

**Where to add:**
- New command `crowdblock metrics` printing number of blocked IPv4/IPv6, last sync time, last error, number of paused
- Optionally a small HTTP server in ucode (ulelooop + socket) or a shell script invoked via `inetd`

---

## 4. Maintenance windows

Some networks don't want to block traffic during certain hours (e.g. office hours, to avoid accidentally blocking a legitimate address).

**Where to add:**
- Config options: `maintenance_start`, `maintenance_end`, `maintenance_days`
- In `nft.apply()` or `sync()` — skipping application of new blocks during the maintenance window (existing blocks remain, new ones are not added)

---

## 5. Blocking per source / attack category

The server already returns `sources` (e.g. `auth`, `fail2ban`). Allow the user to decide which sources to block and which to only log.

**Where to add:**
- Config option: `list block_sources 'auth'` / `list block_sources 'fail2ban'`
- In `merge()` — skip addresses whose source is not on the `block_sources` list (entry kept in state for preview only)

---

## 6. Dry-run mode (preview without blocking)

Before deploying to production, it's worth seeing what would be blocked without actually modifying nftables.

**Where to add:**
- `--dry-run` flag for `crowdblock sync`
- In `nft.apply()` — instead of `run_batch`, just print the script to stdout/log

---

## 7. Persistence of blocked addresses after router restart

State is currently in `/tmp` (RAM) — after reboot everything is gone until the first sync. State could be saved to overlay or USB.

**Where to add:**
- `save_state()` also saves to `/etc/crowdblock/state.json` (or `/overlay/upper/etc/crowdblock/state.json`)
- On start (`cmd_run`) — restore from file if no state in `/tmp`

---

## 8. Summary widget on LuCI home page

Currently you have to open the dedicated crowdblock page. A small dashboard widget with the number of blocked addresses and last sync time would be convenient.

**Where to add:**
- New file in `htdocs/luci-static/resources/view/crowdblock/` with a simple summary
- Registration as a LuCI dashboard widget (requires changes to LuCI structure)

---

## 9. IPv4-only or IPv6-only mode

Some networks don't have IPv6 and don't want to create empty v6 sets.

**Where to add:**
- Config option: `ip_version 'both' | 'ipv4' | 'ipv6'`
- In `nft.setup()` — skipping creation of v6 set or chain with `ip6 saddr`

---

## 10. Export / import of configuration and state

Backup of state file and configuration before update or migration to another router.

**Where to add:**
- Commands `crowdblock export > backup.json` and `crowdblock import < backup.json`
- In LuCI — Export/Import buttons on the settings page

---

## Recommended implementation order

1. **Local static blocklist** — least code, high practical value.
2. **Local attack reporting** — differentiates the project from simple blocklists, but requires server cooperation (reporting endpoint).
3. **Metrics / stats** — easy to add, immediately useful for monitoring a fleet of routers.

---

## Implementation notes

All changes in ucode code require rebuilding the OpenWrt package and reinstalling on the router:

```bash
opkg remove crowdblock
opkg install crowdblock_<version>_all.apk
/etc/init.d/crowdblock restart
```

LuCI changes require rebuilding the `luci-app-crowdblock` package.
