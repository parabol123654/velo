import React, { useEffect, useState } from 'react';
import { X, KeyRound, Users, Database, Copy, Check, Trash2, Download, Upload } from 'lucide-react';
import { api, APIToken, Invite, Label, Project, User } from '../api';

export function SettingsModal({
  open,
  onClose,
  currentUser,
  currentProject,
  labels,
  onRefresh,
}: {
  open: boolean;
  onClose: () => void;
  currentUser: User;
  currentProject: Project | null;
  labels: Label[];
  onRefresh: () => void;
}) {
  const [tab, setTab] = useState<'mcp' | 'team' | 'data'>('mcp');
  const [tokens, setTokens] = useState<APIToken[]>([]);
  const [tokenName, setTokenName] = useState('Coding Agent MCP');
  const [newRawToken, setNewRawToken] = useState('');
  const [copied, setCopied] = useState(false);

  const [users, setUsers] = useState<User[]>([]);
  const [invites, setInvites] = useState<Invite[]>([]);
  const [newLabelName, setNewLabelName] = useState('');
  const [newLabelColor, setNewLabelColor] = useState('#6366f1');

  useEffect(() => {
    if (!open) return;
    void api.listTokens().then(setTokens);
    void api.listUsers().then(setUsers);
    if (currentUser.role === 'admin') {
      void api.listInvites().then(setInvites);
    }
  }, [open, currentUser.role]);

  if (!open) return null;

  const origin = window.location.origin;

  const handleGenerateToken = async (e: React.FormEvent) => {
    e.preventDefault();
    const res = await api.createToken(tokenName);
    setNewRawToken(res.token);
    setTokens((prev) => [res.token_meta, ...prev]);
  };

  const copyText = (txt: string) => {
    void navigator.clipboard.writeText(txt);
    setCopied(true);
    setTimeout(() => setCopied(false), 1800);
  };

  const handleCreateInvite = async () => {
    const inv = await api.createInvite('member');
    setInvites((prev) => [inv, ...prev]);
  };

  const handleAddLabel = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!currentProject || !newLabelName.trim()) return;
    await api.createLabel(currentProject.key, newLabelName.trim(), newLabelColor);
    setNewLabelName('');
    onRefresh();
  };

  const handleImportJSON = async (file: File) => {
    try {
      const text = await file.text();
      const parsed = JSON.parse(text);
      await api.importProject(parsed);
      onRefresh();
      onClose();
    } catch (err) {
      alert(err instanceof Error ? err.message : 'Invalid JSON export file');
    }
  };

  const mcpSnippet = JSON.stringify(
    {
      mcpServers: {
        velo: {
          command: 'velo',
          args: ['mcp'],
          env: {
            VELO_URL: origin,
            VELO_TOKEN: newRawToken || 'velo_your_token_here',
          },
        },
      },
    },
    null,
    2
  );

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 backdrop-blur-xs p-4" onClick={onClose}>
      <div
        className="w-full max-w-2xl rounded-2xl border border-zinc-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 shadow-2xl overflow-hidden flex flex-col max-h-[85vh]"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center justify-between px-6 py-4 border-b border-zinc-200 dark:border-zinc-800">
          <div className="flex items-center gap-2">
            <button
              onClick={() => setTab('mcp')}
              className={`flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-medium cursor-pointer ${
                tab === 'mcp'
                  ? 'bg-indigo-600 text-white'
                  : 'text-zinc-500 hover:bg-zinc-100 dark:hover:bg-zinc-800'
              }`}
            >
              <KeyRound className="w-3.5 h-3.5" /> API & MCP Agent
            </button>
            <button
              onClick={() => setTab('team')}
              className={`flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-medium cursor-pointer ${
                tab === 'team'
                  ? 'bg-indigo-600 text-white'
                  : 'text-zinc-500 hover:bg-zinc-100 dark:hover:bg-zinc-800'
              }`}
            >
              <Users className="w-3.5 h-3.5" /> Team & Invites
            </button>
            <button
              onClick={() => setTab('data')}
              className={`flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-medium cursor-pointer ${
                tab === 'data'
                  ? 'bg-indigo-600 text-white'
                  : 'text-zinc-500 hover:bg-zinc-100 dark:hover:bg-zinc-800'
              }`}
            >
              <Database className="w-3.5 h-3.5" /> Labels & Data Portability
            </button>
          </div>
          <button onClick={onClose} className="p-1 text-zinc-400 hover:text-zinc-700 dark:hover:text-zinc-200 cursor-pointer">
            <X className="w-4 h-4" />
          </button>
        </div>

        <div className="p-6 overflow-y-auto space-y-5 text-sm">
          {tab === 'mcp' && (
            <>
              <div>
                <h3 className="font-semibold text-zinc-900 dark:text-zinc-100 mb-1">Personal API Tokens</h3>
                <p className="text-xs text-zinc-500 mb-3">
                  Connect Claude Code, Cursor, Antigravity, or the <code className="font-mono">velo</code> CLI directly to this instance.
                </p>
                <form onSubmit={handleGenerateToken} className="flex gap-2 mb-3">
                  <input
                    type="text"
                    value={tokenName}
                    onChange={(e) => setTokenName(e.target.value)}
                    placeholder="Token label"
                    className="flex-1 px-3 py-1.5 rounded-lg text-xs border border-zinc-300 dark:border-zinc-700 bg-transparent"
                  />
                  <button
                    type="submit"
                    className="px-3.5 py-1.5 rounded-lg bg-indigo-600 hover:bg-indigo-500 text-white text-xs font-medium cursor-pointer"
                  >
                    Generate Token
                  </button>
                </form>

                {newRawToken && (
                  <div className="p-3 rounded-xl bg-emerald-500/10 border border-emerald-500/30 mb-3">
                    <div className="flex items-center justify-between text-xs text-emerald-600 dark:text-emerald-400 font-medium mb-1">
                      <span>Copy your new API token now (shown only once):</span>
                      <button
                        onClick={() => copyText(newRawToken)}
                        className="flex items-center gap-1 underline cursor-pointer"
                      >
                        {copied ? <Check className="w-3.5 h-3.5" /> : <Copy className="w-3.5 h-3.5" />}
                        {copied ? 'Copied' : 'Copy'}
                      </button>
                    </div>
                    <code className="block text-xs font-mono break-all text-zinc-900 dark:text-zinc-100">
                      {newRawToken}
                    </code>
                  </div>
                )}

                <div className="divide-y divide-zinc-200 dark:divide-zinc-800 border border-zinc-200 dark:border-zinc-800 rounded-xl">
                  {tokens.length === 0 ? (
                    <div className="p-3 text-xs text-zinc-400">No tokens created yet.</div>
                  ) : (
                    tokens.map((t) => (
                      <div key={t.id} className="flex items-center justify-between px-3 py-2 text-xs">
                        <div>
                          <span className="font-medium text-zinc-800 dark:text-zinc-200">{t.name}</span>
                          <span className="ml-2 font-mono text-zinc-400">{t.token_prefix}...</span>
                        </div>
                        <button
                          onClick={() =>
                            void api.deleteToken(t.id).then(() => setTokens((p) => p.filter((x) => x.id !== t.id)))
                          }
                          className="text-zinc-400 hover:text-rose-500 cursor-pointer"
                        >
                          <Trash2 className="w-3.5 h-3.5" />
                        </button>
                      </div>
                    ))
                  )}
                </div>
              </div>

              <div>
                <div className="flex items-center justify-between mb-1">
                  <h4 className="text-xs font-semibold uppercase tracking-wider text-zinc-400">
                    MCP Config (Claude / Cursor / Antigravity)
                  </h4>
                  <button
                    onClick={() => copyText(mcpSnippet)}
                    className="text-xs text-indigo-500 hover:underline cursor-pointer"
                  >
                    Copy JSON
                  </button>
                </div>
                <pre className="p-3 rounded-xl bg-zinc-950 text-zinc-200 text-xs font-mono overflow-x-auto border border-zinc-800">
                  {mcpSnippet}
                </pre>
              </div>
            </>
          )}

          {tab === 'team' && (
            <>
              <div>
                <h3 className="font-semibold text-zinc-900 dark:text-zinc-100 mb-2">Workspace Members ({users.length})</h3>
                <div className="divide-y divide-zinc-200 dark:divide-zinc-800 border border-zinc-200 dark:border-zinc-800 rounded-xl">
                  {users.map((u) => (
                    <div key={u.id} className="flex items-center justify-between px-3 py-2 text-xs">
                      <div>
                        <span className="font-medium text-zinc-800 dark:text-zinc-200">{u.name}</span>
                        <span className="ml-2 text-zinc-400">{u.email}</span>
                      </div>
                      <span className="px-2 py-0.5 rounded bg-zinc-100 dark:bg-zinc-800 text-zinc-500 capitalize">
                        {u.role}
                      </span>
                    </div>
                  ))}
                </div>
              </div>

              {currentUser.role === 'admin' && (
                <div>
                  <div className="flex items-center justify-between mb-2">
                    <div>
                      <h4 className="font-semibold text-zinc-900 dark:text-zinc-100">Single-use Invite Links</h4>
                      <p className="text-xs text-zinc-500">Share directly on Slack/Zalo/Discord — no SMTP setup needed.</p>
                    </div>
                    <button
                      onClick={() => void handleCreateInvite()}
                      className="px-3 py-1.5 rounded-lg bg-indigo-600 hover:bg-indigo-500 text-white text-xs font-medium cursor-pointer"
                    >
                      + Create Invite Link
                    </button>
                  </div>
                  <div className="space-y-1.5">
                    {invites.map((inv) => {
                      const url = `${origin}/invite/${inv.code}`;
                      return (
                        <div
                          key={inv.id}
                          className="flex items-center justify-between px-3 py-2 rounded-lg border border-zinc-200 dark:border-zinc-800 text-xs"
                        >
                          <code className="font-mono truncate mr-2 text-zinc-600 dark:text-zinc-300">{url}</code>
                          {inv.used_by ? (
                            <span className="text-zinc-400">Used</span>
                          ) : (
                            <button
                              onClick={() => copyText(url)}
                              className="text-indigo-500 hover:underline shrink-0 cursor-pointer"
                            >
                              Copy link
                            </button>
                          )}
                        </div>
                      );
                    })}
                  </div>
                </div>
              )}
            </>
          )}

          {tab === 'data' && (
            <>
              {currentProject && (
                <div>
                  <h3 className="font-semibold text-zinc-900 dark:text-zinc-100 mb-2">
                    Project Labels ({currentProject.key})
                  </h3>
                  <div className="flex flex-wrap gap-1.5 mb-3">
                    {labels.map((lb) => (
                      <span
                        key={lb.id}
                        className="inline-flex items-center gap-1.5 px-2.5 py-0.5 rounded-full text-xs text-white"
                        style={{ backgroundColor: lb.color }}
                      >
                        {lb.name}
                        <button
                          onClick={() => void api.deleteLabel(lb.id).then(onRefresh)}
                          className="opacity-75 hover:opacity-100 cursor-pointer"
                        >
                          ×
                        </button>
                      </span>
                    ))}
                  </div>
                  <form onSubmit={handleAddLabel} className="flex items-center gap-2">
                    <input
                      type="color"
                      value={newLabelColor}
                      onChange={(e) => setNewLabelColor(e.target.value)}
                      className="w-8 h-8 rounded cursor-pointer bg-transparent"
                    />
                    <input
                      type="text"
                      value={newLabelName}
                      onChange={(e) => setNewLabelName(e.target.value)}
                      placeholder="New label name..."
                      className="flex-1 px-3 py-1.5 rounded-lg text-xs border border-zinc-300 dark:border-zinc-700 bg-transparent"
                    />
                    <button
                      type="submit"
                      className="px-3 py-1.5 rounded-lg bg-zinc-900 dark:bg-zinc-100 text-white dark:text-zinc-900 text-xs font-medium cursor-pointer"
                    >
                      Add Label
                    </button>
                  </form>
                </div>
              )}

              <div>
                <h3 className="font-semibold text-zinc-900 dark:text-zinc-100 mb-1">Export & Import (Zero Lock-in)</h3>
                <p className="text-xs text-zinc-500 mb-3">
                  Export your project as human-readable Markdown or full JSON bundle at any time.
                </p>
                <div className="flex flex-wrap gap-2">
                  {currentProject && (
                    <>
                      <a
                        href={`/api/projects/${currentProject.key}/export?format=markdown`}
                        download={`${currentProject.key}.md`}
                        className="inline-flex items-center gap-1.5 px-3 py-2 rounded-lg border border-zinc-200 dark:border-zinc-700 text-xs hover:bg-zinc-100 dark:hover:bg-zinc-800"
                      >
                        <Download className="w-3.5 h-3.5" /> Export {currentProject.key} (.md)
                      </a>
                      <a
                        href={`/api/projects/${currentProject.key}/export?format=json`}
                        download={`${currentProject.key}.json`}
                        className="inline-flex items-center gap-1.5 px-3 py-2 rounded-lg border border-zinc-200 dark:border-zinc-700 text-xs hover:bg-zinc-100 dark:hover:bg-zinc-800"
                      >
                        <Download className="w-3.5 h-3.5" /> Export {currentProject.key} (.json)
                      </a>
                    </>
                  )}
                  <label className="inline-flex items-center gap-1.5 px-3 py-2 rounded-lg bg-indigo-600 hover:bg-indigo-500 text-white text-xs font-medium cursor-pointer">
                    <Upload className="w-3.5 h-3.5" /> Import Project JSON
                    <input
                      type="file"
                      accept=".json"
                      className="hidden"
                      onChange={(e) => {
                        const f = e.target.files?.[0];
                        if (f) void handleImportJSON(f);
                        e.target.value = '';
                      }}
                    />
                  </label>
                </div>
              </div>
            </>
          )}
        </div>
      </div>
    </div>
  );
}
