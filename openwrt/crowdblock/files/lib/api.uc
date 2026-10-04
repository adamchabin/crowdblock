// Talks to the collector server through uclient-fetch.
'use strict';

import { popen, open, stat, unlink, rmdir, mkdtemp, rename } from 'fs';

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
// most MAX_BODY bytes unpacked (a gzip bomb can't fill the RAM). Returns null
// or an error.
function check_body(file, rcfile) {
	let size = stat(file)?.size;
	if (size == null)
		return 'no response body';

	if (size > MAX_BODY)
		return 'response too large';

	// The exit code of gunzip, not of head / wc, tells whether it was valid.
	let p = popen(sprintf('(%s 2>/dev/null; echo $? > %s) | head -c %d | wc -c',
		cat_command(file), shell_quote(rcfile), MAX_BODY + 1), 'r');
	let unpacked = p ? +trim(p.read('all') ?? '') : null;
	if (p)
		p.close();

	if (unpacked == null || unpacked > MAX_BODY)
		return 'response too large';

	if (trim(read_file(rcfile) ?? '') != '0')
		return 'cannot unpack the gzip response';

	return null;
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

// Downloads the list into LIST_FILE. Returns { etag }, { not_modified: true }
// (LIST_FILE is still current) or { error }.
function http_get(cfg, url, etag) {
	let dir = mkdtemp(TMP_DIR + '/fetch.XXXXXX');
	if (!dir)
		return { error: 'cannot create a temporary directory in ' + TMP_DIR };

	let files = { body: dir + '/body', err: dir + '/err', rc: dir + '/rc' };
	let res = download(cfg, url, files.body, files.err, etag);

	if (!res.error && !res.not_modified) {
		let err = check_body(files.body, files.rc);
		if (err)
			res = { error: err };
		else if (!rename(files.body, LIST_FILE))
			res = { error: 'cannot write ' + LIST_FILE };
		else
			res = { etag: etag_of(LIST_FILE) };
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
// ones), parsing it line by line while unpacking: memory use doesn't grow
// with the list. A list in one line (servers before the line format) is
// parsed as a whole. Returns null or an error.
function each_entry(cb) {
	if (!stat(LIST_FILE))
		return 'no list downloaded';

	let p = popen(sprintf('%s 2>/dev/null | head -c %d', cat_command(LIST_FILE), MAX_BODY + 1), 'r');
	if (!p)
		return 'cannot read ' + LIST_FILE;

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
					cb(entry(e));

			done = true;
			break;
		}

		if (line == ']') {
			done = true;
			break;
		}

		let e;
		try { e = json(line); } catch (x) { }
		cb(entry(e));
	}

	p.close();

	if (!err && !done)
		err = started ? 'truncated list' : 'unexpected response format';

	return err;
}

// GET /api/v1/ips into LIST_FILE -> { etag, not_modified } or { error: '...' }.
// `etag` is the one of the previous list (from the state); if the list hasn't
// changed, the saved copy stays. Read the entries with each_entry().
// Expected response (sorted by distinct_reporters, descending; country and
// sources are optional), one entry per line:
//   [
//   { "ip": "198.51.100.23", "distinct_reporters": 17, "country": "CN",
//     "sources": [ "auth", "fail2ban" ] },
//   ...
//   ]
function fetch_blocklist(cfg, etag) {
	let url = sprintf('%s/api/v1/ips?min_reporters=%d&minutes=%d',
		cfg.server, cfg.min_reports, (cfg.report_window + 59) / 60);

	// Unpacked copy kept by 0.1.0-r13; the list is stored packed now.
	unlink(TMP_DIR + '/list.json');

	// Without the saved list a 304 would leave us with nothing.
	if (!stat(LIST_FILE))
		etag = null;

	let res = http_get(cfg, url, etag);
	if (res.error)
		return res;

	return {
		etag: res.not_modified ? etag : res.etag,
		not_modified: !!res.not_modified,
	};
}

export { fetch_blocklist, each_entry };
