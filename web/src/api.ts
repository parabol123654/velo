export interface User {
  id: string;
  email: string;
  name: string;
  role: 'admin' | 'member';
  disabled_at?: string;
  created_at: string;
}

export interface Project {
  id: string;
  key: string;
  name: string;
  description: string;
  issue_seq: number;
  issue_count: number;
  open_issue_count: number;
  created_at: string;
}

export interface WorkflowState {
  id: string;
  project_id: string;
  name: string;
  category: 'backlog' | 'unstarted' | 'started' | 'completed' | 'canceled';
  color: string;
  position: number;
}

export interface Cycle {
  id: string;
  project_id: string;
  name: string;
  description: string;
  start_date: string;
  end_date: string;
  status: 'upcoming' | 'active' | 'completed';
  issue_count: number;
  completed_count: number;
  created_at: string;
}

export interface Label {
  id: string;
  project_id: string;
  name: string;
  color: string;
}

export interface IssueSummary {
  id: string;
  identifier: string;
  title: string;
  state_id: string;
  state_name: string;
  state_category: string;
  state_color: string;
  priority: number;
  assignee_id?: string;
  assignee_name?: string;
}

export interface Issue {
  id: string;
  project_id: string;
  project_key: string;
  number: number;
  identifier: string;
  parent_id?: string;
  parent_identifier?: string;
  title: string;
  description: string;
  state_id: string;
  state_name: string;
  state_category: WorkflowState['category'];
  state_color: string;
  priority: number;
  assignee_id?: string;
  assignee_name?: string;
  creator_id: string;
  creator_name: string;
  cycle_id?: string;
  cycle_name?: string;
  position: number;
  labels: Label[];
  sub_issue_count: number;
  sub_issue_done_count: number;
  sub_issues?: IssueSummary[];
  comment_count: number;
  created_at: string;
  updated_at: string;
}

export interface Comment {
  id: string;
  issue_id: string;
  author_id: string;
  author_name: string;
  body: string;
  created_at: string;
  updated_at: string;
}

export interface Activity {
  id: string;
  issue_id: string;
  actor_id: string;
  actor_name: string;
  action: string;
  field?: string;
  old_value?: string;
  new_value?: string;
  created_at: string;
}

export interface APIToken {
  id: string;
  user_id: string;
  name: string;
  token_prefix: string;
  created_at: string;
  last_used_at?: string;
}

export interface Invite {
  id: string;
  code: string;
  role: 'admin' | 'member';
  created_by: string;
  used_by?: string;
  expires_at: string;
  created_at: string;
}

async function req<T>(path: string, init?: RequestInit): Promise<T> {
  const headers: Record<string, string> = {};
  if (init?.body && !(init.body instanceof FormData)) {
    headers['Content-Type'] = 'application/json';
  }
  const res = await fetch(path, {
    ...init,
    headers: { ...headers, ...(init?.headers as Record<string, string> | undefined) },
    credentials: 'same-origin',
  });
  if (!res.ok) {
    let msg = `HTTP ${res.status}`;
    try {
      const errBody = await res.json();
      if (errBody?.error) msg = errBody.error;
    } catch {
      // ignore json parse error
    }
    throw new Error(msg);
  }
  return res.json();
}

