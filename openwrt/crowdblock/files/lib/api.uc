// Downloads the blocklist from the collector server through uclient-fetch.
'use strict';

import { popen, open, stat, unlink, rename } from 'fs';

export const VERSION = '0.1.0';

// Downloads go to this directory (RAM), so the body never lands on flash.
const TMP_DIR = '/tmp/crowdblock';

// Exit code of uclient-fetch for HTTP errors; the status is on stderr.
const UCLIENT_HTTP_ERROR = 8;

// The last full list, as received, under a name that says what it is:
// list.gz (gzipped) or list.json (plain). Parsed line by line while
// unpacking, it is never unpacked to a file. null until a full list is fetched.
let list_file = null;

// Response bigger than this is rejected (protects RAM). Set from cfg.max_body
// (MiB) at the start of fetch_list().
let max_body_bytes = 0;

function shell_quote(s) {
	return "'" + replace('' + s, "'", "'\\''") + "'";
}

// uclient-fetch gained --header in 2025; older ones (OpenWrt 23.05/24.10)
// reject the option, so the API key goes in Basic auth there instead.
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

function read_text(path) {
	let f = open(path, 'r');
	if (!f)
		return '';

	let data = f.read('all');
	f.close();
	return data ?? '';
}

// The gzip magic number is the first two bytes of the file.
function is_gzip(file) {
	let f = open(file, 'r');
	let magic = f ? f.read(2) : null;
	if (f)
		f.close();

	return magic == '\x1f\x8b';
}

// `cat` for plain bodies, `gunzip -c` for gzipped ones.
function cat_command(file) {
	return (is_gzip(file) ? 'gunzip -c ' : 'cat ') + shell_quote(file);
}

// Runs uclient-fetch, writing the body to `file`. Returns {} on success,
// { not_modified: true } on 304 (no body), or { error }. `etag` is sent as
// If-None-Match when given.
function download(cfg, url, file, errfile, etag) {
	// Not -q: it would also hide the "HTTP error <status>" line.
	let argv = [
		'uclient-fetch', '-O', file,
		'--timeout=' + cfg.timeout,
		'--user-agent=crowdblock/' + VERSION,
	];

	if (!cfg.verify_tls)
		push(argv, '--no-check-certificate');

	// Compressed list: about 7x smaller on the wire. each_entry() unpacks it.
	if (supports_header()) {
		push(argv, '--header=Accept-Encoding: gzip', '--header=X-API-Key: ' + cfg.api_key);
		if (etag)
			push(argv, '--header=If-None-Match: ' + etag);
	}
	else {
		push(argv, '--user=crowdblock', '--password=' + cfg.api_key);
	}

	push(argv, url);

	let rc = system(join(' ', map(argv, shell_quote)) + ' 2>' + shell_quote(errfile));
	if (rc == 0)
		return {};

	let stderr = trim(read_text(errfile));
	let status = match(stderr, /HTTP error ([0-9]+)/);

	if (rc == UCLIENT_HTTP_ERROR && status?.[1] == '304')
		return { not_modified: true };

	let msg = status
		? sprintf('server returned HTTP %s', status[1])
		: sprintf('HTTP request failed (uclient-fetch exit code %d)', rc);

	return { error: stderr ? msg + ': ' + stderr : msg };
}

// The first character of a body: '[' for a full list, '{' for a delta.
function body_kind(file) {
	let p = popen(sprintf('%s 2>/dev/null | head -c 1', cat_command(file)), 'r');
	let c = p ? p.read('all') : null;
	if (p)
		p.close();

	return c ?? '';
}

// Same as the server's ETag: the first 16 hex digits of the SHA-256 of the
// unpacked list, quoted. Returns null if it can't be computed.
function etag_of(file) {
	let p = popen(sprintf('%s 2>/dev/null | sha256sum', cat_command(file)), 'r');
	let out = p ? p.read('all') : null;
	if (p)
		p.close();

	let m = match(out ?? '', /^([0-9a-f]{16})/);
	return m ? `"${m[1]}"` : null;
}

