// Board ordering: positions are numbers, and a move changes only the moved
// card's position (SPEC.md, "Ordering").

export interface Positioned {
  identifier: string;
  position: number;
}

// dropPosition returns the new position of the card dragged. Dropped on a
// column, it goes last. Dropped on a card (targetIdent), it takes that card's
// place, as moving an item in a list does: within the same column a card
// moved down lands just after the target, a card moved up (or one from
// another column) just before it.
export function dropPosition(column: Positioned[], dragged: string, targetIdent?: string): number {
  const sorted = [...column].sort((a, b) => a.position - b.position);
  const from = sorted.findIndex((c) => c.identifier === dragged);
  const others = sorted.filter((c) => c.identifier !== dragged);
  if (others.length === 0) return 1000;
  let i = targetIdent === undefined ? -1 : others.findIndex((c) => c.identifier === targetIdent);
  if (i < 0) return others[others.length - 1].position + 1000;
  if (from >= 0 && from <= i) i++; // moving down: after the target
  if (i >= others.length) return others[others.length - 1].position + 1000;
  if (i === 0) return others[0].position / 2;
  return (others[i - 1].position + others[i].position) / 2;
}
