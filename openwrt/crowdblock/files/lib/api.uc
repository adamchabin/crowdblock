// Downloads the blocklist from the collector server through uclient-fetch.
'use strict';

import { popen, open, stat, unlink, rename } from 'fs';

export const VERSION = '0.1.0';

// Downloads go to this directory (RAM), so the body never lands on flash.
const TMP_DIR = '/tmp/crowdblock';

// The last downloaded list, as received, under a name that says what it is:
// list.gz (gzipped) or list.json (plain). Parsed line by line while
// unpacking, it is never unpacked to a file. null until a download succeeds.
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

// Runs uclient-fetch, writing the body to `file`. Returns null on success,
// else the error message.
function download(cfg, url, file, errfile) {
	// Not -q: it would also hide the "HTTP error <status>" line.
	let argv = [
		'uclient-fetch', '-O', file,
		'--timeout=' + cfg.timeout,
		'--user-agent=crowdblock/' + VERSION,
	];

	if (!cfg.verify_tls)
		push(argv, '--no-check-certificate');

	// Compressed list: about 7x smaller on the wire. each_entry() unpacks it.
	if (supports_header())
		push(argv, '--header=Accept-Encoding: gzip', '--header=X-API-Key: ' + cfg.api_key);
	else
		push(argv, '--user=crowdblock', '--password=' + cfg.api_key);

	push(argv, url);

	let rc = system(join(' ', map(argv, shell_quote)) + ' 2>' + shell_quote(errfile));
	if (rc == 0)
		return null;

	let stderr = trim(read_text(errfile));
	let status = match(stderr, /HTTP error ([0-9]+)/);
	let msg = status
		? sprintf('server returned HTTP %s', status[1])
		: sprintf('HTTP request failed (uclient-fetch exit code %d)', rc);

	return stderr ? msg + ': ' + stderr : msg;
}

// Downloads the full list to list.gz or list.json (by content). Returns null
// or an error. The previous list is removed first, so a failed download
// never leaves a stale list behind.
function fetch_list(cfg) {
	max_body_bytes = cfg.max_body * 1024 * 1024;

	let url = sprintf('%s/api/v1/ips?min_reporters=%d&minutes=%d',
		cfg.server, cfg.min_reports, int((cfg.report_window + 59) / 60));

	if (cfg.limit > 0)
		url += sprintf('&limit=%d', cfg.limit);

	unlink(TMP_DIR + '/list.gz');
	unlink(TMP_DIR + '/list.json');
	list_file = null;

	let part = TMP_DIR + '/list.part';
	let err = download(cfg, url, part, TMP_DIR + '/err');
	if (err) {
		unlink(part);
		return err;
	}

	let st = stat(part);
	if (st?.size > max_body_bytes) {
		unlink(part);
		return 'response too large';
	}

	let name = TMP_DIR + (is_gzip(part) ? '/list.gz' : '/list.json');
	if (!rename(part, name)) {
		unlink(part);
		return 'cannot write ' + name;
	}

	list_file = name;
	return null;
}

// Calls cb(entry, line) for every entry of the downloaded list, parsing it
// line by line while unpacking: memory use doesn't grow with the list. Reads
// at most max_body_bytes unpacked. Returns null or an error.
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

export { fetch_list, each_entry, shell_quote };
