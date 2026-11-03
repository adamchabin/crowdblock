// Manages the dedicated nftables table `inet crowdblock`.
//
// We use our own table instead of hooking into fw4: `fw4 reload` recreates
// table inet fw4 from scratch (and would wipe our set), but leaves other
// tables untouched. Our base chains run at priority filter - 10, i.e.
// before fw4's chains, so a drop here is final.
'use strict';

import { open, popen } from 'fs';

const TABLE = 'inet crowdblock';
const BATCH_FILE = '/tmp/crowdblock/batch.nft';
const CHUNK = 2048;

// Shell timeout: busybox/coreutils `timeout` kills the command after `secs`
// and exits 124. If `timeout` is missing, the command runs without a limit
// (rc will be 127 "not found", still an error, still better than a hang).
function with_timeout(cmd, secs) {
	return 'timeout -s KILL ' + secs + ' ' + cmd + ' 2>&1';
}

// Runs an nft script as one transaction: it applies completely or not at all.
// Returns null on success, error string on failure.
function run_batch(script, cfg) {
	let f = open(BATCH_FILE, 'w');
	if (!f)
		return 'cannot write ' + BATCH_FILE;

	f.write(script);
	f.close();

	let p = popen(with_timeout('nft -f ' + BATCH_FILE, 30), 'r');
	if (!p)
		return 'cannot run nft';

	let out = p.read('all');
	let rc = p.close();

	// Kept on purpose: the last batch stays in RAM for debugging. The next
	// run overwrites it.

	if (rc == 124)
		return 'nft timed out after 30s';
	if (rc != 0) {
		let code = rc;
		let what = trim(out) || sprintf('nft exit code %d', code);
		return what;
	}

	if (cfg && !cfg.keep_batch_nft)
		system('rm -f ' + BATCH_FILE);

	return null;
}

// For single commands (teardown). `args` must be shell-safe.
function run_cmd(args) {
	let p = popen(with_timeout('nft ' + args, 10), 'r');
	let out = p.read('all');
	let rc = p.close();

	if (rc == 124)
		return 'nft timed out after 10s';
	return (rc == 0) ? null : (trim(out) || `nft exit code ${rc}`);
}

function hook_chain(name, hook) {
	return '	chain ' + name + ' {\n' +
		'		type filter hook ' + hook + ' priority filter -10; policy accept;\n' +
		'		ip saddr @v4 counter drop\n' +
		'		ip6 saddr @v6 counter drop\n' +
		'	}\n';
}

function add_elements(lines, set, elems) {
	for (let i = 0; i < length(elems); i += CHUNK)
		push(lines, 'add element ' + TABLE + ' ' + set + ' { ' + join(', ', slice(elems, i, i + CHUNK)) + ' }');
}

// Creates the nftables table, sets, and chains.
// Returns null on success, error string on failure.
function setup(cfg) {
	let lines = [
		'table ' + TABLE + ' {',
		'	set v4 { type ipv4_addr; flags timeout; }',
		'	set v6 { type ipv6_addr; flags timeout; }',
		hook_chain('input', 'input'),
		hook_chain('forward', 'forward'),
		'}'
	];
	return run_batch(join('\n', lines) + '\n', cfg);
}

// True if an error from load() means the table (or its sets) is missing,
// e.g. after `fw4 stop` flushed all tables. Callers recreate it with setup().
function is_missing_table(err) {
	return match(err ?? '', /No such file or directory/) != null;
}

// Removes the entire nftables table (unblocks everything).
function teardown() {
	let err = run_cmd('delete table ' + TABLE);
	if (err && match(err, /No such file or directory/))
		return null;
	return err;
}

// Replaces the whole blocklist with `ips` in one transaction: both sets are
// flushed and refilled together, so there is never a partial list. Every
// element gets a kernel timeout of cfg.block_time - a safety net if the
// daemon dies, since the next sync refreshes all of them anyway.
function load(cfg, ips) {
	let v4 = [], v6 = [];
	for (let ip in ips)
		push(index(ip, ':') >= 0 ? v6 : v4, ip + ' timeout ' + cfg.block_time + 's');

	let lines = [ 'flush set ' + TABLE + ' v4', 'flush set ' + TABLE + ' v6' ];
	add_elements(lines, 'v4', v4);
	add_elements(lines, 'v6', v6);

	return run_batch(join('\n', lines) + '\n', cfg);
}

export { setup, is_missing_table, teardown, load };
