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

// Returns { cfg: {...} } or { error: '...' }.
function load() {
	let s = cursor().get_all('crowdblock', 'main');
	if (!s)
		return { error: "missing section 'main' in /etc/config/crowdblock" };

	let wl = s.whitelist ?? [];

	let cfg = {
		server:        rtrim(s.server ?? '', '/'),
		api_key:       s.api_key ?? '',
		min_reports:   int_opt(s.min_reports, 5, 1, 1000000),
		report_window: dur_opt(s.report_window, 21600, 60, 30 * 86400),
		block_time:    dur_opt(s.block_time, 86400, 60, 30 * 86400),
		sync_interval: dur_opt(s.sync_interval, 600, 60, 86400),
		max_entries:   int_opt(s.max_entries, 20000, 1, 500000),
		timeout:       int_opt(s.timeout, 30, 1, 300),
		block_forward: bool_opt(s.block_forward, true),
		verify_tls:    bool_opt(s.verify_tls, true),
		debug:         bool_opt(s.debug, false),
		whitelist:     (type(wl) == 'array') ? wl : [ wl ],
	};

	if (!match(cfg.server, /^https?:\/\/[^\s'"]+$/))
		return { error: 'option server must be an http(s) URL' };

	if (cfg.api_key == '')
		return { error: 'option api_key is required' };

	for (let k in [ 'min_reports', 'report_window', 'block_time', 'sync_interval', 'max_entries', 'timeout' ])
		if (cfg[k] == null)
			return { error: `invalid value for option ${k}` };

	if (!(cfg.min_reports in MIN_REPORTS))
		return { error: `option min_reports must be one of ${join(', ', MIN_REPORTS)}` };

	if (!(cfg.report_window in REPORT_WINDOWS))
		return { error: 'option report_window must be one of 1h, 6h, 24h, 7d' };

	// A block must outlive the next sync (with room for one failed sync),
	// otherwise listed addresses are unblocked between syncs.
	if (cfg.block_time < 2 * cfg.sync_interval)
		return { error: 'option block_time must be at least twice sync_interval' };

	return { cfg };
}

// Addresses paused by the user (`crowdblock pause`, LuCI): kept in the state,
// but not blocked. Read on every sync, as the list changes at runtime.
function load_paused() {
	let p = cursor().get('crowdblock', 'main', 'paused') ?? [];
	return (type(p) == 'array') ? p : [ p ];
}

// Commits directly (no config.change event), so the daemon is not restarted.
function save_paused(list) {
	let c = cursor();

	if (length(list))
		c.set('crowdblock', 'main', 'paused', list);
	else
		c.delete('crowdblock', 'main', 'paused');

	return c.commit('crowdblock');
}

// Enhanced logging system with different levels and rate limiting
let _last_log_time = 0;
let _last_log_message = '';
let _log_rate_limit_interval = 5000; // 5 seconds

// Helper function to get current timestamp in milliseconds
function timestamp() {
	let t = clock(true);
	return t[0] * 1000 + t[1] / 1000000;
}

// Rate-limited logging to prevent flood
function rate_limited_log(level, fmt, ...args) {
	let now = timestamp();
	let message = sprintf(fmt, ...args);
	
	// If this is the same message as last time and within rate limit interval, suppress
	if (now - _last_log_time < _log_rate_limit_interval && message === _last_log_message) {
		return;
	}
	
	_last_log_time = now;
	_last_log_message = message;
	
	// Format: [LEVEL] timestamp message
	let ts = new Date(now).toISOString();
	print(sprintf('[%s] %s %s\n', level, ts, message));
}

// Option debug: details of every sync in the system log, prefixed "DEBUG"
// (logread -e crowdblock).
function debug(cfg, fmt, ...args) {
	if (cfg?.debug)
		rate_limited_log('DEBUG', fmt, ...args);
}

// Option info: general information
function info(cfg, fmt, ...args) {
	if (cfg?.debug) // For now, only show info when debug is enabled, but could be separate config
		rate_limited_log('INFO', fmt, ...args);
}

// Option warn: warnings
function warn_log(fmt, ...args) {
	rate_limited_log('WARN', fmt, ...args);
}

// Option error: errors
function error_log(fmt, ...args) {
	rate_limited_log('ERROR', fmt, ...args);
}

// Milliseconds since `start` = clock(true).
function ms_since(start) {
	let now = clock(true);
	return (now[0] - start[0]) * 1000 + int((now[1] - start[1]) / 1000000);
}

export { parse_duration, load, load_paused, save_paused, debug, info, warn_log, error_log, ms_since };