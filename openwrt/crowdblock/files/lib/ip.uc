// IP address helpers: parsing, CIDR matching, whitelist.
'use strict';

// Networks that must never end up in the blocklist.
const RESERVED = [
	'0.0.0.0/8', '10.0.0.0/8', '100.64.0.0/10', '127.0.0.0/8',
	'169.254.0.0/16', '172.16.0.0/12', '192.0.0.0/24', '192.0.2.0/24',
	'192.168.0.0/16', '198.18.0.0/15', '198.51.100.0/24', '203.0.113.0/24',
	'224.0.0.0/4', '240.0.0.0/4',
	'::/128', '::1/128', '::ffff:0:0/96', '64:ff9b::/96', '2001:db8::/32',
	'fc00::/7', 'fe80::/10', 'ff00::/8',
];

// "addr" or "addr/len" -> { bytes, len } or null
function parse_cidr(str) {
	if (type(str) != 'string')
		return null;

	let parts = split(trim(str), '/');
	if (length(parts) > 2)
		return null;

	let bytes = iptoarr(parts[0]);
	if (!bytes)
		return null;

	let maxlen = length(bytes) * 8;
	let len = maxlen;

	if (length(parts) == 2) {
		if (!match(parts[1], /^[0-9]{1,3}$/))
			return null;

		len = +parts[1];
		if (len > maxlen)
			return null;
	}

	return { bytes, len };
}

function cidr_contains(net, bytes) {
	if (length(net.bytes) != length(bytes))
		return false;

	let full = int(net.len / 8);
	let rem = net.len % 8;

	for (let i = 0; i < full; i++)
		if (net.bytes[i] != bytes[i])
			return false;

	if (rem) {
		let mask = (0xff << (8 - rem)) & 0xff;
		if ((net.bytes[full] & mask) != (bytes[full] & mask))
			return false;
	}

	return true;
}

// Build a whitelist from user entries + reserved ranges.
// Invalid entries are reported via `on_invalid(entry)`.
function whitelist(entries, on_invalid) {
	let nets = [];

	for (let list in [ RESERVED, entries ]) {
		for (let e in list) {
			let n = parse_cidr(e);
			if (n)
				push(nets, n);
			else if (on_invalid)
				on_invalid(e);
		}
	}

	return nets;
}

function whitelisted(nets, bytes) {
	for (let n in nets)
		if (cidr_contains(n, bytes))
			return true;

	return false;
}

export { parse_cidr, whitelist, whitelisted };
