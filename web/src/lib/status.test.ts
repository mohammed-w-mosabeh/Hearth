import { describe, expect, it } from 'vitest';
import { statusShape } from './status';

describe('status shapes', () => {
  it('gives each kind its own shape', () => {
    const shapes = Object.values(statusShape);
    expect(shapes).toHaveLength(4);
    expect(new Set(shapes).size).toBe(4);
  });
});
