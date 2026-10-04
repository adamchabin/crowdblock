// Talks to the collector server through uclient-fetch.
'use strict';

import { popen, open, stat, unlink, rmdir, mkdtemp } from 'fs';

export const VERSION = '0.1.0';

// Response bigger than this is rejected (protects RAM), also after unpacking.
const MAX_BODY = 8 * 1024 * 1024;

// Downloads go to a fresh directory here (RAM), so that the daemon and a
// manual `crowdblock sync` never share a file.
const TMP_DIR = '/tmp/crowdblock';

function shell_quote(s) {
	return "'" + replace('' + s, "'", "'\\''") + "'";
}

// uclient-fetch gained --header in 2025; older ones (OpenWrt 23.05/24.10)
// reject the option, so ask for gzip only when it's there.
let header_support = null;

function supports_header() {
	if (header_support == null) {
		let p = popen('uclient-fetch --help 2>&1', 'r');
		header_support = p ? index(p.read('all') ?? '', '--header') >= 0 : false;
		if (p)
			p.close();
	}
	return header_support;
}

// Reads at most MAX_BODY + 1 bytes of a command's output.
function read_command(cmd) {
	let p = popen(cmd, 'r');
	if (!p)
		return { error: 'cannot execute ' + cmd };

	let body = p.read(MAX_BODY + 1);
	let rc = p.close();

	return { body, rc };
}

function download(cfg, url, file) {
	let argv = [
		'uclient-fetch', '-q', '-O', file,
		'--timeout=' + cfg.timeout,
		'--user-agent=crowdblock/' + VERSION,
	];

	if (!cfg.verify_tls)
		push(argv, '--no-check-certificate');

	// The list is ~7x smaller gzipped. uclient-fetch doesn't unpack it, the
	// caller does.
	if (supports_header())
		push(argv, '--header=Accept-Encoding: gzip');

	// The server accepts the API key as the Basic auth password (the user
	// name is ignored); uclient-fetch sends it after the 401 challenge.
	// NOTE: visible in `ps` for the duration of the request. Acceptable on a
	// single-user router.
	push(argv, '--user=crowdblock', '--password=' + cfg.api_key);

	push(argv, url);

	let rc = system(join(' ', map(argv, shell_quote)) + ' 2>/dev/null');

	return (rc == 0) ? null : `HTTP request failed (uclient-fetch exit code ${rc})`;
}

// Returns { body } (unpacked) or { error }.
function read_body(file) {
	let size = stat(file)?.size;
	if (size == null)
		return { error: 'no response body' };

	if (size > MAX_BODY)
		return { error: 'response too large' };

	let f = open(file, 'r');
	let magic = f ? f.read(2) : null;
	if (f)
		f.close();

	let res;

	if (magic == '\x1f\x8b') {
		res = read_command('gunzip -c ' + shell_quote(file) + ' 2>/dev/null');
		if (!res.error && res.rc != 0)
			res.error = 'cannot unpack the gzip response';
	}
	else {
		res = read_command('cat ' + shell_quote(file));
	}

	if (res.error)
		return res;

	if (length(res.body) > MAX_BODY)
		return { error: 'response too large' };

	return { body: res.body };
}

function http_get(cfg, url) {
	let dir = mkdtemp(TMP_DIR + '/fetch.XXXXXX');
	if (!dir)
		return { error: 'cannot create a temporary directory in ' + TMP_DIR };

	let file = dir + '/body';
	let err = download(cfg, url, file);
	let res = err ? { error: err } : read_body(file);

	unlink(file);
	rmdir(dir);

	return res;
}

// GET /api/v1/ips -> { entries: [ { ip, reports }, ... ] } or { error: '...' }
// Expected response (sorted by distinct_reporters, descending; country and
// sources are optional):
//   [ { "ip": "198.51.100.23", "distinct_reporters": 17, "country": "CN",
//       "sources": [ "auth", "fail2ban" ] }, ... ]
// What detected the attack ("auth", "fail2ban"): short identifiers only, the
// strings end up in the LuCI page.
function valid_sources(list) {
	if (type(list) != 'array')
		return null;

	let res = filter(list, (s) => type(s) == 'string' && match(s, /^[a-z0-9][a-z0-9_-]{0,31}$/));
	return length(res) ? res : null;
}

function fetch_blocklist(cfg) {
	let url = sprintf('%s/api/v1/ips?min_reporters=%d&minutes=%d',
		cfg.server, cfg.min_reports, (cfg.report_window + 59) / 60);

	let res = http_get(cfg, url);
	if (res.error)
		return res;

	let data;
	try {
		data = json(res.body);
	}
	catch (e) {
		return { error: 'server returned invalid JSON' };
	}

	if (type(data) != 'array')
		return { error: 'unexpected response format' };

	// Malformed elements end up with ip == null and are counted as invalid.
	return { entries: map(data, (e) => ({
		ip: (type(e) == 'object') ? e.ip : null,
		reports: (type(e) == 'object') ? e.distinct_reporters : null,
		country: (type(e) == 'object' && type(e.country) == 'string') ? e.country : null,
		sources: (type(e) == 'object') ? valid_sources(e.sources) : null,
	})) };
}

export { fetch_blocklist };
