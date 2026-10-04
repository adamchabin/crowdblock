// Talks to the collector server through uclient-fetch.
'use strict';

import { popen, open, stat, unlink, rmdir, mkdtemp, rename } from 'fs';
import { debug, ms_since } from 'crowdblock.config';

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

// The last list as received (usually gzipped, ~10x smaller than unpacked –
// /tmp is RAM), reused when the server answers 304 Not Modified. It is never
// unpacked to a file: entries are parsed line by line while unpacking.
const LIST_FILE = TMP_DIR + '/list';

// Exit code of uclient-fetch for HTTP errors; the status is on stderr.
const UCLIENT_HTTP_ERROR = 8;

function read_file(path) {
	let st = stat(path);
	if (!st || st.size > MAX_BODY)
		return null;

	let f = open(path, 'r');
	if (!f)
		return null;

	let data = f.read('all');
	f.close();
	return data;
}

// Returns {} on 200, { not_modified: true } on 304, or { error }.
function download(cfg, url, file, errfile, etag) {
	// Not -q: it would also hide the "HTTP error <status>" line.
	let argv = [
		'uclient-fetch', '-O', file,
		'--timeout=' + cfg.timeout,
		'--user-agent=crowdblock/' + VERSION,
	];

	if (!cfg.verify_tls)
		push(argv, '--no-check-certificate');

	if (supports_header()) {
		// The list is ~7x smaller gzipped. uclient-fetch doesn't unpack it,
		// each_entry() does.
		push(argv, '--header=Accept-Encoding: gzip');

		// Unchanged list: 304 and no body at all.
		if (etag)
			push(argv, '--header=If-None-Match: ' + etag);
	}

	// The server accepts the API key as the Basic auth password (the user
	// name is ignored); uclient-fetch sends it after the 401 challenge.
	// NOTE: visible in `ps` for the duration of the request. Acceptable on a
	// single-user router.
	push(argv, '--user=crowdblock', '--password=' + cfg.api_key);

	push(argv, url);

	let rc = system(join(' ', map(argv, shell_quote)) + ' 2>' + shell_quote(errfile));
	if (rc == 0)
		return {};

	let status = match(read_file(errfile) ?? '', /HTTP error ([0-9]+)/);

	if (rc == UCLIENT_HTTP_ERROR && status?.[1] == '304')
		return { not_modified: true };

	return { error: status
		? `server returned HTTP ${status[1]}`
		: `HTTP request failed (uclient-fetch exit code ${rc})` };
}

// Shell command printing the unpacked content of `file` (gzip or plain).
function cat_command(file) {
	let f = open(file, 'r');
	let magic = f ? f.read(2) : null;
	if (f)
		f.close();

	return (magic == '\x1f\x8b' ? 'gunzip -c ' : 'cat ') + shell_quote(file);
}

// Checks a downloaded body without unpacking it to a file: valid gzip and at
// most MAX_BODY bytes unpacked (a gzip bomb can't fill the RAM). Returns
// { size, unpacked } or { error }.
function check_body(file, rcfile) {
	let size = stat(file)?.size;
	if (size == null)
		return { error: 'no response body' };

	if (size > MAX_BODY)
		return { error: 'response too large' };

	// The exit code of gunzip, not of head / wc, tells whether it was valid.
	let p = popen(sprintf('(%s 2>/dev/null; echo $? > %s) | head -c %d | wc -c',
		cat_command(file), shell_quote(rcfile), MAX_BODY + 1), 'r');
	let unpacked = p ? +trim(p.read('all') ?? '') : null;
	if (p)
		p.close();

	if (unpacked == null || unpacked > MAX_BODY)
		return { error: 'response too large' };

	if (trim(read_file(rcfile) ?? '') != '0')
		return { error: 'cannot unpack the gzip response' };

	return { size, unpacked };
}

// Same as the server's ETag: the first 16 hex digits of the SHA-256 of the
// unpacked list, quoted (see the API contract in README.md).
function etag_of(path) {
	let p = popen(cat_command(path) + ' 2>/dev/null | sha256sum', 'r');
	let out = p ? p.read('all') : null;
	if (p)
		p.close();

	let m = match(out ?? '', /^([0-9a-f]{16})/);
	return m ? `"${m[1]}"` : null;
}

// First character of the (unpacked) body: '[' full list, '{' delta.
function body_kind(file) {
	let p = popen(cat_command(file) + ' 2>/dev/null | head -c 1', 'r');
	let c = p ? p.read('all') : null;
	if (p)
		p.close();
	return c;
}

