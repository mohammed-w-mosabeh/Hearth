import { describe, expect, it } from 'vitest';
import { appEntries, pageEntries, score, search, unitEntries, type Entry } from './palette';

const pages = pageEntries([
  { id: 'home', title: 'Home' },
  { id: 'services', title: 'Services' },
  { id: 'resources', title: 'Resources' },
]);
const apps = appEntries([
  { id: 'org.gnome.TextEditor', name: 'Text Editor', generic_name: 'Editor', categories: [], source: 'system', path: '', has_icon: false },
  { id: 'firefox-esr', name: 'Firefox ESR', comment: 'Browse the web', categories: [], source: 'system', path: '', has_icon: false },
  { id: 'code', name: 'Visual Studio Code', keywords: ['vscode'], categories: [], source: 'system', path: '', has_icon: false },
]);
const units = unitEntries([
  { name: 'cups.service', scope: 'system', load: 'loaded', active: 'active', sub: 'running', description: 'CUPS Scheduler' },
  { name: 'dev-sda.device', scope: 'system', load: 'loaded', active: 'active', sub: 'plugged' },
  { name: 'session-2.scope', scope: 'system', load: 'loaded', active: 'active', sub: 'running' },
  { name: 'syncnotes.service', scope: 'user', load: 'loaded', active: 'active', sub: 'running' },
  { name: 'cups.path', scope: 'system', load: 'loaded', active: 'active', sub: 'waiting', description: 'CUPS Scheduler' },
  { name: 'pipewire.socket', scope: 'user', load: 'loaded', active: 'active', sub: 'listening' },
]);
const all: Entry[] = [...pages, ...apps, ...units];
const titles = (list: Entry[]) => list.map((e) => e.title);

describe('palette', () => {
  it('builds entries', () => {
    expect(units.map((u) => u.id)).toEqual(['system/cups.service', 'user/syncnotes.service', 'system/cups.path', 'user/pipewire.socket']);
    expect(apps[0]?.detail).toBe('Editor');
    expect(apps[1]?.detail).toBe('Browse the web');
  });
  it('scores prefix, word start, substring, detail, then in-order letters', () => {
    const e = apps[0] as Entry;
    expect(score(e, 'text')).toBe(0);
    expect(score(e, 'edit')).toBe(1);
    expect(score(e, 'xt ed')).toBe(2);
    expect(score(e, 'gnome')).toBe(3);
    expect(score(e, 'txe')).toBe(4);
    expect(score(e, 'zzz')).toBeNull();
    expect(score(e, 'xtr')).toBeNull(); // in order, but 'x' starts no word
    expect(score(e, '')).toBe(0);
  });
  it('ranks and caps results', () => {
    // Prefix, word starts (".service"), description ("Browse"), then letters in
    // order from a word start ("Studio Code", "socket"); not "Resources".
    expect(titles(search(all, 'se'))).toEqual(['Services', 'cups.service', 'syncnotes.service', 'Firefox ESR', 'Visual Studio Code', 'pipewire.socket']);
    expect(titles(search(all, 'cups'))).toEqual(['cups.service', 'cups.path']);
    expect(titles(search(all, 'reso'))).toEqual(['Resources']);
    expect(titles(search(all, 'vscode'))).toEqual(['Visual Studio Code']);
    expect(titles(search(all, 'web'))).toEqual(['Firefox ESR']);
    expect(search(all, '', 3).length).toBe(3);
    expect(search(all, '')[0]?.kind).toBe('page');
  });
});
