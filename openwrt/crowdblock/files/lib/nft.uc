// Manages the dedicated nftables table `inet crowdblock`.
//
// We use our own table instead of hooking into fw4: `fw4 reload` recreates
// table inet fw4 from scratch (and would wipe our set), but leaves other
// tables untouched. Our base chains run at priority filter - 10, i.e.
// before fw4's chains, so a drop here is final.
'use strict';

import { open, popen, unlink } from 'fs';

const TABLE = 'inet crowdblock';
const BATCH_FILE = '/tmp/crowdblock/batch.nft';
const CHUNK = 256;

function run_batch(script) {
	let f = open(BATCH_FILE, 'w');
	if (!f)
		return 'cannot write ' + BATCH_FILE;

	f.write(script);
	f.close();

	let p = popen('nft -f ' + BATCH_FILE + ' 2>&1', 'r');
	let out = p.read('all');
	let rc = p.close();

	unlink(BATCH_FILE);

	return (rc == 0) ? null : (trim(out) || `nft exit code ${rc}`);
}

// For single commands run outside the daemon (no shared batch file).
// `args` must be shell-safe, i.e. built from canonical IP addresses only.
function run_cmd(args) {
	let p = popen('nft ' + args + ' 2>&1', 'r');
	let out = p.read('all');
	let rc = p.close();

	return (rc == 0) ? null : (trim(out) || `nft exit code ${rc}`);
}

function set_of(ip) {
	return (index(ip, ':') >= 0) ? 'v6' : 'v4';
}

function hook_chain(name, hook) {
	return `	chain ${name} {
		type filter hook ${hook} priority filter - 10; policy accept;
		ip saddr @v4 counter drop
		ip6 saddr @v6 counter drop
	}
`;
}

// (Re)creates the table with empty sets. The first line makes sure the
// table exists so that `delete` never fails.
function setup(cfg) {
	let script = `table ${TABLE}
delete table ${TABLE}
table ${TABLE} {
	set v4 {
		type ipv4_addr
		flags timeout
		size ${cfg.max_entries}
	}
	set v6 {
		type ipv6_addr
		flags timeout
		size ${cfg.max_entries}
	}
${hook_chain('input', 'input')}`;

	if (cfg.block_forward)
		script += hook_chain('forward', 'forward');

	script += '}\n';

	return run_batch(script);
}

function exists() {
	return system(`nft list tables | grep -qx 'table ${TABLE}'`) == 0;
}

function teardown() {
	return run_batch(`table ${TABLE}\ndelete table ${TABLE}\n`);
}

function add_elements(lines, set, elems) {
	for (let i = 0; i < length(elems); i += CHUNK)
		push(lines, `add element ${TABLE} ${set} { ${join(', ', slice(elems, i, i + CHUNK))} }`);
}

// Replaces the contents of both sets atomically (one nft transaction).
// `entries` is { ip: expires_at_epoch }, addresses in `paused` are skipped.
// Kernel timeouts are a safety net: if the daemon dies, blocks still expire
// on time.
function apply(entries, now, paused) {
	let v4 = [], v6 = [];

	for (let ip, expires in entries) {
		let ttl = expires - now;
		if (ttl > 0 && !(ip in (paused ?? [])))
			push(index(ip, ':') >= 0 ? v6 : v4, `${ip} timeout ${ttl}s`);
	}

	let lines = [ `flush set ${TABLE} v4`, `flush set ${TABLE} v6` ];
	add_elements(lines, 'v4', v4);
	add_elements(lines, 'v6', v6);

	return run_batch(join('\n', lines) + '\n');
}

// Single-element changes for `crowdblock pause` / `resume`.
function block(ip, ttl) {
	return run_cmd(`add element ${TABLE} ${set_of(ip)} { ${ip} timeout ${ttl}s }`);
}

function unblock(ip) {
	return run_cmd(`delete element ${TABLE} ${set_of(ip)} { ${ip} }`);
}

// Rule lines with packet/byte counters, for `crowdblock status`.
// (Listing chains, not the table, avoids printing every set element.)
function counters() {
	let out = [];

	for (let chain in [ 'input', 'forward' ]) {
		let p = popen(`nft list chain ${TABLE} ${chain} 2>/dev/null`, 'r');
		if (!p)
			continue;

		for (let line = p.read('line'); length(line); line = p.read('line'))
			if (index(line, 'counter') >= 0)
				push(out, `${chain}: ${trim(line)}`);

		p.close();
	}

	return out;
}

export { setup, exists, teardown, apply, block, unblock, counters };
