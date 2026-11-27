// @vitest-environment jsdom
// Interactive tests of the whole app against a fake API (#3): board drag and
// drop, keyboard shortcuts, the issue drawer and the query filter.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { App } from './App';
import type { Issue } from './api';

const me = { id: 'u1', email: 'me@velo.dev', name: 'Me', role: 'admin', created_at: '' };
const project = { id: 'p1', key: 'DEV', name: 'Dev', description: '', issue_seq: 3, issue_count: 3, open_issue_count: 3, created_at: '' };
const ops = { id: 'p2', key: 'OPS', name: 'Ops', description: '', issue_seq: 0, issue_count: 0, open_issue_count: 0, created_at: '' };
const todo = { id: 's1', project_id: 'p1', name: 'Todo', category: 'unstarted', color: '#888', position: 1 };
const done = { id: 's2', project_id: 'p1', name: 'Done', category: 'completed', color: '#0a0', position: 2 };

const issue = (n: number, title: string, position: number, extra: Partial<Issue> = {}): Issue => ({
  id: `i${n}`, project_id: 'p1', project_key: 'DEV', number: n, identifier: `DEV-${n}`, title, description: '',
  state_id: 's1', state_name: 'Todo', state_category: 'unstarted', state_color: '#888', priority: 0,
  creator_id: 'u1', creator_name: 'Me', position, labels: [], sub_issue_count: 0, sub_issue_done_count: 0,
  comment_count: 0, blocked_by_count: 0, created_at: '', updated_at: '', ...extra,
} as Issue);

let issues: Issue[];
let calls: { method: string; url: string; body?: unknown }[];

const json = (status: number, body: unknown) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

// The fake server: just enough of the API for the app to boot and act.
async function fakeFetch(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
  const url = new URL(String(input), 'http://velo.test');
  const method = init?.method ?? 'GET';
  const body = init?.body ? JSON.parse(String(init.body)) : undefined;
  calls.push({ method, url: url.pathname + url.search, body });
  const path = url.pathname;
  if (path === '/api/auth/status') return json(200, { has_users: true, user: me });
  if (path === '/api/projects') return json(200, [project, ops]);
  if (path === '/api/users') return json(200, [me]);
  if (path === '/api/filters' && method === 'GET') return json(200, []);
  if (path === '/api/filters' && method === 'POST') return json(201, { id: 'f1', ...body, position: 1000, created_at: '' });
  if (path === '/api/projects/DEV') return json(200, { project, states: [todo, done], labels: [], cycles: [] });
  if (path === '/api/projects/OPS') return json(200, { project: ops, states: [], labels: [], cycles: [] });
  if (path === '/api/search') return json(200, []);
  if (path === '/api/issues' && method === 'GET') {
    const q = url.searchParams.get('q');
    if (url.searchParams.get('project') === 'OPS') return json(200, []);
    if (!q) return json(200, issues);
    if (q.startsWith('bad:')) return json(400, { error: 'validation error: unknown filter "bad:"' });
    if (q === 'assignee:me') return json(200, issues.filter((i) => i.assignee_id === 'u1'));
    return json(200, []);
  }
  const m = path.match(/^\/api\/issues\/(DEV-\d+)$/);
  if (m && method === 'PATCH') {
    const cur = issues.find((i) => i.identifier === m[1])!;
    return json(200, { ...cur, ...(body as object) });
  }
  if (m && url.searchParams.get('detail')) {
    const cur = issues.find((i) => i.identifier === m[1])!;
    return json(200, { issue: cur, comments: [], activities: [] });
  }
  return json(404, { error: `fake API: no ${method} ${path}` });
}

class FakeEventSource {
  onmessage: ((e: MessageEvent) => void) | null = null;
  onerror: (() => void) | null = null;
  addEventListener() {}
  close() {}
}

