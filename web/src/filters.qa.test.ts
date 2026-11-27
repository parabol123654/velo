import { afterEach, describe, expect, it, vi } from 'vitest';
import { api, queryFilterParams } from './api';

describe('queryFilterParams (QA)', () => {
  it('trims tabs and newlines and keeps inner text untouched', () => {
    expect(queryFilterParams('DEV', '\t\n label:bug  "lỗi đăng nhập"\n')).toEqual({
      project: 'DEV',
      q: 'label:bug  "lỗi đăng nhập"',
    });
    expect(queryFilterParams('DEV', '\n\t ')).toBeNull();
  });

  it('leaves the project to a query that names one, so a saved project: filter works on the board', () => {
    // Sending project=DEV too would be a 400 (set both ways); the board shows only its own matches.
    expect(queryFilterParams('DEV', 'project:OPS')).toEqual({ q: 'project:OPS' });
  });

  it('keeps characters that need URL escaping intact (escaping is listIssues\' job)', () => {
    const p = queryFilterParams('DEV', 'a&b=c #x ?y');
    expect(p).toEqual({ project: 'DEV', q: 'a&b=c #x ?y' });
  });
});

describe('saved filter API client (QA)', () => {
  afterEach(() => vi.unstubAllGlobals());

  const stub = (status: number, body: unknown) => {
    const f = vi.fn(async () => new Response(JSON.stringify(body), { status }));
    vi.stubGlobal('fetch', f);
    return f;
  };

  it('encodes the id and sends only the patch', async () => {
    const f = stub(200, { id: 'a/b?c', name: 'x', query: 'is:open', position: 1, created_at: '' });
    await api.updateFilter('a/b?c', { position: 2.5 });
    const [url, init] = f.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe('/api/filters/a%2Fb%3Fc');
    expect(init.method).toBe('PATCH');
    expect(JSON.parse(init.body as string)).toEqual({ position: 2.5 });
    await api.deleteFilter('../users');
    expect((f.mock.calls[1] as unknown as [string])[0]).toBe('/api/filters/..%2Fusers');
  });

  it('surfaces the server error message on conflict', async () => {
    stub(409, { error: 'conflict: you already have a filter named "Mine"' });
    await expect(api.createFilter('mine', 'is:open')).rejects.toThrow('already have a filter named');
  });
});
