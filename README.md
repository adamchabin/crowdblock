# crowdblock

Crowd-sourced blocklist: machines report the addresses that attack them, and
routers block the addresses reported by enough distinct users.

| Directory | |
|---|---|
| [`server/`](server/) | collector server (Go, PostgreSQL, Redis): REST API, GeoIP |
| [`reporter/`](reporter/) | `crowdblock-reporter` (Go): watches `auth.log`, fail2ban, … and reports attackers |
| [`openwrt/`](openwrt/) | OpenWrt feed: `crowdblock` (blocking daemon, nftables) and `luci-app-crowdblock` (web UI) |

One Go module (`go.mod` here) covers the server and the reporter:

```sh
go test ./...
go build -o server/crowdblock-server ./server
go build -o reporter/crowdblock-reporter ./reporter
```