export const api = {
  authStatus: () => req<{ has_users: boolean; user: User | null }>('/api/auth/status'),
  setup: (email: string, name: string, password: string) =>
    req<{ user: User }>('/api/auth/setup', {
      method: 'POST',
      body: JSON.stringify({ email, name, password }),
    }),
  login: (email: string, password: string) =>
    req<{ user: User }>('/api/auth/login', {
      method: 'POST',
      body: JSON.stringify({ email, password }),
    }),
  logout: () => req<{ ok: boolean }>('/api/auth/logout', { method: 'POST' }),
  acceptInvite: (code: string, email: string, name: string, password: string) =>
    req<{ user: User }>('/api/auth/accept-invite', {
      method: 'POST',
      body: JSON.stringify({ code, email, name, password }),
    }),

  listUsers: () => req<User[]>('/api/users'),
  createUser: (email: string, name: string, password: string, role: string) =>
    req<User>('/api/users', {
      method: 'POST',
      body: JSON.stringify({ email, name, password, role }),
    }),

  updateUser: (id: string, patch: { role?: 'admin' | 'member'; disabled?: boolean }) =>
    req<User>(`/api/users/${id}`, { method: 'PATCH', body: JSON.stringify(patch) }),
  deleteUser: (id: string) => req<{ ok: boolean }>(`/api/users/${id}`, { method: 'DELETE' }),
  resetPassword: (id: string, password: string) =>
    req<{ ok: boolean }>(`/api/users/${id}/password`, { method: 'POST', body: JSON.stringify({ password }) }),
  changePassword: (current_password: string, new_password: string) =>
    req<{ ok: boolean }>('/api/auth/password', {
      method: 'POST',
      body: JSON.stringify({ current_password, new_password }),
    }),

  listTokens: () => req<APIToken[]>('/api/tokens'),
  createToken: (name: string) =>
    req<{ token: string; token_meta: APIToken }>('/api/tokens', {
      method: 'POST',
      body: JSON.stringify({ name }),
    }),
  deleteToken: (id: string) => req<{ ok: boolean }>(`/api/tokens/${id}`, { method: 'DELETE' }),

  listInvites: () => req<Invite[]>('/api/invites'),
  createInvite: (role: string) =>
    req<Invite>('/api/invites', {
      method: 'POST',
      body: JSON.stringify({ role }),
    }),

  listProjects: () => req<Project[]>('/api/projects'),
  createProject: (key: string, name: string, description: string) =>
    req<Project>('/api/projects', {
      method: 'POST',
      body: JSON.stringify({ key, name, description }),
    }),
  getProjectBundle: (ref: string) =>
    req<{ project: Project; states: WorkflowState[]; labels: Label[]; cycles: Cycle[] }>(
      `/api/projects/${encodeURIComponent(ref)}`
    ),

  createLabel: (projectRef: string, name: string, color: string) =>
    req<Label>(`/api/projects/${encodeURIComponent(projectRef)}/labels`, {
      method: 'POST',
      body: JSON.stringify({ name, color }),
    }),
  deleteLabel: (id: string) => req<{ ok: boolean }>(`/api/labels/${id}`, { method: 'DELETE' }),

  createCycle: (
    projectRef: string,
    payload: { name: string; description: string; start_date: string; end_date: string; status: string }
  ) =>
    req<Cycle>(`/api/projects/${encodeURIComponent(projectRef)}/cycles`, {
      method: 'POST',
      body: JSON.stringify(payload),
    }),
  updateCycle: (
    id: string,
    payload: { name: string; description: string; start_date: string; end_date: string; status: string }
  ) =>
    req<Cycle>(`/api/cycles/${id}`, {
      method: 'PATCH',
      body: JSON.stringify(payload),
    }),

  listIssues: (params: Record<string, string>) => {
    const qs = new URLSearchParams();
    Object.entries(params).forEach(([k, v]) => {
      if (v !== undefined && v !== '') qs.set(k, v);
    });
    return req<Issue[]>(`/api/issues?${qs.toString()}`);
  },
  getIssue: (ref: string) => req<Issue>(`/api/issues/${encodeURIComponent(ref)}`),
  getIssueDetail: (ref: string) =>
    req<{ issue: Issue; comments: Comment[]; activities: Activity[] }>(
      `/api/issues/${encodeURIComponent(ref)}?detail=1`
    ),
  createIssue: (payload: {
    project_ref: string;
    parent_ref?: string;
    title: string;
    description?: string;
    state_id?: string;
    priority?: number;
    assignee_id?: string;
    cycle_id?: string;
    label_ids?: string[];
  }) =>
    req<Issue>('/api/issues', {
      method: 'POST',
      body: JSON.stringify(payload),
    }),
  updateIssue: (ref: string, patch: Record<string, unknown>) =>
    req<Issue>(`/api/issues/${encodeURIComponent(ref)}`, {
      method: 'PATCH',
      body: JSON.stringify(patch),
    }),
  deleteIssue: (ref: string) =>
    req<{ ok: boolean }>(`/api/issues/${encodeURIComponent(ref)}`, {
      method: 'DELETE',
    }),
  addComment: (ref: string, body: string) =>
    req<Comment>(`/api/issues/${encodeURIComponent(ref)}/comments`, {
      method: 'POST',
      body: JSON.stringify({ body }),
    }),
  searchIssues: (q: string) => req<Issue[]>(`/api/search?q=${encodeURIComponent(q)}`),

  uploadAttachment: async (file: File, issueId?: string) => {
    const fd = new FormData();
    fd.append('file', file);
    if (issueId) fd.append('issue_id', issueId);
    return req<{ markdown: string }>('/api/attachments', {
      method: 'POST',
      body: fd,
    });
  },
  importProject: (bundle: unknown) =>
    req<Project>('/api/projects/import', {
      method: 'POST',
      body: JSON.stringify(bundle),
    }),
};

// assignableUsers lists enabled users for pickers, keeping the currently selected
// user (even if disabled) so an existing assignment still shows its name.
export function assignableUsers(users: User[], selectedId?: string): User[] {
  return users.filter((u) => !u.disabled_at || u.id === selectedId);
}

export const PRIORITIES = [
  { value: 0, label: 'No priority', short: '—', color: 'text-zinc-400' },
  { value: 1, label: 'Low', short: 'Low', color: 'text-sky-500' },
  { value: 2, label: 'Medium', short: 'Med', color: 'text-amber-500' },
  { value: 3, label: 'High', short: 'High', color: 'text-orange-500' },
  { value: 4, label: 'Urgent', short: 'Urgent', color: 'text-rose-500 font-semibold' },
] as const;

export function getPriorityMeta(p: number) {
  return PRIORITIES.find((x) => x.value === p) ?? PRIORITIES[0];
}