// Calls cb(entry, line) for every entry of a list file, `line` being the
// entry's JSON text, parsing it line by line while unpacking: memory use
// doesn't grow with the list. A list in one line (servers before the line
// format) is parsed as a whole. Returns null or an error.
function list_lines(file, cb) {
	if (!stat(file))
		return 'no list downloaded';

	let p = popen(sprintf('%s 2>/dev/null | head -c %d', cat_command(file), MAX_BODY + 1), 'r');
	if (!p)
		return 'cannot read ' + file;

	let err = null, started = false, done = false;

	for (let line = p.read('line'); length(line); line = p.read('line')) {
		line = rtrim(line, ", \t\r\n");
		if (line == '')
			continue;

		if (!started) {
			started = true;
			if (line == '[')
				continue;

			// "[...]" or "[]": the whole list in one line.
			let data;
			try { data = json(line); } catch (e) { }

			if (type(data) != 'array')
				err = 'unexpected response format';
			else
				for (let e in data)
					cb(e, sprintf('%J', e));

			done = true;
			break;
		}

		if (line == ']') {
			done = true;
			break;
		}

		let e;
		try { e = json(line); } catch (x) { }
		cb(e, line);
	}

	p.close();

	if (!err && !done)
		err = started ? 'truncated list' : 'unexpected response format';

	return err;
}

// Same as the server's set hash: the first 16 hex digits of the SHA-256 of
// the sorted addresses, each followed by "\n".
function set_hash(ips, file) {
	sort(ips, (a, b) => (a < b) ? -1 : (a > b) ? 1 : 0);  // bytewise, like Go

	let f = open(file, 'w');
	if (!f)
		return null;
	for (let ip in ips)
		f.write(ip + '\n');
	f.close();

	let p = popen('sha256sum ' + shell_quote(file), 'r');
	let out = p ? p.read('all') : null;
	if (p)
		p.close();
	unlink(file);

	let m = match(out ?? '', /^([0-9a-f]{16})/);
	return m ? m[1] : null;
}

// Applies a delta response (`file`) to the saved list: writes a new list with
// the entries of the old one minus the removed and changed ones, plus the
// changed and new ones, checks it against the server's count and set hash
// and replaces LIST_FILE with it, packed. Returns { etag, upserted, removed }
// or { error } (the caller then fetches the full list).
function apply_delta(cfg, file, dir) {
	let p = popen(sprintf('%s 2>/dev/null | head -c %d', cat_command(file), MAX_BODY + 1), 'r');
	let d;
	try { d = json(p); } catch (e) { }
	if (p)
		p.close();

	if (type(d) != 'object' || type(d.etag) != 'string' || type(d.count) != 'int' ||
	    type(d.set_hash) != 'string' || type(d.upsert) != 'array' || type(d.remove) != 'array')
		return { error: 'invalid delta' };

	let drop = {};
	for (let ip in d.remove)
		if (type(ip) == 'string')
			drop[ip] = true;

	let upsert = filter(d.upsert, (e) => type(e) == 'object' && type(e.ip) == 'string');
	for (let e in upsert)
		drop[e.ip] = true;

	let plain = dir + '/list.new';
	let out = open(plain, 'w');
	if (!out)
		return { error: 'cannot write ' + plain };

	let ips = [], first = true, base = 0;
	let emit = function(line, ip) {
		out.write((first ? '[\n' : ',\n') + line);
		first = false;
		push(ips, ip);
	};

	let err = list_lines(LIST_FILE, function(e, line) {
		base++;
		if (type(e) == 'object' && type(e.ip) == 'string' && !drop[e.ip])
			emit(line, e.ip);
	});
	let kept = length(ips);

	for (let e in upsert)
		emit(sprintf('%J', e), e.ip);

	out.write(first ? '[]\n' : '\n]\n');
	out.close();

	let hash = err ? null : set_hash(ips, dir + '/ips');
	debug(cfg, 'delta: saved list %d entries, kept %d, %d new or changed, %d removed -> %d entries (server: %d), set hash %s (server: %s)',
		base, kept, length(upsert), length(d.remove), length(ips), d.count, hash, d.set_hash);

	if (!err && (length(ips) != d.count || hash != d.set_hash))
		err = 'delta does not match the server list';

	if (!err && system(sprintf('gzip -c %s > %s', shell_quote(plain), shell_quote(plain + '.gz'))) != 0)
		err = 'cannot pack the list';

	if (!err && !rename(plain + '.gz', LIST_FILE))
		err = 'cannot write ' + LIST_FILE;

	unlink(plain);
	unlink(plain + '.gz');

	return err ? { error: err } : { etag: d.etag, upserted: length(upsert), removed: length(d.remove) };
}

