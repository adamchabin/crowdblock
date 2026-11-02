// Manages the dedicated nftables table `inet crowdblock`.
//
// We use our own table instead of hooking into fw4: `fw4 reload` recreates
// table inet fw4 from scratch (and would wipe our set), but leaves other
// tables untouched. Our base chains run at priority filter - 10, i.e.
// before fw4's chains, so a drop here is final.
'use strict';

import { open, popen, unlink } from 'fs';
import { debug } from 'crowdblock.logging';

const TABLE = 'inet crowdblock';
const BATCH_FILE = '/tmp/crowdblock/batch.nft';
const CHUNK = 2048;

// Higher precision timer using time() which returns seconds with microsecond precision.
function timer_start() {
	return time();
}

function timer_elapsed_ms(start) {
	return int((time() - start) * 1000);
}

// Shell timeout: busybox/coreutils `timeout` kills the command after `secs`
// and exits 124. If `timeout` is missing, the command runs without a limit
// (rc will be 127 "not found", still an error, still better than a hang).
function with_timeout(cmd, secs) {
	return 'timeout -s KILL ' + secs + ' ' + cmd + ' 2>&1';
}

function run_batch(script) {
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

	unlink(BATCH_FILE);

	if (rc == 124)
		return 'nft timed out after 30s';
	if (rc != 0) {
		let code = rc;
		let what = trim(out) || sprintf('nft exit code %d', code);
		return what;
	}
	return null;
}

// For single commands run outside the daemon (no shared batch file).
// `args` must be shell-safe, i.e. built from canonical IP addresses only.
function run_cmd(args) {
	let p = popen(with_timeout('nft ' + args, 10), 'r');
	let out = p.read('all');
	let rc = p.close();

	if (rc == 124)
		return 'nft timed out after 10s';
	return (rc == 0) ? null : (trim(out) || `nft exit code ${rc}`);
}

function set_of(ip) {
	return (index(ip, ':') >= 0) ? 'v6' : 'v4';
}

function hook_chain(name, hook) {
	return '	chain ' + name + ' {\n' +
		'		type filter hook ' + hook + ' priority filter -10; policy accept;\n' +
		'		ip saddr @wl4 accept\n' +
		'		ip6 saddr @wl6 accept\n' +
		'		ip saddr @v4 counter drop\n' +
		'		ip6 saddr @v6 counter drop\n' +
		'	}\n';
}

function add_elements(lines, set, elems) {
	for (let i = 0; i < length(elems); i += CHUNK)
		push(lines, 'add element ' + TABLE + ' ' + set + ' { ' + join(', ', slice(elems, i, i + CHUNK)) + ' }');
}

// Helper to add elements to a set (similar to add_elements but for custom sets)
function add_set_elements(lines, set, elems) {
	for (let i = 0; i < length(elems); i += CHUNK) {
		push(lines, 'add element ' + TABLE + ' ' + set + ' { ' + join(', ', slice(elems, i, i + CHUNK)) + ' }');
	}
}

// Adds whitelist elements to wl4/wl6 sets (no timeout for whitelist).
function add_whitelist_elements(lines, elems) {
	for (let i = 0; i < length(elems); i += CHUNK) {
		let chunk = slice(elems, i, i + CHUNK);
		let v4 = [], v6 = [];
		for (let ip in chunk) {
			// ucode arrays have no methods: use push()
			push(index(ip, ':') >= 0 ? v6 : v4, ip);
		}
		if (length(v4) > 0)
			push(lines, 'add element ' + TABLE + ' wl4 { ' + join(', ', v4) + ' }');
		if (length(v6) > 0)
			push(lines, 'add element ' + TABLE + ' wl6 { ' + join(', ', v6) + ' }');
	}
}

// Creates the nftables table, sets, and chains if they don't exist.
// Returns null on success, error string on failure.
function setup(cfg) {
	let lines = [
		'table ' + TABLE + ' {',
		'	set v4 { type ipv4_addr; flags timeout; }',
		'	set v6 { type ipv6_addr; flags timeout; }',
		'	set wl4 { type ipv4_addr; }',
		'	set wl6 { type ipv6_addr; }',
		hook_chain('input', 'input'),
		hook_chain('forward', 'forward'),
		'}'
	];
	return run_batch(join('\n', lines) + '\n');
}

// Checks if the nftables table exists.
function exists() {
	let p = popen(with_timeout('nft list table ' + TABLE + ' 2>/dev/null', 10), 'r');
	if (!p)
		return false;
	let out = p.read('all');
	p.close();
	return length(out) > 0;
}

// Removes the entire nftables table (unblocks everything).
function teardown() {
	return run_cmd('delete table ' + TABLE);
}

// Replaces the contents of both sets atomically (one nft transaction).
// `entries` is { ip: expires_at_epoch }, addresses in `paused` are skipped.
// Kernel timeouts are a safety net: if the daemon dies, blocks still expire
// on time.
function apply(cfg, entries, now, paused) {
	let v4 = [], v6 = [];

	for (let ip, expires in entries) {
		let ttl = expires - now;
		if (ttl > 0 && !(ip in (paused ?? [])))
			push(index(ip, ':') >= 0 ? v6 : v4, ip + ' timeout ' + ttl + 's');
	}

	if (length(v4) == 0 && length(v6) == 0)
		return null; // nothing to add

	// Combine v4 and v6 into a single batch for better performance
	let lines = [];
	let apply_start = timer_start();
	if (length(v4) > 0)
		add_elements(lines, 'v4', v4);
	if (length(v6) > 0)
		add_elements(lines, 'v6', v6);

	let err = run_batch(join('\n', lines) + '\n');
	debug(cfg, 'nft.apply: batch took %d ms', timer_elapsed_ms(apply_start));
	return err;
}

// Single-element changes for `crowdblock pause` / `resume`.
function block(ip, ttl) {
	return run_cmd('add element ' + TABLE + ' ' + set_of(ip) + ' { ' + ip + ' timeout ' + ttl + 's }');
}

function unblock(ip) {
	return run_cmd('delete element ' + TABLE + ' ' + set_of(ip) + ' { ' + ip + ' }');
}

// Rule lines with packet/byte counters, for `crowdblock status`.
// (Listing chains, not the table, avoids printing every set element.)
function counters() {
	let out = [];

	for (let chain in [ 'input', 'forward' ]) {
		let p = popen(with_timeout('nft list chain ' + TABLE + ' ' + chain + ' 2>/dev/null', 10), 'r');
		if (!p)
			continue;

		for (let line = p.read('line'); length(line); line = p.read('line')) {
			if (index(line, 'counter') >= 0)
				push(out, chain + ': ' + trim(line));
		}

		p.close();
	}

	return out;
}

export { setup, exists, teardown, apply, block, unblock, counters, add_whitelist_elements, run_batch };