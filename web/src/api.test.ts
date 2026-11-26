import { describe, expect, it } from 'vitest';
import { assignableUsers, tokenScopeLabel, User } from './api';

const u = (id: string, disabled = false): User => ({
  id,
  email: `${id}@x.io`,
  name: id,
  role: 'member',
  created_at: '',
  ...(disabled ? { disabled_at: '2026-01-01T00:00:00Z' } : {}),
});

describe('assignableUsers', () => {
  const team = [u('a'), u('b', true), u('c')];

  it('hides disabled users', () => {
    expect(assignableUsers(team).map((x) => x.id)).toEqual(['a', 'c']);
  });

  it('keeps the selected user even when disabled', () => {
    expect(assignableUsers(team, 'b').map((x) => x.id)).toEqual(['a', 'b', 'c']);
  });
});

describe('tokenScopeLabel', () => {
  it('names the access and the projects, or all projects', () => {
    expect(tokenScopeLabel({ access: 'read', project_keys: ['DEV', 'OPS'] })).toBe('read · DEV, OPS');
    expect(tokenScopeLabel({ access: 'write', project_keys: [] })).toBe('write · all projects');
  });
});
