import { describe, expect, it } from 'vitest';
import { builtinName, groupTiles, hostOf, moved, suggestionForm, urlProblem, type Tile } from './homepage';

const tile = (id: number, group: string, position: number): Tile => ({
  id, name: `t${id}`, url: 'http://x/', description: '', icon: '', group, container: '', health_check: false, position,
});

describe('homepage', () => {
  it('checks URLs like the server', () => {
    expect(urlProblem('http://192.168.1.2:8080/')).toBeNull();
    expect(urlProblem('https://nas.local')).toBeNull();
    expect(urlProblem('')).toBe('empty');
    expect(urlProblem('javascript:alert(1)')).toBe('scheme');
    expect(urlProblem('nas.local')).toBe('scheme');
    expect(urlProblem('http://a b/')).toBe('scheme');
    expect(urlProblem('http://me:pw@nas/')).toBe('credentials');
    expect(hostOf('http://nas.local:5000/x')).toBe('nas.local:5000');
  });
  it('knows built-in icons only', () => {
    expect(builtinName('builtin:globe')).toBe('globe');
    expect(builtinName('builtin:nope')).toBeNull();
    expect(builtinName('upload:abc')).toBeNull();
  });
  it('groups in page order', () => {
    const g = groupTiles([tile(1, 'Media', 3), tile(2, '', 1), tile(3, 'Media', 2), tile(4, 'Home', 4)]);
    expect(g.map((x) => [x.group, x.tiles.map((t) => t.id)])).toEqual([['', [2]], ['Media', [3, 1]], ['Home', [4]]]);
  });
  it('moves tiles', () => {
    const ts = [tile(1, '', 1), tile(2, '', 2), tile(3, '', 3)];
    expect(moved(ts, 2, -1)).toEqual([2, 1, 3]);
    expect(moved(ts, 3, 1)).toEqual([1, 2, 3]);
    expect(moved(ts, 9, 1)).toEqual([1, 2, 3]);
  });
  it('pre-fills the form from a suggestion', () => {
    expect(suggestionForm({ name: 'filestore', url: 'http://127.0.0.1:9000/', source: 'container', container: 'filestore', title: 'Files Console' })).toEqual({
      name: 'filestore', url: 'http://127.0.0.1:9000/', description: 'Files Console', icon: '', group: '', container: 'filestore', health_check: true,
    });
    const f = suggestionForm({ name: 'Jellyfin', url: 'http://127.0.0.1:8096/', source: 'listener', process: 'jellyfin', title: 'Jellyfin' });
    expect(f.description).toBe('');
    expect(f.container).toBe('');
  });
});
