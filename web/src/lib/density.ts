// Comfortable or compact spacing: a choice of this browser, not of the
// machine, so it lives in localStorage and works without it.

export type Density = 'comfortable' | 'compact';

const KEY = 'hearth.density';

export function loadDensity(storage: Pick<Storage, 'getItem'> | undefined = globalThis.localStorage): Density {
  try {
    return storage?.getItem(KEY) === 'compact' ? 'compact' : 'comfortable';
  } catch {
    return 'comfortable'; // storage can be blocked
  }
}

export function applyDensity(d: Density, root: HTMLElement = document.documentElement): void {
  if (d === 'compact') root.dataset['density'] = 'compact';
  else delete root.dataset['density'];
}

export function saveDensity(d: Density, storage: Pick<Storage, 'setItem'> | undefined = globalThis.localStorage): void {
  try {
    storage?.setItem(KEY, d);
  } catch {
    // It still applies to this page.
  }
}
