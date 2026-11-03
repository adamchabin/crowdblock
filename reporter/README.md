# crowdblock-reporter

Watches attack logs and reports the attacking addresses to the server
(`POST /api/v1/reports`). Each log type is handled by a plugin; the user picks
the active ones with `-plugins`.

| Plugin | Log | Reports |
|---|---|---|
| `auth` | `/var/log/auth.log` | failed / hostile SSH logins |
| `fail2ban` | `/var/log/fail2ban.log` | addresses banned by fail2ban (any jail) |

```sh
go build -o crowdblock-reporter ./reporter     # or reporter/build_reporter.sh

./crowdblock-reporter -list-plugins

# test without sending anything
sudo ./crowdblock-reporter -dry-run -plugins auth,fail2ban

CROWDBLOCK_API_KEY=sfw_... sudo -E ./crowdblock-reporter -server https://your-server
```

| Option | Default | |
|---|---|---|
| `-server` / `CROWDBLOCK_SERVER` | – | server URL |
| `CROWDBLOCK_API_KEY` | – | API key (environment only – command lines are visible in `ps`) |
| `-plugins` | all | active plugins, e.g. `auth,fail2ban` |
| `-list-plugins` | | lists the available plugins |
| `-<plugin>-log` | from the plugin | log path, e.g. `-auth-log /var/log/secure` |
| `-threshold`, `-window` | `3`, `10m` | attempts within a time window before an address is reported |
| `-cooldown` | `1h` | minimum time between two reports of the same address (shared by all plugins) |
| `-dry-run` | `false` | only log what would be reported |

## How it works

- `plugins.go` – plugin registry. A plugin is a name, description, default
  log, threshold (0 = `-threshold`) and a `Parse(line) → (ip, detail, ok)`
  function. The name is sent as the report's source, so it must match
  `^[a-z0-9][a-z0-9_-]{0,31}$` (checked at start).
- `authlog.go` – recognises sshd messages: `Failed password/publickey`,
  `Invalid user`, `maximum authentication attempts exceeded`,
  `Too many authentication failures`, `Did not receive identification string`,
  `banner exchange … invalid format`, `Unable to negotiate`. The user name is
  controlled by the attacker, so the **last** address in the line is used
  (`Invalid user x from 8.8.8.8 port 1 from <real address>`).
- `fail2ban.go` – `NOTICE [jail] Ban <ip>` lines. fail2ban applies its own
  thresholds, so every ban is reported right away. `Restore Ban` (bans
  re-applied after a fail2ban restart – old attacks), `Unban` and `Found` are
  ignored.
- Private, loopback, link-local, CGNAT and documentation addresses are never
  reported.
- `tail.go` – reads only new lines (like `tail -F`) and handles rotation
  (logrotate `create` and `copytruncate`).
- `tracker.go` – threshold within a time window (per plugin) and a cooldown
  shared by all plugins – an attack seen in both `auth.log` and fail2ban is
  reported once.
- `client.go` – sends reports (`{"ip": ..., "source": "<plugin>"}`) with the
  `X-API-Key` header; the source is shown in the router's list. A failed report is
  retried on the next attempt from that address.

Needs read access to the logs (root; `auth.log` also group `adm`). A missing
file is not an error – the reporter waits for it to appear. Systems without
`auth.log` (journald only) are not supported yet.

## Adding a plugin

A new file, e.g. `nginx.go`, with a parser and the registration – `main.go`
doesn't change, the `-nginx-log` flag is created automatically:

```go
func init() {
	register(&Plugin{
		Name:        "nginx",
		Description: "...",
		DefaultLog:  "/var/log/nginx/access.log",
		Parse: func(line string) (netip.Addr, string, bool) { ... },
	})
}
```
