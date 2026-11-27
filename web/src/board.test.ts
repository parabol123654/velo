import { describe, expect, it } from 'vitest';
import { dropPosition } from './board';

const col = (...pairs: [string, number][]) => pairs.map(([identifier, position]) => ({ identifier, position }));

describe('dropPosition', () => {
  const abc = col(['A', 1000], ['B', 2000], ['C', 3000]);

  it('puts a card from another column just before the card it is dropped on', () => {
    expect(dropPosition(abc, 'X', 'B')).toBe(1500);
    expect(dropPosition(abc, 'X', 'A')).toBe(500);
  });

  it('appends a card dropped on the column itself', () => {
    expect(dropPosition(abc, 'X')).toBe(4000);
    expect(dropPosition(abc, 'A')).toBe(4000);
    expect(dropPosition([], 'X')).toBe(1000);
  });

  it('within a column, a card takes the place of the card it is dropped on', () => {
    // Moving down: after the target.
    expect(dropPosition(abc, 'A', 'B')).toBe(2500); // B, A, C
    expect(dropPosition(abc, 'A', 'C')).toBe(4000); // B, C, A
    expect(dropPosition(abc, 'B', 'C')).toBe(4000); // A, C, B
    // Moving up: before the target.
    expect(dropPosition(abc, 'C', 'B')).toBe(1500); // A, C, B
    expect(dropPosition(abc, 'C', 'A')).toBe(500); // C, A, B
    expect(dropPosition(abc, 'B', 'A')).toBe(500); // B, A, C
  });

  it('works on an unsorted column', () => {
    expect(dropPosition(col(['C', 3000], ['A', 1000], ['B', 2000]), 'X', 'C')).toBe(2500);
  });

  it('appends when the target card is not in the column', () => {
    expect(dropPosition(abc, 'X', 'Z')).toBe(4000);
  });
});