beforeEach(() => {
  issues = [
    issue(1, 'Alpha', 1000, { assignee_id: 'u1', assignee_name: 'Me' }),
    issue(2, 'Bravo', 2000),
    issue(3, 'Charlie', 3000),
  ];
  calls = [];
  vi.stubGlobal('fetch', vi.fn(fakeFetch));
  vi.stubGlobal('EventSource', FakeEventSource);
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

async function renderBoard() {
  render(<App />);
  await screen.findByText('Charlie');
}

const loads = (key: string) => calls.filter((c) => c.method === 'GET' && c.url === `/api/issues?project=${key}`).length;

// #39: the workspace loads once at start and once per project switch.
describe('workspace loading', () => {
  it('loads the issues once at start', async () => {
    await renderBoard();
    await new Promise((r) => setTimeout(r, 200)); // time for a stray second load
    expect(loads('DEV')).toBe(1);
  });

  it('loads the issues once when switching project, and not the old project again', async () => {
    await renderBoard();
    await userEvent.click(screen.getByRole('button', { name: /OPS/ }));
    await waitFor(() => expect(screen.queryByText('Charlie')).toBeNull());
    await new Promise((r) => setTimeout(r, 200)); // time for a stray second load
    expect(loads('OPS')).toBe(1);
    expect(loads('DEV')).toBe(1);
  });
});

const card = (title: string) => screen.getByText(title).closest('[draggable]') as HTMLElement;
const patches = () => calls.filter((c) => c.method === 'PATCH' && c.url.startsWith('/api/issues/'));

// A DataTransfer that keeps what dragstart puts in it, as a browser does.
function dataTransfer() {
  const data: Record<string, string> = {};
  return { setData: (k: string, v: string) => (data[k] = v), getData: (k: string) => data[k] ?? '' };
}

// Titles of the cards in a column, top to bottom.
const order = (column: string) =>
  within(screen.getByRole('region', { name: `${column} column` }))
    .queryAllByText(/^(Alpha|Bravo|Charlie)$/)
    .map((el) => el.textContent);

function drag(from: string, onto: HTMLElement) {
  const dt = dataTransfer();
  fireEvent.dragStart(card(from), { dataTransfer: dt });
  fireEvent.drop(onto, { dataTransfer: dt });
}

describe('board drag and drop', () => {
  it('a card dropped on the card below takes its place', async () => {
    await renderBoard();
    drag('Alpha', card('Bravo'));
    await waitFor(() => expect(patches()).toHaveLength(1));
    expect(patches()[0]).toMatchObject({ url: '/api/issues/DEV-1', body: { state_id: 's1', position: 2500 } });
    await waitFor(() => expect(order('Todo')).toEqual(['Bravo', 'Alpha', 'Charlie']));
  });

  it('a card dropped on a card above takes its place', async () => {
    await renderBoard();
    drag('Charlie', card('Alpha'));
    await waitFor(() => expect(patches()).toHaveLength(1));
    expect(patches()[0]).toMatchObject({ url: '/api/issues/DEV-3', body: { position: 500 } });
    await waitFor(() => expect(order('Todo')).toEqual(['Charlie', 'Alpha', 'Bravo']));
  });

  it('drops a card on another column at its end, in that state', async () => {
    await renderBoard();
    drag('Bravo', screen.getByRole('region', { name: 'Done column' }));
    await waitFor(() => expect(patches()).toHaveLength(1));
    expect(patches()[0]).toMatchObject({ url: '/api/issues/DEV-2', body: { state_id: 's2', position: 1000 } });
    await waitFor(() => expect(order('Done')).toEqual(['Bravo']));
    expect(order('Todo')).toEqual(['Alpha', 'Charlie']);
  });
});

describe('keyboard shortcuts', () => {
  it('C opens the new issue form', async () => {
    await renderBoard();
    await userEvent.keyboard('c');
    expect(await screen.findByPlaceholderText('Issue title...')).toBeTruthy();
  });

  it('Ctrl+K opens the command palette', async () => {
    await renderBoard();
    await userEvent.keyboard('{Control>}k{/Control}');
    expect(await screen.findByPlaceholderText(/Search issues/)).toBeTruthy();
  });

  it('J and K move the focus and Enter opens that issue in the drawer', async () => {
    await renderBoard();
    await userEvent.keyboard('jjk{Enter}'); // down, down, up: the second issue
    await waitFor(() => expect(calls.some((c) => c.url === '/api/issues/DEV-2?detail=1')).toBe(true));
    // The drawer shows it: its title appears a second time, beside the card.
    await waitFor(() => expect(screen.getAllByText('Bravo').length).toBeGreaterThan(1));
  });

  it('typing in the filter box is not a shortcut', async () => {
    await renderBoard();
    await userEvent.type(screen.getByLabelText('Filter issues'), 'c');
    expect(screen.queryByPlaceholderText('Issue title...')).toBeNull();
  });
});

describe('query filter', () => {
  it('shows only the matching issues, and all again when cleared', async () => {
    await renderBoard();
    const box = screen.getByLabelText('Filter issues');
    await userEvent.type(box, 'assignee:me{Enter}');
    await waitFor(() => expect(screen.queryByText('Bravo')).toBeNull());
    expect(screen.getByText('Alpha')).toBeTruthy();
    expect(calls.some((c) => c.url === '/api/issues?project=DEV&q=assignee%3Ame')).toBe(true);
    await userEvent.type(box, '{Escape}');
    expect(await screen.findByText('Bravo')).toBeTruthy();
  });

  it("shows the server's syntax error and does not filter", async () => {
    await renderBoard();
    await userEvent.type(screen.getByLabelText('Filter issues'), 'bad:x{Enter}');
    expect(await screen.findByText('unknown filter "bad:"')).toBeTruthy();
    expect(screen.getByText('Bravo')).toBeTruthy();
    expect(screen.getByLabelText('Filter issues').getAttribute('aria-invalid')).toBe('true');
  });

  it('saves the applied query under a name', async () => {
    vi.spyOn(window, 'prompt').mockReturnValue('Mine');
    await renderBoard();
    await userEvent.type(screen.getByLabelText('Filter issues'), 'assignee:me{Enter}');
    await userEvent.click(await screen.findByTitle('Save this filter'));
    await waitFor(() =>
      expect(calls.find((c) => c.method === 'POST' && c.url === '/api/filters')?.body).toEqual({ name: 'Mine', query: 'assignee:me' })
    );
  });
});
