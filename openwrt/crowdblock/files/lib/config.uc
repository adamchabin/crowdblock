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
		report_window: dur_opt(s.report_window, 3600, 60, 30 * 86400),
		block_time:    dur_opt(s.block_time, 86400, 60, 30 * 86400),
		sync_interval: dur_opt(s.sync_interval, 600, 60, 86400),
		max_entries:   int_opt(s.max_entries, 20000, 1, 500000),
		timeout:       int_opt(s.timeout, 30, 1, 300),
		block_forward: bool_opt(s.block_forward, true),
		verify_tls:    bool_opt(s.verify_tls, true),
		whitelist:     (type(wl) == 'array') ? wl : [ wl ],
	};

	if (!match(cfg.server, /^https?:\/\/[^\s'"]+$/))
		return { error: 'option server must be an http(s) URL' };

	if (cfg.api_key == '')
		return { error: 'option api_key is required' };

	for (let k in [ 'min_reports', 'report_window', 'block_time', 'sync_interval', 'max_entries', 'timeout' ])
		if (cfg[k] == null)
			return { error: `invalid value for option ${k}` };

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

export { parse_duration, load, load_paused, save_paused };
