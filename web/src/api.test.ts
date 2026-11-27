import { describe, expect, it } from 'vitest';
import { assignableUsers, describeActivity, tokenScopeLabel, User } from './api';

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

describe('describeActivity', () => {
  it('reads naturally for each kind of change', () => {
    expect(describeActivity({ action: 'created' })).toBe('created this issue');
    expect(describeActivity({ action: 'updated', field: 'checklist', new_value: 'checked: deploy' })).toBe('checked: deploy');
    expect(describeActivity({ action: 'updated', field: 'link', new_value: 'blocked_by DEV-1' })).toBe('linked: blocked by DEV-1');
    expect(describeActivity({ action: 'updated', field: 'link', old_value: 'DEV-1' })).toBe('removed the link to DEV-1');
    expect(describeActivity({ action: 'updated', field: 'link', old_value: 'blocked_by DEV-1' })).toBe('unlinked: blocked by DEV-1');
    expect(describeActivity({ action: 'updated', field: 'description' })).toBe('edited the description');
    expect(describeActivity({ action: 'updated', field: 'labels', new_value: 'bug, feature' })).toBe(
      'changed labels from "none" to "bug, feature"'
    );
    expect(describeActivity({ action: 'updated', field: 'priority', old_value: 'Low', new_value: 'High' })).toBe(
      'changed priority from "Low" to "High"'
    );
  });
});
