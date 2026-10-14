'use strict';
'require baseclass';
'require request';
'require uci';

// Translations of the crowdblock pages, one file per language in
// crowdblock/i18n/<code>.json: { "English text": "translation", ... }.
// The language is the `language` option of /etc/config/crowdblock, or the
// language of LuCI when it is empty ("auto"). Missing texts stay in English.
//
// Usage in a view:
//	'require crowdblock.i18n as i18n';
//	const _ = i18n.translate;        // shadows LuCI's global _()
//	load: function() { return Promise.all([ i18n.load(), ... ]); }

const LANGUAGES = { en: 'English', pl: 'Polski', de: 'Deutsch' };

let catalog = {};

// "pl", "de-DE", "pt_BR" -> code of a supported language, or null
function supported(lang) {
	const code = String(lang || '').toLowerCase().split(/[-_]/)[0];
	return LANGUAGES.hasOwnProperty(code) ? code : null;
}

function translate(text) {
	return catalog.hasOwnProperty(text) ? catalog[text] : text;
}

return baseclass.extend({
	languages: LANGUAGES,

	translate: translate,

	// Language of the LuCI interface (set by the theme on <html lang>).
	luciLanguage: function() {
		return supported(document.documentElement.lang) || supported(navigator.language) || 'en';
	},

	// Resolves to the language code in use.
	load: function() {
		return L.resolveDefault(uci.load('crowdblock'), null).then(L.bind(function() {
			const lang = supported(uci.get('crowdblock', 'main', 'language')) || this.luciLanguage();

			return request.get(L.resource('crowdblock/i18n/%s.json'.format(lang))).then(function(res) {
				catalog = res.ok ? res.json() : {};
				return lang;
			}).catch(function() {
				catalog = {};
				return lang;
			});
		}, this));
	}
});
