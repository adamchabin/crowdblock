'use strict';
'require view';
'require fs';
'require poll';
'require uci';
'require ui';
'require crowdblock.i18n as i18n';

const _ = i18n.translate;

// Written by the daemon after every sync:
// { entries: { ip: expires_at }, reports: { ip: count }, countries: { ip: "CN" },
//   sources: { ip: [ "auth", "fail2ban" ] }, last_sync, last_error }
const STATE_FILE = '/tmp/crowdblock/state.json';

// read_direct (cgi-io) instead of read (rpcd): rpcd refuses files of 256 KiB
// and more, which the state reaches at a few thousand addresses.
function load_state() {
	return L.resolveDefault(fs.read_direct(STATE_FILE, 'json'), null).then(function(data) {
		return (data && typeof data == 'object') ? data : {};
	});
}

// Paused addresses (`crowdblock pause`) are committed outside of LuCI,
// so drop the cached config before reading it.
function load_paused() {
	uci.unload('crowdblock');

	return uci.load('crowdblock').then(function() {
		return L.toArray(uci.get('crowdblock', 'main', 'paused'));
	});
}

function load_all() {
	return Promise.all([ load_state(), load_paused() ]);
}

function format_time(epoch) {
	return epoch ? new Date(epoch * 1000).toLocaleString() : _('never');
}

// "CN" -> "🇨🇳 CN" (regional indicator symbols render as a flag)
function format_country(cc) {
	if (!cc || cc.length != 2)
		return '-';

	return String.fromCodePoint(0x1F1E6 + cc.charCodeAt(0) - 65, 0x1F1E6 + cc.charCodeAt(1) - 65) + ' ' + cc;
}

// 11100 -> "3h5m", 90000 -> "1d1h", 30 -> "<1m"
function format_duration(sec) {
	const d = Math.floor(sec / 86400), h = Math.floor(sec % 86400 / 3600), m = Math.floor(sec % 3600 / 60);

	if (d > 0)
		return d + 'd' + (h ? h + 'h' : '');

	if (h > 0)
		return h + 'h' + (m ? m + 'm' : '');

	return (m > 0) ? m + 'm' : '<1m';
}

