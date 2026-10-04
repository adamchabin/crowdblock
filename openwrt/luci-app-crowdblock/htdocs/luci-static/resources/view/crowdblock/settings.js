'use strict';
'require view';
'require form';
'require crowdblock.i18n as i18n';

const _ = i18n.translate;

// Same formats and ranges as accepted by /usr/share/ucode/crowdblock/config.uc
// (the daemon refuses to start on an invalid value).
// Keep regex literals out of `return` statements: jsmin (used by the LuCI
// build) parses a regex after `return` as division and breaks the file.
const URL_RE = /^https?:\/\/[^\s'"]+$/;
const DURATION_RE = /^([0-9]+)([smhd]?)$/;
const DURATION_MULT = { '': 1, s: 1, m: 60, h: 3600, d: 86400 };

function validate_url(section_id, value) {
	return URL_RE.test(value) || _('Expecting an http:// or https:// URL');
}

// "90" -> 90, "30m" -> 1800, "24h" -> 86400; null if invalid
function parse_duration(value) {
	const m = DURATION_RE.exec(value);
	return m ? +m[1] * DURATION_MULT[m[2]] : null;
}

// Value of a duration option in the form, its default if empty.
function form_duration(option, section_id, def) {
	const value = option.formvalue(section_id);
	return (value == null || value == '') ? def : parse_duration(value);
}

// min/max in seconds, shown as e.g. "1m" / "30d". `check(section_id, n)`
// validates the value (default `def` if empty) against other options.
function duration_validator(min, max, min_str, max_str, def, check) {
	return function(section_id, value) {
		const n = (value == '') ? def : parse_duration(value);

		if (n == null)
			return _('Expecting seconds or a number with s, m, h or d suffix (e.g. 30m, 24h)');

		if (value != '' && (n < min || n > max))
			return _('Expecting a value between %s and %s').format(min_str, max_str);

		return check ? check(section_id, n) : true;
	};
}

return view.extend({
	load: function() {
		return i18n.load();
	},

	render: function() {
		let m, s, o, block_time, sync_interval;

		m = new form.Map('crowdblock', _('Crowdblock'),
			_('Blocks IP addresses reported by other users of the crowdblock server.'));

		s = m.section(form.NamedSection, 'main', 'crowdblock');
		s.tab('general', _('General Settings'));
		s.tab('advanced', _('Advanced Settings'));

		o = s.taboption('general', form.Flag, 'enabled', _('Enable'));
		o.rmempty = false;

		o = s.taboption('general', form.Value, 'server', _('Server URL'),
			_('Base URL of the crowdblock server, e.g. https://crowdblock.example.org'));
		o.rmempty = false;
		o.validate = validate_url;

		o = s.taboption('general', form.Value, 'api_key', _('API key'));
		o.password = true;
		o.rmempty = false;

		o = s.taboption('general', form.ListValue, 'language', _('Language'),
			_('Language of the crowdblock pages. Takes effect after saving and reloading the page.'));
		o.value('', _('Auto (LuCI language)'));
		for (const code in i18n.languages)
			o.value(code, i18n.languages[code]);

		// Fixed thresholds served by the server (same lists as in config.uc).
		o = s.taboption('general', form.ListValue, 'min_reports', _('Minimum reports'),
			_('How many distinct users must report an address before it is blocked.'));
		for (const n of [ 1, 5, 10, 20, 50 ])
			o.value(String(n));
		o.default = '5';

		o = s.taboption('advanced', form.ListValue, 'report_window', _('Report window'),
			_('Only reports from this recent period are counted.'));
		o.value('1h', _('1 hour'));
		o.value('6h', _('6 hours'));
		o.value('24h', _('24 hours'));
		o.value('7d', _('7 days'));
		o.default = '6h';

		// Same rule as in config.uc: a block must outlive the next sync (with
		// room for one failed sync). Checked on both options, as LuCI only
		// re-validates the field that was edited.
		block_time = o = s.taboption('advanced', form.Value, 'block_time', _('Block time'),
			_('How long an address stays blocked after it was last seen on the list. At least twice the sync interval.'));
		o.placeholder = '24h';
		o.validate = duration_validator(60, 30 * 86400, '1m', '30d', 86400, function(section_id, n) {
			const interval = form_duration(sync_interval, section_id, 600);
			return (interval == null || n >= 2 * interval) ||
				_('Must be at least twice the sync interval');
		});

		sync_interval = o = s.taboption('advanced', form.Value, 'sync_interval', _('Sync interval'));
		o.placeholder = '10m';
		o.validate = duration_validator(60, 86400, '1m', '1d', 600, function(section_id, n) {
			const time = form_duration(block_time, section_id, 86400);
			return (time == null || 2 * n <= time) ||
				_('Must be at most half of the block time');
		});

		o = s.taboption('advanced', form.Value, 'max_entries', _('Maximum entries'));
		o.datatype = 'range(1,500000)';
		o.placeholder = '20000';

		o = s.taboption('advanced', form.Flag, 'block_forward', _('Block forwarded traffic'),
			_('Also protects port-forwarded LAN services.'));
		o.default = o.enabled;

		o = s.taboption('advanced', form.Flag, 'verify_tls', _('Verify TLS certificate'));
		o.default = o.enabled;

		o = s.taboption('advanced', form.Value, 'timeout', _('HTTP timeout'), _('Seconds'));
		o.datatype = 'range(1,300)';
		o.placeholder = '30';

		o = s.taboption('advanced', form.DynamicList, 'whitelist', _('Whitelist'),
			_('Addresses and networks that are never blocked. Private ranges and the router\'s own addresses are whitelisted automatically.'));
		o.datatype = 'ipaddr';

		return m.render();
	}
});
