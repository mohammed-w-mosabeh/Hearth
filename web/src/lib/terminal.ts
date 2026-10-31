// xterm.js, loaded on demand (import('./terminal')) so it stays out of
// the main bundle (CLAUDE.md budget).

import { FitAddon } from '@xterm/addon-fit';
import { Terminal } from '@xterm/xterm';
import '@xterm/xterm/css/xterm.css';

export { FitAddon, Terminal };

/**
 * xterm.js writes its theme and cell sizes into <style> elements, which
 * the CSP (SECURITY S4: style-src 'self', no 'unsafe-inline') blocks.
 * While a terminal is open, a <style> asked of doc is an inert <template>
 * (so the CSP has nothing to block) whose text is applied as a
 * constructed stylesheet, which the CSP allows (CSSOM). Returns the undo.
 */
export function cspStyles(doc: Document): () => void {
  const sheets: CSSStyleSheet[] = [];
  const create = doc.createElement;
  const adopt = (el: HTMLElement): void => {
    const sheet = new CSSStyleSheet();
    sheets.push(sheet);
    doc.adoptedStyleSheets = [...doc.adoptedStyleSheets, sheet];
    let text = '';
    Object.defineProperty(el, 'textContent', {
      configurable: true,
      get: () => text,
      set: (v: string | null) => {
        text = v ?? '';
        try {
          sheet.replaceSync(text);
        } catch {
          // not CSS; ignore like the browser would
        }
      },
    });
  };
  doc.createElement = function (this: Document, tag: string, options?: ElementCreationOptions): HTMLElement {
    if (tag.toLowerCase() !== 'style') return create.call(this, tag, options);
    const el = create.call(this, 'template');
    adopt(el);
    return el;
  } as typeof doc.createElement;
  return () => {
    doc.createElement = create;
    doc.adoptedStyleSheets = doc.adoptedStyleSheets.filter((s) => !sheets.includes(s));
  };
}