// Downloads the list into LIST_FILE. Returns { etag }, { etag, delta } (a
// delta was applied), { not_modified: true } (LIST_FILE is still current) or
// { error } – with `retry_full` when a full download may fix it.
function http_get(cfg, url, etag) {
	let dir = mkdtemp(TMP_DIR + '/fetch.XXXXXX');
	if (!dir)
		return { error: 'cannot create a temporary directory in ' + TMP_DIR };

	let files = { body: dir + '/body', err: dir + '/err', rc: dir + '/rc' };
	let start = clock(true);

	debug(cfg, 'fetch %s, %s', replace(url, /^.*\/api\//, '/api/'),
		etag ? 'If-None-Match ' + etag : 'full list requested');

	let res = download(cfg, url, files.body, files.err, etag);

	if (res.error) {
		debug(cfg, 'fetch failed after %d ms: %s', ms_since(start), res.error);
	}
	else if (res.not_modified) {
		debug(cfg, 'fetch: 304 not modified, %d ms', ms_since(start));
	}
	else {
		let body = check_body(files.body, files.rc);
		let kind = body.error ? null : body_kind(files.body);

		if (!body.error)
			debug(cfg, 'fetch: %s, %d B received, %d B unpacked, %d ms',
				(kind == '{') ? 'delta' : 'full list', body.size, body.unpacked, ms_since(start));

		if (body.error) {
			res = { error: body.error };
		}
		else if (kind == '{') {
			let d = apply_delta(cfg, files.body, dir);
			res = d.error
				? { error: d.error, retry_full: true }
				: { etag: d.etag, delta: { upserted: d.upserted, removed: d.removed } };
		}
		else if (!rename(files.body, LIST_FILE)) {
			res = { error: 'cannot write ' + LIST_FILE };
		}
		else {
			res = { etag: etag_of(LIST_FILE) };
		}
	}

	for (let k, path in files)
		unlink(path);
	rmdir(dir);

	return res;
}

// What detected the attack ("auth", "fail2ban"): short identifiers only, the
// strings end up in the LuCI page.
function valid_sources(list) {
	if (type(list) != 'array')
		return null;

	let res = filter(list, (s) => type(s) == 'string' && match(s, /^[a-z0-9][a-z0-9_-]{0,31}$/));
	return length(res) ? res : null;
}

// Normalised entry, or null for a malformed one.
function entry(e) {
	if (type(e) != 'object')
		return null;

	return {
		ip: e.ip,
		reports: e.distinct_reporters,
		country: (type(e.country) == 'string') ? e.country : null,
		sources: valid_sources(e.sources),
	};
}

// Calls cb(entry) for every entry of the saved list (cb(null) for malformed
// ones) – see list_lines(). Returns null or an error.
function each_entry(cb) {
	return list_lines(LIST_FILE, (e) => cb(entry(e)));
}

// GET /api/v1/ips into LIST_FILE -> { etag, not_modified, delta } or
// { error: '...' }. `etag` is the one of the previous list (from the state):
// the server answers 304 if it's current, or – delta sync – just the changes
// since then, which are applied to the saved list. Read the entries with
// each_entry().
// Full list (sorted by distinct_reporters, descending; country and sources
// optional), one entry per line:
//   [
//   {"ip":"198.51.100.23","distinct_reporters":17,"country":"CN","sources":["auth"]},
//   ...
//   ]
// Delta (see server/delta.go):
//   {"etag":"\"…\"","count":5000,"set_hash":"…","remove":["1.2.3.4"],"upsert":[
//   {"ip":"5.6.7.8","distinct_reporters":3},
//   ...
//   ]}
function fetch_blocklist(cfg, etag) {
	let url = sprintf('%s/api/v1/ips?min_reporters=%d&minutes=%d',
		cfg.server, cfg.min_reports, (cfg.report_window + 59) / 60);

	// Unpacked copy kept by 0.1.0-r13; the list is stored packed now.
	unlink(TMP_DIR + '/list.json');

	// Without the saved list a 304 or a delta would leave us with nothing.
	if (!stat(LIST_FILE))
		etag = null;

	// Deltas need If-None-Match, i.e. a uclient-fetch with --header.
	let res = http_get(cfg, (etag && supports_header()) ? url + '&delta=1' : url, etag);

	// A delta that doesn't fit the saved list: start over with the full one.
	let delta_failed = null;
	if (res.retry_full) {
		delta_failed = res.error;
		debug(cfg, 'delta rejected (%s), fetching the full list', delta_failed);
		res = http_get(cfg, url, null);
	}

	if (res.error)
		return res;

	return {
		etag: res.not_modified ? etag : res.etag,
		not_modified: !!res.not_modified,
		delta: res.delta,
		delta_failed,
	};
}

export { fetch_blocklist, each_entry };
