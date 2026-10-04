#!/usr/bin/env python3
"""Checks the translation files of luci-app-crowdblock against the views.

Every text passed to _() in htdocs/.../view/crowdblock/*.js must be in each
htdocs/.../crowdblock/i18n/<lang>.json, with the same %s / %d placeholders.

  ./check-i18n.py           report problems (exit code 1 if any)
  ./check-i18n.py --update  also rewrite en.json and add missing texts
                            (untranslated, in English) to the other files
"""
import glob
import json
import os
import re
import sys

ROOT = os.path.join(os.path.dirname(os.path.abspath(__file__)), 'htdocs/luci-static/resources')
VIEWS = os.path.join(ROOT, 'view/crowdblock/*.js')
I18N = os.path.join(ROOT, 'crowdblock/i18n')


def source_texts():
    texts = set()
    for path in sorted(glob.glob(VIEWS)):
        with open(path, encoding='utf-8') as f:
            for m in re.findall(r"_\('((?:[^'\\]|\\.)*)'\)", f.read()):
                texts.add(m.encode('utf-8').decode('unicode_escape').encode('latin-1').decode('utf-8'))
    return sorted(texts, key=str.lower)


def write(path, catalog):
    with open(path, 'w', encoding='utf-8') as f:
        json.dump(catalog, f, ensure_ascii=False, indent='\t')
        f.write('\n')


def main():
    update = '--update' in sys.argv
    texts = source_texts()
    problems = 0

    if update:
        write(os.path.join(I18N, 'en.json'), {t: t for t in texts})

    for path in sorted(glob.glob(os.path.join(I18N, '*.json'))):
        lang = os.path.basename(path)[:-5]
        with open(path, encoding='utf-8') as f:
            catalog = json.load(f)

        missing = [t for t in texts if t not in catalog]
        unused = [t for t in catalog if t not in texts]
        placeholders = [t for t in texts if t in catalog
                        and re.findall(r'%[sd]', t) != re.findall(r'%[sd]', catalog[t])]

        for t in missing:
            print(f'{lang}: missing: {t!r}')
        for t in unused:
            print(f'{lang}: unused: {t!r}')
        for t in placeholders:
            print(f'{lang}: placeholders differ: {t!r} -> {catalog[t]!r}')
        problems += len(missing) + len(unused) + len(placeholders)

        if update and (missing or unused):
            write(path, {t: catalog.get(t, t) for t in texts})

    print(f'{len(texts)} texts, {problems} problem(s)')
    return 1 if problems else 0


if __name__ == '__main__':
    sys.exit(main())
