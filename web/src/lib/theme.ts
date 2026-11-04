// Dark, light or the system's choice: a choice of this browser, like density.
// data-theme always holds the resolved theme, so tokens.css writes each once.

export type Theme = 'system' | 'dark' | 'light';
export const THEME_EVENT = 'hearth-theme';

const KEY = 'hearth.theme';

export function loadTheme(storage: Pick<Storage, 'getItem'> | undefined = globalThis.localStorage): Theme {
  try {
    const v = storage?.getItem(KEY);
    return v === 'system' || v === 'light' ? v : 'dark'; // dark is the primary theme
  } catch {
    return 'dark'; // storage can be blocked
  }
}

export function saveTheme(t: Theme, storage: Pick<Storage, 'setItem'> | undefined = globalThis.localStorage): void {
  try {
    storage?.setItem(KEY, t);
  } catch {
    // It still applies to this page.
  }
}

export const resolveTheme = (t: Theme, systemDark: boolean): 'dark' | 'light' => (t === 'system' ? (systemDark ? 'dark' : 'light') : t);

const scheme = () => matchMedia('(prefers-color-scheme: dark)');
let chosen: Theme = 'dark';

function paint(): void {
  document.documentElement.dataset['theme'] = resolveTheme(chosen, scheme().matches);
  window.dispatchEvent(new Event(THEME_EVENT)); // charts draw with these colors
}

/** Applies t, and follows the system while t is "system". */
export function applyTheme(t: Theme): void {
  chosen = t;
  paint();
}

export function followSystemTheme(): void {
  scheme().addEventListener('change', () => {
    if (chosen === 'system') paint();
  });
}