// Same as the server's set_hash: the first 16 hex digits of the SHA-256 of
// the sorted addresses, each followed by "\n". Sorting is bytewise (LC_ALL=C),
// like Go's string sort on the server. `ips` is an array of canonical
// addresses. Returns the hash, or null if it can't be computed.
function set_hash(ips) {
	let file = TMP_DIR + '/ips.txt';
	let f = open(file, 'w');
	if (!f)
		return null;

	for (let ip in ips)
		f.write(ip + '\n');
	f.close();

	let p = popen(sprintf('LC_ALL=C sort %s | sha256sum', shell_quote(file)), 'r');
	let out = p ? p.read('all') : null;
	if (p)
		p.close();

	unlink(file);

	let m = match(out ?? '', /^([0-9a-f]{16})/);
	return m ? m[1] : null;
}

// Reads a delta body. Returns the parsed object, or null if it is malformed.
//   {"etag":"\"…\"","count":5000,"set_hash":"…","remove":["1.2.3.4"],"upsert":[
//   {"ip":"5.6.7.8","distinct_reporters":3},
//   …
//   ]}
function read_delta(file) {
	let p = popen(sprintf('%s 2>/dev/null | head -c %d', cat_command(file), max_body_bytes + 1), 'r');
	let d;
	try { d = json(p); } catch (e) { }
	if (p)
		p.close();

	if (type(d) != 'object' || type(d.etag) != 'string' || type(d.count) != 'int' ||
	    type(d.set_hash) != 'string' || type(d.upsert) != 'array' || type(d.remove) != 'array')
		return null;

	return d;
}

// GET /api/v1/ips. `etag` is the ETag of the list the caller holds, or null.
// With it, the server answers 304 if nothing changed, or with a delta against
// that list. Returns one of:
//   { not_modified: true }  the held list is current
//   { delta: {...} }        the changes since `etag`; delta.etag is the new one
//   { etag }                the full list was downloaded into list_file
//   { error }
function fetch_list(cfg, etag) {
	max_body_bytes = cfg.max_body * 1024 * 1024;

	let url = sprintf('%s/api/v1/ips?min_reporters=%d&minutes=%d',
		cfg.server, cfg.min_reports, int((cfg.report_window + 59) / 60));

	if (cfg.limit > 0)
		url += sprintf('&limit=%d', cfg.limit);

	// Deltas need If-None-Match, i.e. uclient-fetch with --header.
	let delta = etag && supports_header();
	if (delta)
		url += '&delta=1';

	unlink(TMP_DIR + '/list.gz');
	unlink(TMP_DIR + '/list.json');
	list_file = null;

	let part = TMP_DIR + '/list.part';
	let res = download(cfg, url, part, TMP_DIR + '/err', delta ? etag : null);
	if (res.not_modified)
		return res;

	if (res.error) {
		unlink(part);
		return res;
	}

	let st = stat(part);
	if (st?.size > max_body_bytes) {
		unlink(part);
		return { error: 'response too large' };
	}

	if (body_kind(part) == '{') {
		let d = read_delta(part);
		unlink(part);

		// A malformed delta: the full list is the only safe answer.
		return d ? { delta: d } : fetch_list(cfg, null);
	}

	let name = TMP_DIR + (is_gzip(part) ? '/list.gz' : '/list.json');
	if (!rename(part, name)) {
		unlink(part);
		return { error: 'cannot write ' + name };
	}

	list_file = name;
	return { etag: etag_of(name) };
}

// Calls cb(entry, line) for every entry of the downloaded full list, parsing
// it line by line while unpacking: memory use doesn't grow with the list.
// Reads at most max_body_bytes unpacked. Returns null or an error.
//
// Expected format, one entry per line:
//   [
//   {"ip":"198.51.100.23","distinct_reporters":17,"country":"CN","sources":["auth"]},
//   ...
//   ]
function each_entry(cb) {
	if (!list_file)
		return 'no list downloaded';

	let p = popen(sprintf('%s 2>/dev/null | head -c %d', cat_command(list_file), max_body_bytes + 1), 'r');
	if (!p)
		return 'cannot read ' + list_file;

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
					cb(e);

			done = true;
			break;
		}

		if (line == ']') {
			done = true;
			break;
		}

		let e;
		try { e = json(line); } catch (x) { }
		cb(e);
	}

	p.close();

	if (!err && !done)
		err = started ? 'truncated list' : 'unexpected response format';

	return err;
}

export { fetch_list, each_entry, set_hash, shell_quote };
