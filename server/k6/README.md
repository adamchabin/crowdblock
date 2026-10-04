# k6 performance tests

Run against a **test instance**, not production: the tests write real reports
(addresses from 198.18.0.0/15, source `k6`) that appear in the lists until the
test accounts are deleted.

```bash
k6 run server/k6/reports.js     # POST /reports at a constant rate
k6 run server/k6/lists.js       # GET /ips: full, conditional (304), delta
k6 run server/k6/accounts.js    # register / key / revoke / delete (bcrypt-bound)
k6 run server/k6/mixed.js       # all of the above at once

# without a local k6 (Linux; --network host to reach localhost)
docker run --rm --network host -v "$PWD/server/k6:/k6:ro" grafana/k6 run /k6/mixed.js
```

Environment: `BASE_URL` (default `http://localhost:8080`), `DURATION`, `RATE`
(`READ_RATE`/`WRITE_RATE`/`ACCOUNT_RATE` in mixed), `USERS` (test accounts
with a key each), `VUS`, `MAX_VUS`. Thresholds in `options` fail the run
(exit code 99) – adjust them to your hardware.

## Cleanup

- `reports.js`, `lists.js`, `mixed.js`: `setup()` creates the users
  (`k6-<run>-<n>@example.test`), `teardown()` deletes them with
  `DELETE /api/v1/account` (keys and reports go with them). Teardown also
  runs after Ctrl-C and after a failed setup.
- `accounts.js`: every iteration deletes its own account, also when a step fails.
- Each reported address comes from a slice owned by one user, so the
  blacklist threshold is never reached (the `blacklist` table has no delete
  endpoint). Redis entries expire on their own (reports 1 h, API key cache 60 s).
- After a hard kill (`kill -9`, lost connection) remove leftovers in the database:
  `DELETE FROM users WHERE email LIKE 'k6-%@example.test';`
