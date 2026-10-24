import { describe, expect, it } from 'vitest';
import { filterApps, initials, rank, type App } from './apps';

const app = (id: string, name: string, source: App['source'], categories: string[], extra: Partial<App> = {}): App => ({
  id, name, source, categories, path: `/x/${id}.desktop`, has_icon: false, ...extra,
});

const apps = [
  app('org.gnome.TextEditor', 'Text Editor', 'system', ['Utility', 'TextEditor'], { keywords: ['plain'] }),
  app('firefox', 'Firefox', 'user', ['Network', 'WebBrowser'], { generic_name: 'Web Browser' }),
  app('app.zen_browser.zen', 'Zen', 'flatpak', ['Network']),
  app('appimage.obsidian', 'Obsidian', 'appimage', []),
];

describe('apps filter', () => {
  it('filters by source, category and words', () => {
    expect(filterApps(apps, 'flatpak', '', '').map((a) => a.id)).toEqual(['app.zen_browser.zen']);
    expect(filterApps(apps, 'all', 'Network', '').map((a) => a.id)).toEqual(['firefox', 'app.zen_browser.zen']);
    expect(filterApps(apps, 'all', '', 'web browser').map((a) => a.id)).toEqual(['firefox']);
    expect(filterApps(apps, 'all', '', 'plain').map((a) => a.id)).toEqual(['org.gnome.TextEditor']);
  });
  it('ranks name prefix first', () => {
    const list = [app('a', 'Notes for Zen', 'system', []), app('b', 'Zen', 'system', [])];
    expect(rank(list, 'zen').map((a) => a.id)).toEqual(['b', 'a']);
  });
  it('makes initials', () => {
    expect(initials('Among Us')).toBe('AU');
    expect(initials('obsidian')).toBe('OB');
  });
});