return view.extend({
	// One after the other: load_paused() unloads the config i18n reads.
	load: function() {
		return i18n.load().then(load_all);
	},

	update: function(data) {
		const state = data[0], paused = data[1];
		const now = Date.now() / 1000;
		const entries = state.entries || {};
		const reports = state.reports || {};
		const countries = state.countries || {};
		const sources = state.sources || {};

		// Entries that expired since the last sync are only purged by the next one.
		const ips = Object.keys(entries).filter(function(ip) { return entries[ip] > now; });
		ips.sort(L.naturalCompare);

		const active = ips.filter(function(ip) { return paused.indexOf(ip) < 0; });
		const v6 = active.filter(function(ip) { return ip.indexOf(':') >= 0; }).length;

		this.status_rows.last_sync.textContent = format_time(state.last_sync);
		this.status_rows.last_error.textContent = state.last_error || '-';
		this.status_rows.blocked.textContent = _('%d IPv4, %d IPv6, %d paused').format(
			active.length - v6, v6, ips.length - active.length);

		this.current = { ips: ips, entries: entries, reports: reports, countries: countries, sources: sources, paused: paused, now: now };
		this.renderTable();
	},

	// Also called on every keystroke in the search field, without reloading.
	renderTable: function() {
		const c = this.current, filter = this.filter, cc = this.filter.toUpperCase();
		const entries = c.entries, reports = c.reports, countries = c.countries, sources = c.sources, paused = c.paused, now = c.now;
		const ips = filter ? c.ips.filter(function(ip) {
			return ip.indexOf(filter) >= 0 || countries[ip] == cc || (sources[ip] || []).indexOf(filter) >= 0;
		}) : c.ips;

		this.match_count.textContent = filter ? _('%d of %d addresses').format(ips.length, c.ips.length) : '';

		cbi_update_table(this.table, ips.map(L.bind(function(ip) {
			const is_paused = paused.indexOf(ip) >= 0;

			return [
				ip,
				format_country(countries[ip]),
				(reports[ip] != null) ? reports[ip] : '-',
				(sources[ip] || []).join(', ') || '-',
				is_paused ? E('em', _('paused')) :
					'%s (%s)'.format(format_time(entries[ip]), format_duration(entries[ip] - now)),
				E('div', { 'class': 'right' }, E('button', {
					'class': 'cbi-button ' + (is_paused ? 'cbi-button-apply' : 'cbi-button-neutral'),
					'title': is_paused ? _('Block this address again') : _('Stop blocking this address'),
					'click': ui.createHandlerFn(this, 'handlePause', ip, !is_paused),
				}, is_paused ? _('Resume') : _('Pause'))),
			];
		}, this)), E('em', filter ? _('No matching addresses') : _('No blocked addresses')));
	},

	refresh: function() {
		return load_all().then(L.bind(this.update, this));
	},

	handlePause: function(ip, pause) {
		return fs.exec('/usr/sbin/crowdblock', [ pause ? 'pause' : 'resume', ip ]).then(function(res) {
			if (res.code != 0)
				ui.addNotification(null, E('p', _('Command failed: %s').format(res.stderr || res.stdout || res.code)), 'error');
		}).catch(function(e) {
			ui.addNotification(null, E('p', e.message), 'error');
		}).then(L.bind(this.refresh, this));
	},

	handleSync: function() {
		return fs.exec('/usr/sbin/crowdblock', [ 'sync' ]).then(function(res) {
			if (res.code != 0)
				ui.addNotification(null, E('p', _('Sync failed: %s').format(res.stderr || res.stdout || res.code)), 'error');
		}).catch(function(e) {
			ui.addNotification(null, E('p', e.message), 'error');
		}).then(L.bind(this.refresh, this));
	},

	render: function(data) {
		const enabled = uci.get('crowdblock', 'main', 'enabled') == '1';

		this.filter = '';
		this.match_count = E('span');

		this.status_rows = {
			last_sync: E('td', { 'class': 'td left' }),
			last_error: E('td', { 'class': 'td left' }),
			blocked: E('td', { 'class': 'td left' }),
		};

		this.table = E('table', { 'class': 'table' }, [
			E('tr', { 'class': 'tr table-titles' }, [
				E('th', { 'class': 'th' }, _('IP address')),
				E('th', { 'class': 'th' }, _('Country')),
				E('th', { 'class': 'th', 'title': _('Distinct users who reported the address in the report window') }, _('Reports')),
				E('th', { 'class': 'th', 'title': _('What detected the attacks: the plugins of the reporters') }, _('Source')),
				E('th', { 'class': 'th' }, _('Blocked until')),
				E('th', { 'class': 'th right' }, ''),
			])
		]);

		const view = E([], [
			E('h2', _('Blocked addresses')),
			E('div', { 'class': 'cbi-map-descr' },
				_('Addresses downloaded from the server in the last sync. The list refreshes automatically.')),

			E('table', { 'class': 'table' }, [
				E('tr', { 'class': 'tr' }, [ E('td', { 'class': 'td left', 'width': '33%' }, _('Last sync')), this.status_rows.last_sync ]),
				E('tr', { 'class': 'tr' }, [ E('td', { 'class': 'td left' }, _('Last error')), this.status_rows.last_error ]),
				E('tr', { 'class': 'tr' }, [ E('td', { 'class': 'td left' }, _('Blocked')), this.status_rows.blocked ]),
			]),

			E('div', { 'style': 'display:flex; flex-wrap:wrap; gap:.5em 1em; align-items:center; margin:1em 0' }, [
				E('input', {
					'type': 'search',
					'class': 'cbi-input-text',
					'placeholder': _('IP address, country code or source'),
					'input': L.bind(function(ev) {
						this.filter = ev.target.value.trim();
						this.renderTable();
					}, this),
				}),
				this.match_count,
				E('button', {
					'class': 'cbi-button cbi-button-action',
					'style': 'margin-left:auto',
					'disabled': enabled ? null : true,
					'title': enabled ? null : _('Enable crowdblock in the settings first'),
					'click': ui.createHandlerFn(this, 'handleSync'),
				}, _('Sync now')),
			]),

			this.table,

			// Required by the CC BY 4.0 license of the server's GeoIP database.
			E('div', { 'class': 'cbi-section-descr' }, [
				_('Country data: '),
				E('a', { 'href': 'https://db-ip.com', 'target': '_blank', 'rel': 'noreferrer' }, 'IP Geolocation by DB-IP'),
			]),
		]);

		this.update(data);

		poll.add(L.bind(this.refresh, this), 10);

		return view;
	},

	handleSave: null,
	handleSaveApply: null,
	handleReset: null
});
