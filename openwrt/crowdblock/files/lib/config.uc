// Loads and validates /etc/config/crowdblock.
'use strict';

import { cursor } from 'uci';

// "90" -> 90, "30m" -> 1800, "24h" -> 86400, "7d" -> 604800
function parse_duration(v) {
	let m = match(trim('' + v), /^([0-9]+)([smhd]?)$/);
	if (!m)
		return null;

	let mult = { '': 1, s: 1, m: 60, h: 3600, d: 86400 };
	return +m[1] * mult[m[2]];
}

function int_opt(v, def, min, max) {
	if (v == null || v == '')
		return def;

	if (!match('' + v, /^[0-9]+$/))
		return null;

	let n = +v;
	return (n >= min && n <= max) ? n : null;
}

function dur_opt(v, def, min, max) {
	if (v == null || v == '')
		return def;

	let n = parse_duration(v);
	return (n != null && n >= min && n <= max) ? n : null;
}

function bool_opt(v, def) {
	if (v == null || v == '')
		return def;

	return (v in [ '1', 'yes', 'on', 'true', 'enabled' ]);
}

// The server serves the list for these values only (GET /api/v1/ips).
const MIN_REPORTS = [ 1, 5, 10, 20, 50 ];
const REPORT_WINDOWS = [ 3600, 21600, 86400, 604800 ];  // 1h, 6h, 24h, 7d

// The server accepts these limit values (0 = unlimited).
const LIMIT_VALUES = [ 0, 5000, 10000, 50000, 100000, 250000, 500000 ];

// Maximum response body size from the server, in MiB.
// 8 MiB protects RAM on small devices; raise it if you use a large limit.
const MAX_BODY_DEFAULT_MIB = 8;

// Returns { cfg: {...} } or { error: '...' }.
function load() {
	let s = cursor().get_all('crowdblock', 'main');
	if (!s)
		return { error: "missing section 'main' in /etc/config/crowdblock" };

	let cfg = {
		server:         rtrim(s.server ?? '', '/'),
		api_key:        s.api_key ?? '',
		min_reports:    int_opt(s.min_reports, 5, 1, 1000000),
		report_window:  dur_opt(s.report_window, 21600, 60, 30 * 86400),
		block_time:     dur_opt(s.block_time, 86400, 60, 30 * 86400),
		sync_interval:  dur_opt(s.sync_interval, 600, 60, 86400),
		max_entries:    int_opt(s.max_entries, 20000, 1, 500000),
		limit:          int_opt(s.limit, 0, 0, 500000),
		max_body:       int_opt(s.max_body, MAX_BODY_DEFAULT_MIB, 1, 1024),
		timeout:        int_opt(s.timeout, 30, 1, 300),
		verify_tls:     bool_opt(s.verify_tls, true),
		debug:          bool_opt(s.debug, false),
		keep_batch_nft: bool_opt(s.keep_batch_nft, false),
	};

	if (!match(cfg.server, /^https?:\/\/[^\s'"]+$/))
		return { error: 'option server must be an http(s) URL' };

	if (cfg.api_key == '')
		return { error: 'option api_key is required' };

	for (let k in [ 'min_reports', 'report_window', 'block_time', 'sync_interval', 'max_entries', 'max_body', 'timeout' ])
		if (cfg[k] == null)
			return { error: `invalid value for option ${k}` };

	if (!(cfg.min_reports in MIN_REPORTS))
		return { error: `option min_reports must be one of ${join(', ', MIN_REPORTS)}` };

	if (!(cfg.report_window in REPORT_WINDOWS))
		return { error: 'option report_window must be one of 1h, 6h, 24h, 7d' };

	if (!(cfg.limit in LIMIT_VALUES))
		return { error: `option limit must be one of ${join(', ', LIMIT_VALUES)}` };

	// A block must outlive the next sync (with room for one failed sync),
	// otherwise listed addresses are unblocked between syncs.
	if (cfg.block_time < 2 * cfg.sync_interval)
		return { error: 'option block_time must be at least twice sync_interval' };

	return { cfg };
}

export { parse_duration, load };
