import { describe, expect, it } from 'vitest';
import { limitChoices } from './sleep';

describe('sleep', () => {
  it('offers the usual limits and keeps the current one', () => {
    expect(limitChoices()).toEqual([60, 70, 80, 90, 100]);
    expect(limitChoices(80)).toEqual([60, 70, 80, 90, 100]);
    expect(limitChoices(75)).toEqual([60, 70, 75, 80, 90, 100]);
  });
});
