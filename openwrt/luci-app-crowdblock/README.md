# luci-app-crowdblock

LuCI pages for the `crowdblock` package: **Services → Crowdblock**.

- **Settings** – server, API key, thresholds, whitelist, page language.
- **Blocked addresses** – addresses from the last sync with country, number
  of reporters and expiry; search by address or country code; pause / resume
  single addresses; manual sync.

## Translations

Each language has its own file in
`htdocs/luci-static/resources/crowdblock/i18n/<code>.json`, mapping the
English text to the translation:

```json
{
	"Sync now": "Synchronizuj teraz"
}
```

Available: `en`, `pl`, `de`. The language is chosen in **Settings → Language**
(UCI `crowdblock.main.language`); the default (empty) follows the language of
LuCI. Texts missing from a file are shown in English.

The views use `_()` from `crowdblock/i18n.js` instead of LuCI's catalogs, so
a translation is just a JSON file – no `.po` / `.lmo` build step. The menu
entries are the exception: they come from LuCI and stay in English.

After adding or changing texts in the views, run:

```sh
./check-i18n.py            # lists missing / unused texts and placeholder mismatches
./check-i18n.py --update   # regenerates en.json, adds missing texts to the others
```

### Adding a language

1. Copy `en.json` to `<code>.json` (e.g. `fr.json`) and translate the values.
   Keep the `%s` / `%d` placeholders in the same order.
2. Add the language to `LANGUAGES` in `htdocs/luci-static/resources/crowdblock/i18n.js`.
3. `./check-i18n.py`
