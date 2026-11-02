import { lang } from '../lib/i18n';

// These screens are fixtures, so their words stay here and out of the
// production message files. Tasks 9.9 and 10.9 move them there.
export const tr = (en: string, ar: string): string => (lang() === 'ar' ? ar : en);

/** A value inside an Arabic sentence keeps its own order, as t() does it. */
export const iso = (s: string): string => `\u2068${s}\u2069`;
