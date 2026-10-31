import { describe, expect, it } from 'vitest';
import { sourceCounts, view, type Tool } from './tools';

const tools: Tool[] = [
  { name: 'ls', path: '/usr/bin/ls', manager: 'apt', package: 'coreutils', summary: 'list directory contents' },
  { name: 'rg', path: '/h/.cargo/bin/rg', manager: 'cargo', package: 'ripgrep' },
  { name: 'myscript', path: '/h/.local/bin/myscript' },
  { name: 'cat', path: '/usr/bin/cat', manager: 'apt', package: 'coreutils' },
  { name: 'Zed', path: '/usr/bin/Zed', manager: 'apt', package: 'zed' },
];
const names = (l: Tool[]) => l.map((x) => x.name);

describe('tools view', () => {
  it('filters by source and words', () => {
    expect(names(view(tools, 'apt', '', 'name', false))).toEqual(['cat', 'ls', 'Zed']);
    expect(names(view(tools, 'other', '', 'name', false))).toEqual(['rg']);
    expect(names(view(tools, 'none', '', 'name', false))).toEqual(['myscript']);
    expect(names(view(tools, 'all', 'coreutils list', 'name', false))).toEqual(['ls']);
  });
  it('sorts by package, unowned last', () => {
    expect(names(view(tools, 'all', '', 'package', false))).toEqual(['cat', 'ls', 'rg', 'Zed', 'myscript']);
    expect(names(view(tools, 'all', '', 'name', true))[0]).toBe('Zed');
  });
  it('counts sources', () => {
    expect(sourceCounts(tools)).toEqual({ all: 5, apt: 3, other: 1, none: 1 });
  });
});
