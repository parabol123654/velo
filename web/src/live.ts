import type { Issue } from './api';

export interface LiveEvent {
  type: string;
  payload: unknown;
}

export interface LiveResult {
  issues: Issue[];
  // Project/cycle counters (sidebar, cycles view) may have changed: refetch the small metadata, not the issue list.
  refreshMeta: boolean;
  // Unknown or structural change (project, state, cycle, user): reload the workspace.
  reload: boolean;
  // Identifiers of parent issues on the board whose sub-issue counters should be refetched.
  refetch: string[];
}

// applyEvent folds one SSE event into the board's issue list for the active project,
// so a change touches only the affected card instead of reloading the workspace.
export function applyEvent(issues: Issue[], ev: LiveEvent, projectId: string): LiveResult {
  switch (ev.type) {
    case 'issue.created':
    case 'issue.updated': {
      const iss = ev.payload as Issue;
      const idx = issues.findIndex((it) => it.id === iss.id);
      let next = issues;
      if (iss.project_id === projectId) {
        next = idx >= 0 ? issues.map((it, i) => (i === idx ? iss : it)) : [...issues, iss];
      } else if (idx >= 0) {
        next = issues.filter((it) => it.id !== iss.id); // moved out of this project
      }
      // Both the old and the new parent's counters may change.
      return { issues: next, refreshMeta: true, reload: false, refetch: parentsOf(issues, [issues[idx]?.parent_id, iss.parent_id]) };
    }
    case 'issue.deleted': {
      const { id } = ev.payload as { id: string };
      const gone = issues.find((it) => it.id === id);
      // Sub-issues survive with parent_id cleared (ON DELETE SET NULL).
      const next = issues
        .filter((it) => it.id !== id)
        .map((it) => (it.parent_id === id ? { ...it, parent_id: undefined, parent_identifier: undefined } : it));
      return { issues: next, refreshMeta: true, reload: false, refetch: parentsOf(next, [gone?.parent_id]) };
    }
    case 'comment.created': {
      const { issue_id } = ev.payload as { issue_id: string };
      return {
        issues: issues.map((it) => (it.id === issue_id ? { ...it, comment_count: it.comment_count + 1 } : it)),
        refreshMeta: false,
        reload: false,
        refetch: [],
      };
    }
    case 'comment.deleted': {
      const { issue_id } = ev.payload as { issue_id: string };
      return {
        issues: issues.map((it) =>
          it.id === issue_id ? { ...it, comment_count: Math.max(0, it.comment_count - 1) } : it
        ),
        refreshMeta: false,
        reload: false,
        refetch: [],
      };
    }
    case 'comment.updated':
      return { issues, refreshMeta: false, reload: false, refetch: [] }; // cards show no comment text
    default:
      return { issues, refreshMeta: false, reload: true, refetch: [] };
  }
}

function parentsOf(issues: Issue[], ids: (string | undefined)[]): string[] {
  const wanted = new Set(ids.filter(Boolean));
  return issues.filter((it) => wanted.has(it.id)).map((it) => it.identifier);
}

// touchesIssue reports whether an event changes what the issue drawer shows
// for issueId: the issue itself, one of its sub-issues, or its comments.
export function touchesIssue(ev: LiveEvent, issueId: string): boolean {
  switch (ev.type) {
    case 'issue.created':
    case 'issue.updated': {
      const iss = ev.payload as Issue;
      return iss.id === issueId || iss.parent_id === issueId;
    }
    case 'issue.deleted':
    case 'comment.created':
    case 'comment.updated':
    case 'comment.deleted': {
      const p = ev.payload as { id: string; issue_id?: string };
      return ev.type === 'issue.deleted' ? p.id === issueId : p.issue_id === issueId;
    }
    default:
      return false;
  }
}
