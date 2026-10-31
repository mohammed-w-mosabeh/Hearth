import en from './en.json';

// Another locale is another JSON file with the same keys. Only the chosen one
// is loaded, so English pays nothing for Arabic.
export type MessageKey = keyof typeof en;
export type Lang = 'en' | 'ar';

const KEY = 'hearth.lang';

let messages: Record<MessageKey, string> = en;
let current: Lang = 'en';

/** Returns the message for key with {name} placeholders filled in. */
export function t(key: MessageKey, params: Record<string, string | number> = {}): string {
  const msg = messages[key] ?? key;
  return msg.replace(/\{(\w+)\}/g, (whole, name: string) =>
    name in params ? isolate(String(params[name])) : whole,
  );
}

/** A value inside a sentence is its own run: "/home" keeps its slash in Arabic,
 *  and bidi controls in a hostile name stay inside it. */
const isolate = (s: string): string => (current === 'ar' ? `\u2068${s}\u2069` : s);

export const lang = (): Lang => current;
export const isRTL = (): boolean => current === 'ar';

/** A choice of this browser, like density; it works without storage. */
export function loadLang(storage: Pick<Storage, 'getItem'> | undefined = globalThis.localStorage): Lang {
  try {
    return storage?.getItem(KEY) === 'ar' ? 'ar' : 'en';
  } catch {
    return 'en'; // storage can be blocked
  }
}

export function saveLang(l: Lang, storage: Pick<Storage, 'setItem'> | undefined = globalThis.localStorage): void {
  try {
    storage?.setItem(KEY, l);
  } catch {
    // It lasts until the page closes.
  }
}

/** Loads a language and sets the page's direction. Messages are read as pages
 *  are built, so a change takes a reload (see Settings). */
export async function applyLang(l: Lang, root: HTMLElement = document.documentElement): Promise<void> {
  messages = l === 'ar' ? (await import('./ar.json')).default : en;
  current = l;
  root.lang = l;
  root.dir = l === 'ar' ? 'rtl' : 'ltr';
}

/** The locale for dates and numbers. Arabic keeps Western digits, as sizes,
 *  addresses and versions have, and the Gregorian calendar of the system's
 *  own logs. In English this is what the caller has always used. */
export function locale(english?: string): string | undefined {
  return current === 'ar' ? 'ar-u-nu-latn-ca-gregory' : english;
}

/** Keeps a left-to-right run (a size, a version, "a → b") whole inside
 *  right-to-left text. Unlike <bdi> it survives t() and attributes. */
export function ltr(s: string): string {
  return current === 'ar' ? `⁦${s}⁩` : s;
}

/** Where a menu's left edge goes so that it lines up with the button's
 *  outer edge, inside the window. */
export function menuLeft(button: DOMRect, width: number): number {
  const wanted = isRTL() ? button.left : button.right - width;
  return Math.min(Math.max(4, wanted), innerWidth - width - 4);
}
