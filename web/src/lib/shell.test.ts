import { describe, expect, it } from 'vitest';
import { openContainer, openDir, openTool, parseOpen, shellURL, tabTitle } from './shell';

describe('shellURL', () => {
  it('builds the WebSocket URL with size and folder', () => {
    expect(shellURL('http://127.0.0.1:7381/#/terminal', 80, 24)).toBe('ws://127.0.0.1:7381/api/v1/terminal?cols=80&rows=24');
    expect(shellURL('http://127.0.0.1:7381/', 100, 30, '/home/me/My Files')).toBe('ws://127.0.0.1:7381/api/v1/terminal?cols=100&rows=30&dir=%2Fhome%2Fme%2FMy+Files');
  });
});

describe('parseOpen', () => {
  it('reads folder, tool and container requests', () => {
    expect(parseOpen(openDir('/srv/a:b'))).toEqual({ kind: 'shell', dir: '/srv/a:b', prefill: '' });
    expect(parseOpen(openTool('g++'))).toEqual({ kind: 'shell', dir: '', prefill: 'g++ ' });
    expect(parseOpen(openContainer('0123456789ab', 'web:1'))).toEqual({ kind: 'container', id: '0123456789ab', name: 'web:1' });
    expect(parseOpen('container:0123456789abcdef')).toEqual({ kind: 'container', id: '0123456789abcdef', name: '0123456789ab' });
  });
  it('refuses anything else', () => {
    for (const q of ['', 'cups', 'dir:relative', 'tool:rm -rf /', 'tool:ls;id', 'tool:-h', 'tool:a\nb', 'container:ZZZ', 'container:abc']) {
      expect(parseOpen(q)).toBeNull();
    }
  });
});

describe('tabTitle', () => {
  it('names tabs', () => {
    expect(tabTitle({ kind: 'shell', dir: '', prefill: '' }, 1)).toBe('~');
    expect(tabTitle({ kind: 'shell', dir: '', prefill: '' }, 3)).toBe('~ (3)');
    expect(tabTitle({ kind: 'shell', dir: '/home/me/src', prefill: '' }, 1)).toBe('src');
    expect(tabTitle({ kind: 'shell', dir: '/', prefill: '' }, 1)).toBe('/');
    expect(tabTitle({ kind: 'shell', dir: '', prefill: 'htop ' }, 1)).toBe('htop');
    expect(tabTitle({ kind: 'container', id: 'x', name: 'db' }, 1)).toBe('db');
  });
});
