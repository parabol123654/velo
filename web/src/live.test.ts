import { describe, expect, it } from 'vitest';
import type { Issue } from './api';
import { applyEvent, touchesIssue } from './live';

const P = 'proj1';

function issue(id: string, extra: Partial<Issue> = {}): Issue {
  return {
    id,
    project_id: P,
    project_key: 'DEV',
    number: 1,
    identifier: `DEV-${id}`,
    title: `Issue ${id}`,
    description: '',
    state_id: 's1',
    state_name: 'Todo',
    state_category: 'unstarted',
    state_color: '#000',
    priority: 0,
    creator_id: 'u1',
    creator_name: 'U',
    position: 1000,
    labels: [],
    sub_issue_count: 0,
    sub_issue_done_count: 0,
    comment_count: 0,
    created_at: '',
    updated_at: '',
    ...extra,
  };
}

describe('applyEvent', () => {
  it('appends a created issue in the active project', () => {
    const r = applyEvent([issue('1')], { type: 'issue.created', payload: issue('2') }, P);
    expect(r.issues.map((i) => i.id)).toEqual(['1', '2']);
    expect(r.reload).toBe(false);
    expect(r.refreshMeta).toBe(true);
  });

  it('is idempotent: a repeated create replaces instead of duplicating', () => {
    const r = applyEvent([issue('1')], { type: 'issue.created', payload: issue('1', { title: 'new' }) }, P);
    expect(r.issues).toHaveLength(1);
    expect(r.issues[0].title).toBe('new');
  });

  it('replaces only the updated card and keeps the others by reference', () => {
    const a = issue('1');
    const b = issue('2');
    const r = applyEvent([a, b], { type: 'issue.updated', payload: issue('2', { state_id: 's2' }) }, P);
    expect(r.issues[0]).toBe(a);
    expect(r.issues[1].state_id).toBe('s2');
  });

  it('ignores issues from another project but still refreshes counters', () => {
    const list = [issue('1')];
    const r = applyEvent(list, { type: 'issue.created', payload: issue('9', { project_id: 'other' }) }, P);
    expect(r.issues).toBe(list);
    expect(r.refreshMeta).toBe(true);
  });

  it('drops an issue that moved to another project', () => {
    const r = applyEvent([issue('1')], { type: 'issue.updated', payload: issue('1', { project_id: 'other' }) }, P);
    expect(r.issues).toEqual([]);
  });

  it('asks to refetch old and new parents when a sub-issue changes parent', () => {
    const list = [issue('p1'), issue('p2'), issue('c', { parent_id: 'p1' })];
    const r = applyEvent(list, { type: 'issue.updated', payload: issue('c', { parent_id: 'p2' }) }, P);
    expect(r.refetch.sort()).toEqual(['DEV-p1', 'DEV-p2']);
  });

  it('removes a deleted issue and detaches its sub-issues', () => {
    const list = [issue('p', { parent_id: 'gp' }), issue('gp'), issue('c', { parent_id: 'p', parent_identifier: 'DEV-p' })];
    const r = applyEvent(list, { type: 'issue.deleted', payload: { id: 'p', identifier: 'DEV-p' } }, P);
    expect(r.issues.map((i) => i.id)).toEqual(['gp', 'c']);
    expect(r.issues[1].parent_id).toBeUndefined();
    expect(r.refetch).toEqual(['DEV-gp']);
  });

  it('bumps the comment count on comment.created', () => {
    const r = applyEvent([issue('1', { comment_count: 2 })], { type: 'comment.created', payload: { issue_id: '1' } }, P);
    expect(r.issues[0].comment_count).toBe(3);
    expect(r.refreshMeta).toBe(false);
  });

  it('decrements the comment count on comment.deleted, never below zero', () => {
    const ev = { type: 'comment.deleted', payload: { id: 'c', issue_id: '1' } };
    expect(applyEvent([issue('1', { comment_count: 1 })], ev, P).issues[0].comment_count).toBe(0);
    expect(applyEvent([issue('1')], ev, P).issues[0].comment_count).toBe(0);
  });

  it('ignores comment.updated', () => {
    const list = [issue('1')];
    const r = applyEvent(list, { type: 'comment.updated', payload: { issue_id: '1' } }, P);
    expect(r.issues).toBe(list);
    expect(r.reload).toBe(false);
  });

  it('falls back to a reload for structural events', () => {
    for (const type of ['project.created', 'project.deleted', 'state.updated', 'cycle.deleted', 'user.updated']) {
      expect(applyEvent([], { type, payload: {} }, P).reload).toBe(true);
    }
  });
});

describe('touchesIssue', () => {
  it('matches changes to the issue, its sub-issues and its comments', () => {
    expect(touchesIssue({ type: 'issue.updated', payload: issue('1') }, '1')).toBe(true);
    expect(touchesIssue({ type: 'issue.created', payload: issue('2', { parent_id: '1' }) }, '1')).toBe(true);
    expect(touchesIssue({ type: 'issue.deleted', payload: { id: '1' } }, '1')).toBe(true);
    expect(touchesIssue({ type: 'comment.deleted', payload: { id: 'c', issue_id: '1' } }, '1')).toBe(true);
  });

  it('ignores unrelated events', () => {
    expect(touchesIssue({ type: 'issue.updated', payload: issue('2') }, '1')).toBe(false);
    expect(touchesIssue({ type: 'comment.created', payload: { id: 'c', issue_id: '2' } }, '1')).toBe(false);
    expect(touchesIssue({ type: 'project.updated', payload: {} }, '1')).toBe(false);
  });
});
