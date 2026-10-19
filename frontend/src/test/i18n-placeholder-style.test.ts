import { readFileSync, readdirSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';

// i18next is configured in this panel with prefix '{' and suffix '}'. A string
// written with double braces therefore never interpolates: the customer or the
// operator reads "{{remark}}" where a name belongs. It is invisible in review,
// because the English file looks perfectly sensible.
//
// Two strings were found like this on a live panel. The five below are not
// bugs: they list the tokens an operator types into a field by hand, and are
// meant to be read literally. Anything new has to be one or the other, and
// saying which is the point of this test.
const LITERAL_BY_DESIGN = new Set([
  'pages.settings.subShowIdentityOnAllLinksDesc',
  'pages.settings.subTitleDesc',
  'pages.settings.subSupportUrlDesc',
  'pages.settings.subProfileUrlDesc',
  'pages.settings.subAnnounceDesc',
]);

const DIR = join(process.cwd(), '..', 'internal', 'web', 'translation');

function doubleBraced(obj: unknown, path = ''): string[] {
  if (typeof obj === 'string') {
    return /\{\{[A-Za-z_]+\}\}/.test(obj) ? [path] : [];
  }
  if (!obj || typeof obj !== 'object') return [];
  return Object.entries(obj as Record<string, unknown>).flatMap(([k, v]) =>
    doubleBraced(v, path ? `${path}.${k}` : k),
  );
}

describe('i18n placeholder style', () => {
  it('uses the brace style the panel is configured for', () => {
    const offenders: string[] = [];
    for (const file of readdirSync(DIR)) {
      if (!file.endsWith('.json')) continue;
      const data = JSON.parse(readFileSync(join(DIR, file), 'utf8'));
      for (const key of doubleBraced(data)) {
        if (!LITERAL_BY_DESIGN.has(key)) offenders.push(`${file}: ${key}`);
      }
    }
    expect(
      offenders,
      `these strings use {{double}} braces, which this panel never interpolates, so the\n` +
        `placeholder is shown to the reader verbatim. Use {single} braces, or add the key\n` +
        `to LITERAL_BY_DESIGN if the braces are meant to be read:\n  ${offenders.join('\n  ')}`,
    ).toEqual([]);
  });
});
