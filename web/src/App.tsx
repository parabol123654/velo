import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  Zap,
  Plus,
  Search,
  LayoutGrid,
  List,
  Inbox,
  Timer,
  Settings,
  Sun,
  Moon,
  LogOut,
  MessageSquare,
  GitBranch,
  FolderPlus,
} from 'lucide-react';
import {
  api,
  Cycle,
  getPriorityMeta,
  Issue,
  Label,
  PRIORITIES,
  Project,
  User,
  WorkflowState,
} from './api';
import { AuthScreen } from './components/AuthScreen';
import { CommandPalette } from './components/CommandPalette';
import { IssueDrawer } from './components/IssueDrawer';
import { MarkdownEditor } from './components/Markdown';
import { SettingsModal } from './components/SettingsModal';
import { applyEvent, LiveEvent } from './live';

type ViewMode = 'board' | 'list' | 'backlog' | 'cycles';

export function App() {
  const [booting, setBooting] = useState(true);
  const [hasUsers, setHasUsers] = useState(false);
  const [user, setUser] = useState<User | null>(null);
  const [dark, setDark] = useState(() => !document.documentElement.classList.contains('light'));

  const [projects, setProjects] = useState<Project[]>([]);
  const [activeKey, setActiveKey] = useState<string>('');
  const [states, setStates] = useState<WorkflowState[]>([]);
  const [labels, setLabels] = useState<Label[]>([]);
  const [cycles, setCycles] = useState<Cycle[]>([]);
  const [users, setUsers] = useState<User[]>([]);
  const [issues, setIssues] = useState<Issue[]>([]);

  const [view, setView] = useState<ViewMode>('board');
  const [filterCycle, setFilterCycle] = useState<string>('');
  const [filterAssignee, setFilterAssignee] = useState<string>('');
  const [selectedIssueRef, setSelectedIssueRef] = useState<string | null>(null);
  const [focusedIdx, setFocusedIdx] = useState<number>(0);
  const [sseConnected, setSseConnected] = useState(false);

  // Modals
  const [cmdOpen, setCmdOpen] = useState(false);
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [newProjectOpen, setNewProjectOpen] = useState(false);
  const [newIssueOpen, setNewIssueOpen] = useState(false);
  const [newIssueDefaultState, setNewIssueDefaultState] = useState<string>('');

  // Quick create states
  const [projKey, setProjKey] = useState('');
  const [projName, setProjName] = useState('');
  const [projDesc, setProjDesc] = useState('');

  const [issTitle, setIssTitle] = useState('');
  const [issDesc, setIssDesc] = useState('');
  const [issPrio, setIssPrio] = useState(0);
  const [issAssignee, setIssAssignee] = useState('');
  const [issCycle, setIssCycle] = useState('');

  const [cycleName, setCycleName] = useState('');
  const [cycleStart, setCycleStart] = useState('');
  const [cycleEnd, setCycleEnd] = useState('');

  const inviteCode = useMemo(() => {
    const m = window.location.pathname.match(/^\/invite\/([a-zA-Z0-9]+)$/);
    return m ? m[1] : null;
  }, []);

  useEffect(() => {
    document.documentElement.classList.toggle('dark', dark);
  }, [dark]);

  useEffect(() => {
    api
      .authStatus()
      .then((s) => {
        setHasUsers(s.has_users);
        setUser(s.user);
      })
      .finally(() => setBooting(false));
  }, []);

  const loadWorkspace = useCallback(async (preferredKey?: string) => {
    const [projs, team] = await Promise.all([api.listProjects(), api.listUsers()]);
    setProjects(projs);
    setUsers(team);

    const targetKey =
      preferredKey ||
      (projs.some((p) => p.key === activeKey) ? activeKey : projs[0]?.key || '');

    if (!targetKey) {
      setActiveKey('');
      setStates([]);
      setLabels([]);
      setCycles([]);
      setIssues([]);
      return;
    }

    setActiveKey(targetKey);
    const [bundle, projIssues] = await Promise.all([
      api.getProjectBundle(targetKey),
      api.listIssues({ project: targetKey }),
    ]);
    setStates(bundle.states);
    setLabels(bundle.labels);
    setCycles(bundle.cycles);
    setIssues(projIssues);
  }, [activeKey]);

  useEffect(() => {
    if (user) {
      void loadWorkspace();
    }
  }, [user, loadWorkspace]);

  // Refs let the long-lived SSE handler see current state without reconnecting.
  const issuesRef = useRef(issues);
  issuesRef.current = issues;
  const activeKeyRef = useRef(activeKey);
  activeKeyRef.current = activeKey;
  const projectIdRef = useRef('');
  projectIdRef.current = projects.find((p) => p.key === activeKey)?.id ?? '';
  const loadWorkspaceRef = useRef(loadWorkspace);
  loadWorkspaceRef.current = loadWorkspace;

  // Small refetch for counters (sidebar open counts, cycle progress); never the issue list.
  const refreshMeta = useCallback(async () => {
    const key = activeKeyRef.current;
    const [projs, bundle] = await Promise.all([
      api.listProjects(),
      key ? api.getProjectBundle(key) : Promise.resolve(null),
    ]);
    setProjects(projs);
    if (bundle && key === activeKeyRef.current) {
      setStates(bundle.states);
      setLabels(bundle.labels);
      setCycles(bundle.cycles);
    }
  }, []);

  // Real-time SSE live sync: patch only the affected cards.
  useEffect(() => {
    if (!user) return;
    const es = new EventSource('/api/events');
    let lostConnection = false;
    let metaTimer: ReturnType<typeof setTimeout> | undefined;
    let reloadTimer: ReturnType<typeof setTimeout> | undefined;
    const debounce = (timer: typeof metaTimer, fn: () => void) => {
      clearTimeout(timer);
      return setTimeout(fn, 300);
    };

    es.addEventListener('connected', () => {
      setSseConnected(true);
      // Events may have been missed while disconnected: resync once.
      if (lostConnection) void loadWorkspaceRef.current();
      lostConnection = false;
    });
    es.onmessage = (msg) => {
      let ev: LiveEvent;
      try {
        ev = JSON.parse(msg.data);
      } catch {
        return;
      }
      const res = applyEvent(issuesRef.current, ev, projectIdRef.current);
      if (res.reload) {
        reloadTimer = debounce(reloadTimer, () => void loadWorkspaceRef.current());
        return;
      }
      if (res.issues !== issuesRef.current) {
        issuesRef.current = res.issues;
        setIssues(res.issues);
      }
      for (const ref of res.refetch) {
        void api.getIssue(ref).then((fresh) =>
          setIssues((prev) => prev.map((it) => (it.id === fresh.id ? fresh : it)))
        );
      }
      if (res.refreshMeta) metaTimer = debounce(metaTimer, () => void refreshMeta());
    };
    es.onerror = () => {
      lostConnection = true;
      setSseConnected(false);
    };
    return () => {
      clearTimeout(metaTimer);
      clearTimeout(reloadTimer);
      es.close();
      setSseConnected(false);
    };
  }, [user, refreshMeta]);

  const filteredIssues = useMemo(() => {
    return issues.filter((it) => {
      if (view === 'backlog' && it.state_category !== 'backlog') return false;
      if (filterCycle && it.cycle_id !== filterCycle) return false;
      if (filterAssignee && it.assignee_id !== filterAssignee) return false;
      return true;
    });
  }, [issues, view, filterCycle, filterAssignee]);

  // Linear-style global keyboard shortcuts
  useEffect(() => {
    if (!user) return;
    const onKey = (e: KeyboardEvent) => {
      const target = e.target as HTMLElement | null;
      const inInput =
        target &&
        (target.tagName === 'INPUT' ||
          target.tagName === 'TEXTAREA' ||
          target.tagName === 'SELECT' ||
          target.isContentEditable);

      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault();
        setCmdOpen((v) => !v);
        return;
      }
      if (inInput || cmdOpen || settingsOpen || newIssueOpen || newProjectOpen || selectedIssueRef) {
        return;
      }

      if (e.key === '/') {
        e.preventDefault();
        setCmdOpen(true);
      } else if (e.key.toLowerCase() === 'c' && activeKey) {
        e.preventDefault();
        setNewIssueDefaultState('');
        setNewIssueOpen(true);
      } else if (e.key === 'j' && filteredIssues.length > 0) {
        e.preventDefault();
        setFocusedIdx((i) => Math.min(filteredIssues.length - 1, i + 1));
      } else if (e.key === 'k' && filteredIssues.length > 0) {
        e.preventDefault();
        setFocusedIdx((i) => Math.max(0, i - 1));
      } else if (e.key === 'Enter' && filteredIssues[focusedIdx]) {
        e.preventDefault();
        setSelectedIssueRef(filteredIssues[focusedIdx].identifier);
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [
    user,
    activeKey,
    cmdOpen,
    settingsOpen,
    newIssueOpen,
    newProjectOpen,
    selectedIssueRef,
    filteredIssues,
    focusedIdx,
  ]);

  if (booting) {
    return (
      <div className="min-h-screen flex items-center justify-center bg-zinc-950 text-zinc-400 text-sm">
        Loading Velo...
      </div>
    );
  }

  if (!user || inviteCode) {
    return (
      <AuthScreen
        hasUsers={hasUsers}
        inviteCode={inviteCode}
        onAuthenticated={(u) => {
          setHasUsers(true);
          setUser(u);
        }}
      />
    );
  }

  const currentProject = projects.find((p) => p.key === activeKey) ?? null;

  const handleCreateProject = async (e: React.FormEvent) => {
    e.preventDefault();
    const p = await api.createProject(projKey, projName, projDesc);
    setProjKey('');
    setProjName('');
    setProjDesc('');
    setNewProjectOpen(false);
    await loadWorkspace(p.key);
  };

  const handleCreateIssue = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!activeKey || !issTitle.trim()) return;
    const created = await api.createIssue({
      project_ref: activeKey,
      title: issTitle.trim(),
      description: issDesc,
      state_id: newIssueDefaultState || undefined,
      priority: issPrio,
      assignee_id: issAssignee || undefined,
      cycle_id: issCycle || filterCycle || undefined,
    });
    setIssTitle('');
    setIssDesc('');
    setIssPrio(0);
    setNewIssueOpen(false);
    // Show it now; the matching SSE event (issue.created) is an idempotent upsert.
    setIssues(applyEvent(issuesRef.current, { type: 'issue.created', payload: created }, projectIdRef.current).issues);
  };

  const handleDropOnColumn = async (issueIdent: string, targetStateId: string, targetIndex?: number) => {
    const colIssues = filteredIssues
      .filter((i) => i.state_id === targetStateId && i.identifier !== issueIdent)
      .sort((a, b) => a.position - b.position);

    let newPos = 1000;
    if (colIssues.length > 0) {
      if (targetIndex === undefined || targetIndex >= colIssues.length) {
        newPos = colIssues[colIssues.length - 1].position + 1000;
      } else if (targetIndex === 0) {
        newPos = colIssues[0].position / 2;
      } else {
        newPos = (colIssues[targetIndex - 1].position + colIssues[targetIndex].position) / 2;
      }
    }

    // Optimistic update
    const targetState = states.find((s) => s.id === targetStateId);
    if (targetState) {
      setIssues((prev) =>
        prev.map((it) =>
          it.identifier === issueIdent
            ? {
                ...it,
                state_id: targetState.id,
                state_name: targetState.name,
                state_category: targetState.category,
                state_color: targetState.color,
                position: newPos,
              }
            : it
        )
      );
    }
    // Reconcile with the server's version; SSE (issue.updated) delivers the same card to other clients.
    const saved = await api.updateIssue(issueIdent, { state_id: targetStateId, position: newPos });
    setIssues((prev) => prev.map((it) => (it.id === saved.id ? saved : it)));
  };

  const handleCreateCycle = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!activeKey || !cycleName.trim()) return;
    await api.createCycle(activeKey, {
      name: cycleName.trim(),
      description: '',
      start_date: cycleStart,
      end_date: cycleEnd,
      status: 'active',
    });
    setCycleName('');
    setCycleStart('');
    setCycleEnd('');
    await loadWorkspace(activeKey);
  };

  return (
    <div className="h-screen flex bg-zinc-50 dark:bg-zinc-950 text-zinc-900 dark:text-zinc-100 overflow-hidden">
      {/* Sidebar */}
      <aside className="w-60 shrink-0 border-r border-zinc-200 dark:border-zinc-800/90 bg-zinc-100/60 dark:bg-zinc-900/50 flex flex-col">
        <div className="px-4 py-3.5 flex items-center justify-between border-b border-zinc-200 dark:border-zinc-800/80">
          <div className="flex items-center gap-2">
            <div className="w-7 h-7 rounded-lg bg-indigo-600 flex items-center justify-center text-white">
              <Zap className="w-4 h-4" />
            </div>
            <span className="font-bold tracking-tight text-sm">Velo</span>
            <span
              title={sseConnected ? 'Real-time SSE connected' : 'Reconnecting...'}
              className={`w-2 h-2 rounded-full ${sseConnected ? 'bg-emerald-500' : 'bg-zinc-400'}`}
            />
          </div>
          <button
            onClick={() => setCmdOpen(true)}
            title="Command Palette (Cmd+K)"
            className="flex items-center gap-1 px-2 py-1 rounded-md text-xs bg-white dark:bg-zinc-800 border border-zinc-200 dark:border-zinc-700 text-zinc-500 cursor-pointer"
          >
            <Search className="w-3 h-3" />
            <span className="font-mono text-[10px]">⌘K</span>
          </button>
        </div>

        {/* Projects */}
        <div className="p-3 flex-1 overflow-y-auto space-y-5">
          <div>
            <div className="flex items-center justify-between px-2 mb-1.5">
              <span className="text-[11px] font-semibold uppercase tracking-wider text-zinc-400">Projects</span>
              <button
                onClick={() => setNewProjectOpen(true)}
                className="text-zinc-400 hover:text-zinc-800 dark:hover:text-zinc-100 cursor-pointer"
                title="Create project"
              >
                <Plus className="w-3.5 h-3.5" />
              </button>
            </div>
            <div className="space-y-0.5">
              {projects.map((p) => (
                <button
                  key={p.id}
                  onClick={() => {
                    setFilterCycle('');
                    void loadWorkspace(p.key);
                  }}
                  className={`w-full flex items-center justify-between px-2.5 py-1.5 rounded-lg text-xs font-medium cursor-pointer ${
                    p.key === activeKey
                      ? 'bg-indigo-600/10 text-indigo-600 dark:text-indigo-300'
                      : 'text-zinc-600 dark:text-zinc-400 hover:bg-zinc-200/60 dark:hover:bg-zinc-800/60'
                  }`}
                >
                  <div className="flex items-center gap-2 truncate">
                    <span className="font-mono text-[10px] px-1.5 py-0.5 rounded bg-zinc-200 dark:bg-zinc-800 text-zinc-700 dark:text-zinc-300">
                      {p.key}
                    </span>
                    <span className="truncate">{p.name}</span>
                  </div>
                  <span className="text-[11px] text-zinc-400">{p.open_issue_count}</span>
                </button>
              ))}
            </div>
          </div>

          {currentProject && (
            <div>
              <span className="block px-2 mb-1.5 text-[11px] font-semibold uppercase tracking-wider text-zinc-400">
                Views
              </span>
              <div className="space-y-0.5">
                {[
                  { id: 'board' as const, label: 'Board', icon: LayoutGrid },
                  { id: 'list' as const, label: 'All Issues', icon: List },
                  { id: 'backlog' as const, label: 'Backlog', icon: Inbox },
                  { id: 'cycles' as const, label: 'Cycles / Sprints', icon: Timer },
                ].map((item) => {
                  const Icon = item.icon;
                  return (
                    <button
                      key={item.id}
                      onClick={() => setView(item.id)}
                      className={`w-full flex items-center gap-2 px-2.5 py-1.5 rounded-lg text-xs font-medium cursor-pointer ${
                        view === item.id
                          ? 'bg-zinc-200/80 dark:bg-zinc-800 text-zinc-900 dark:text-zinc-100'
                          : 'text-zinc-600 dark:text-zinc-400 hover:bg-zinc-200/50 dark:hover:bg-zinc-800/50'
                      }`}
                    >
                      <Icon className="w-3.5 h-3.5" />
                      <span>{item.label}</span>
                    </button>
                  );
                })}
              </div>
            </div>
          )}
        </div>

        {/* Footer */}
        <div className="p-3 border-t border-zinc-200 dark:border-zinc-800/80 space-y-1">
          <button
            onClick={() => setSettingsOpen(true)}
            className="w-full flex items-center gap-2 px-2.5 py-1.5 rounded-lg text-xs text-zinc-600 dark:text-zinc-400 hover:bg-zinc-200/60 dark:hover:bg-zinc-800/60 cursor-pointer"
          >
            <Settings className="w-3.5 h-3.5" /> Settings, Tokens & MCP
          </button>
          <div className="flex items-center justify-between px-2.5 pt-1 text-xs text-zinc-500">
            <span className="truncate font-medium text-zinc-700 dark:text-zinc-300">{user.name}</span>
            <div className="flex items-center gap-1">
              <button
                onClick={() => setDark((d) => !d)}
                title="Toggle theme"
                className="p-1 rounded hover:bg-zinc-200 dark:hover:bg-zinc-800 cursor-pointer"
              >
                {dark ? <Sun className="w-3.5 h-3.5" /> : <Moon className="w-3.5 h-3.5" />}
              </button>
              <button
                onClick={() => void api.logout().then(() => setUser(null))}
                title="Sign out"
                className="p-1 rounded hover:bg-zinc-200 dark:hover:bg-zinc-800 cursor-pointer"
              >
                <LogOut className="w-3.5 h-3.5" />
              </button>
            </div>
          </div>
        </div>
      </aside>

      {/* Main Content */}
      <main className="flex-1 flex flex-col min-w-0">
        {!currentProject ? (
          <div className="flex-1 flex flex-col items-center justify-center p-8 text-center">
            <div className="w-12 h-12 rounded-2xl bg-indigo-600/10 text-indigo-500 flex items-center justify-center mb-4">
              <FolderPlus className="w-6 h-6" />
            </div>
            <h2 className="text-lg font-semibold mb-1">Create your first project</h2>
            <p className="text-xs text-zinc-500 max-w-sm mb-5">
              Projects group issues with automatic identifiers like <code className="font-mono">DEV-1</code>, default Kanban states, and instant MCP access.
            </p>
            <button
              onClick={() => setNewProjectOpen(true)}
              className="px-4 py-2 rounded-lg bg-indigo-600 hover:bg-indigo-500 text-white text-sm font-medium cursor-pointer"
            >
              + New Project
            </button>
          </div>
        ) : (
          <>
            {/* Top Bar */}
            <header className="h-13 shrink-0 px-6 border-b border-zinc-200 dark:border-zinc-800/80 flex items-center justify-between gap-4">
              <div className="flex items-center gap-3 min-w-0">
                <h1 className="font-semibold text-sm truncate">{currentProject.name}</h1>
                <select
                  value={filterCycle}
                  onChange={(e) => setFilterCycle(e.target.value)}
                  className="text-xs px-2 py-1 rounded-md border border-zinc-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 text-zinc-600 dark:text-zinc-300"
                >
                  <option value="">All Cycles</option>
                  {cycles.map((c) => (
                    <option key={c.id} value={c.id}>
                      Cycle: {c.name}
                    </option>
                  ))}
                </select>
                <select
                  value={filterAssignee}
                  onChange={(e) => setFilterAssignee(e.target.value)}
                  className="text-xs px-2 py-1 rounded-md border border-zinc-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 text-zinc-600 dark:text-zinc-300"
                >
                  <option value="">All Assignees</option>
                  {users.map((u) => (
                    <option key={u.id} value={u.id}>
                      {u.name}
                    </option>
                  ))}
                </select>
              </div>

              <button
                onClick={() => {
                  setNewIssueDefaultState('');
                  setNewIssueOpen(true);
                }}
                className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-indigo-600 hover:bg-indigo-500 text-white text-xs font-medium shrink-0 cursor-pointer"
              >
                <Plus className="w-3.5 h-3.5" /> New Issue
                <kbd className="ml-1 px-1 rounded bg-indigo-700/80 text-[10px] font-mono">C</kbd>
              </button>
            </header>

            {/* View Body */}
            {view === 'board' && (
              <div className="flex-1 overflow-x-auto p-5">
                <div className="flex gap-4 h-full min-w-max">
                  {states.map((st) => {
                    const colIssues = filteredIssues
                      .filter((i) => i.state_id === st.id)
                      .sort((a, b) => a.position - b.position);
                    return (
                      <div
                        key={st.id}
                        onDragOver={(e) => e.preventDefault()}
                        onDrop={(e) => {
                          e.preventDefault();
                          const ident = e.dataTransfer.getData('text/plain');
                          if (ident) void handleDropOnColumn(ident, st.id);
                        }}
                        className="w-72 flex flex-col rounded-xl bg-zinc-100/80 dark:bg-zinc-900/50 border border-zinc-200/80 dark:border-zinc-800/70"
                      >
                        <div className="px-3.5 py-2.5 flex items-center justify-between">
                          <div className="flex items-center gap-2 text-xs font-semibold">
                            <span className="w-2.5 h-2.5 rounded-full" style={{ backgroundColor: st.color }} />
                            <span>{st.name}</span>
                            <span className="text-zinc-400 font-normal">{colIssues.length}</span>
                          </div>
                          <button
                            onClick={() => {
                              setNewIssueDefaultState(st.id);
                              setNewIssueOpen(true);
                            }}
                            className="text-zinc-400 hover:text-zinc-700 dark:hover:text-zinc-200 cursor-pointer"
                          >
                            <Plus className="w-3.5 h-3.5" />
                          </button>
                        </div>

                        <div className="flex-1 overflow-y-auto px-2.5 pb-2.5 space-y-2">
                          {colIssues.map((iss, idx) => {
                            const prio = getPriorityMeta(iss.priority);
                            return (
                              <div
                                key={iss.id}
                                draggable
                                onDragStart={(e) => {
                                  e.dataTransfer.setData('text/plain', iss.identifier);
                                }}
                                onDragOver={(e) => e.preventDefault()}
                                onDrop={(e) => {
                                  e.preventDefault();
                                  e.stopPropagation();
                                  const ident = e.dataTransfer.getData('text/plain');
                                  if (ident && ident !== iss.identifier) {
                                    void handleDropOnColumn(ident, st.id, idx);
                                  }
                                }}
                                onClick={() => setSelectedIssueRef(iss.identifier)}
                                className="p-3 rounded-lg bg-white dark:bg-zinc-900 border border-zinc-200 dark:border-zinc-800 hover:border-indigo-500/50 shadow-2xs cursor-pointer transition group"
                              >
                                <div className="flex items-center justify-between text-[11px] mb-1">
                                  <span className="font-mono text-zinc-400">{iss.identifier}</span>
                                  <span className={prio.color}>{prio.short}</span>
                                </div>
                                <div className="text-xs font-medium text-zinc-800 dark:text-zinc-100 leading-snug mb-2">
                                  {iss.title}
                                </div>
                                <div className="flex items-center justify-between gap-2 flex-wrap">
                                  <div className="flex items-center gap-1 flex-wrap">
                                    {iss.labels.map((lb) => (
                                      <span
                                        key={lb.id}
                                        className="px-1.5 py-0.2 rounded text-[10px] text-white"
                                        style={{ backgroundColor: lb.color }}
                                      >
                                        {lb.name}
                                      </span>
                                    ))}
                                    {iss.sub_issue_count > 0 && (
                                      <span className="inline-flex items-center gap-0.5 text-[10px] text-zinc-400">
                                        <GitBranch className="w-3 h-3" />
                                        {iss.sub_issue_done_count}/{iss.sub_issue_count}
                                      </span>
                                    )}
                                    {iss.comment_count > 0 && (
                                      <span className="inline-flex items-center gap-0.5 text-[10px] text-zinc-400">
                                        <MessageSquare className="w-3 h-3" />
                                        {iss.comment_count}
                                      </span>
                                    )}
                                  </div>
                                  {iss.assignee_name && (
                                    <span className="text-[10px] px-1.5 py-0.5 rounded bg-zinc-100 dark:bg-zinc-800 text-zinc-500">
                                      {iss.assignee_name}
                                    </span>
                                  )}
                                </div>
                              </div>
                            );
                          })}
                        </div>
                      </div>
                    );
                  })}
                </div>
              </div>
            )}

            {(view === 'list' || view === 'backlog') && (
              <div className="flex-1 overflow-y-auto">
                {filteredIssues.length === 0 ? (
                  <div className="py-16 text-center text-xs text-zinc-400">
                    No issues in this view. Press <kbd className="px-1.5 py-0.5 rounded bg-zinc-800 text-zinc-300">C</kbd> to create one.
                  </div>
                ) : (
                  <div className="divide-y divide-zinc-200 dark:divide-zinc-800/80">
                    {filteredIssues.map((iss, idx) => {
                      const prio = getPriorityMeta(iss.priority);
                      const focused = idx === focusedIdx;
                      return (
                        <div
                          key={iss.id}
                          onClick={() => {
                            setFocusedIdx(idx);
                            setSelectedIssueRef(iss.identifier);
                          }}
                          className={`flex items-center justify-between gap-4 px-6 py-2.5 text-xs cursor-pointer ${
                            focused
                              ? 'bg-indigo-600/8 dark:bg-indigo-500/10'
                              : 'hover:bg-zinc-100/80 dark:hover:bg-zinc-900/60'
                          }`}
                        >
                          <div className="flex items-center gap-3 min-w-0">
                            <span className={`w-12 shrink-0 text-[11px] ${prio.color}`}>{prio.short}</span>
                            <span className="font-mono text-zinc-400 w-16 shrink-0">{iss.identifier}</span>
                            <span className="w-2.5 h-2.5 rounded-full shrink-0" style={{ backgroundColor: iss.state_color }} />
                            <span className="font-medium text-zinc-800 dark:text-zinc-100 truncate">{iss.title}</span>
                            {iss.labels.map((lb) => (
                              <span
                                key={lb.id}
                                className="px-1.5 py-0.5 rounded text-[10px] text-white shrink-0"
                                style={{ backgroundColor: lb.color }}
                              >
                                {lb.name}
                              </span>
                            ))}
                          </div>
                          <div className="flex items-center gap-3 shrink-0 text-zinc-400">
                            {iss.cycle_name && (
                              <span className="px-2 py-0.5 rounded bg-zinc-100 dark:bg-zinc-800 text-[11px]">
                                {iss.cycle_name}
                              </span>
                            )}
                            <span className="w-24 text-right truncate">{iss.assignee_name || 'Unassigned'}</span>
                            <span className="w-24 text-right text-zinc-500">{iss.state_name}</span>
                          </div>
                        </div>
                      );
                    })}
                  </div>
                )}
              </div>
            )}

            {view === 'cycles' && (
              <div className="flex-1 overflow-y-auto p-6 max-w-4xl space-y-6">
                <form
                  onSubmit={handleCreateCycle}
                  className="p-4 rounded-xl border border-zinc-200 dark:border-zinc-800 bg-white dark:bg-zinc-900/60 flex flex-wrap items-end gap-3"
                >
                  <div className="flex-1 min-w-48">
                    <label className="block text-xs text-zinc-400 mb-1">Sprint / Cycle Name</label>
                    <input
                      type="text"
                      required
                      value={cycleName}
                      onChange={(e) => setCycleName(e.target.value)}
                      placeholder="Sprint 1 — Core MVP"
                      className="w-full px-3 py-1.5 rounded-lg text-xs border border-zinc-300 dark:border-zinc-700 bg-transparent"
                    />
                  </div>
                  <div>
                    <label className="block text-xs text-zinc-400 mb-1">Start Date</label>
                    <input
                      type="date"
                      value={cycleStart}
                      onChange={(e) => setCycleStart(e.target.value)}
                      className="px-3 py-1.5 rounded-lg text-xs border border-zinc-300 dark:border-zinc-700 bg-transparent"
                    />
                  </div>
                  <div>
                    <label className="block text-xs text-zinc-400 mb-1">End Date</label>
                    <input
                      type="date"
                      value={cycleEnd}
                      onChange={(e) => setCycleEnd(e.target.value)}
                      className="px-3 py-1.5 rounded-lg text-xs border border-zinc-300 dark:border-zinc-700 bg-transparent"
                    />
                  </div>
                  <button
                    type="submit"
                    className="px-4 py-1.5 rounded-lg bg-indigo-600 hover:bg-indigo-500 text-white text-xs font-medium cursor-pointer"
                  >
                    + Create Cycle
                  </button>
                </form>

                <div className="grid gap-3">
                  {cycles.length === 0 ? (
                    <p className="text-xs text-zinc-400">No cycles created yet.</p>
                  ) : (
                    cycles.map((c) => {
                      const pct = c.issue_count > 0 ? Math.round((c.completed_count / c.issue_count) * 100) : 0;
                      return (
                        <div
                          key={c.id}
                          className="p-4 rounded-xl border border-zinc-200 dark:border-zinc-800 bg-white dark:bg-zinc-900/60 flex items-center justify-between gap-4"
                        >
                          <div className="space-y-1 min-w-0 flex-1">
                            <div className="flex items-center gap-2">
                              <span className="font-semibold text-sm">{c.name}</span>
                              <span className="text-[11px] px-2 py-0.5 rounded bg-indigo-500/10 text-indigo-500 capitalize">
                                {c.status}
                              </span>
                            </div>
                            <p className="text-xs text-zinc-400">
                              {c.start_date || '—'} → {c.end_date || '—'} • {c.completed_count}/{c.issue_count} completed ({pct}%)
                            </p>
                            <div className="w-full max-w-xs h-1.5 rounded-full bg-zinc-200 dark:bg-zinc-800 overflow-hidden">
                              <div className="h-full bg-emerald-500" style={{ width: `${pct}%` }} />
                            </div>
                          </div>
                          <button
                            onClick={() => {
                              setFilterCycle(c.id);
                              setView('board');
                            }}
                            className="px-3 py-1.5 rounded-lg border border-zinc-200 dark:border-zinc-700 text-xs hover:bg-zinc-100 dark:hover:bg-zinc-800 cursor-pointer"
                          >
                            Open Board →
                          </button>
                        </div>
                      );
                    })
                  )}
                </div>
              </div>
            )}
          </>
        )}
      </main>

      {/* Quick Create Project Modal */}
      {newProjectOpen && (
        <div
          className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 backdrop-blur-xs p-4"
          onClick={() => setNewProjectOpen(false)}
        >
          <form
            onSubmit={handleCreateProject}
            onClick={(e) => e.stopPropagation()}
            className="w-full max-w-md rounded-2xl border border-zinc-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 p-6 shadow-2xl space-y-4"
          >
            <h3 className="text-base font-semibold">Create Project</h3>
            <div>
              <label className="block text-xs text-zinc-400 mb-1">Key Prefix (2–10 chars, e.g. DEV)</label>
              <input
                autoFocus
                required
                maxLength={10}
                value={projKey}
                onChange={(e) => setProjKey(e.target.value.toUpperCase())}
                placeholder="DEV"
                className="w-full px-3 py-2 rounded-lg text-sm font-mono border border-zinc-300 dark:border-zinc-700 bg-transparent"
              />
            </div>
            <div>
              <label className="block text-xs text-zinc-400 mb-1">Project Name</label>
              <input
                required
                value={projName}
                onChange={(e) => setProjName(e.target.value)}
                placeholder="Core Engineering"
                className="w-full px-3 py-2 rounded-lg text-sm border border-zinc-300 dark:border-zinc-700 bg-transparent"
              />
            </div>
            <div>
              <label className="block text-xs text-zinc-400 mb-1">Description (optional)</label>
              <input
                value={projDesc}
                onChange={(e) => setProjDesc(e.target.value)}
                placeholder="Issues, bugs & sprints"
                className="w-full px-3 py-2 rounded-lg text-sm border border-zinc-300 dark:border-zinc-700 bg-transparent"
              />
            </div>
            <div className="flex justify-end gap-2 pt-2">
              <button
                type="button"
                onClick={() => setNewProjectOpen(false)}
                className="px-3 py-1.5 rounded-lg text-xs text-zinc-500 cursor-pointer"
              >
                Cancel
              </button>
              <button
                type="submit"
                className="px-4 py-1.5 rounded-lg bg-indigo-600 hover:bg-indigo-500 text-white text-xs font-medium cursor-pointer"
              >
                Create Project
              </button>
            </div>
          </form>
        </div>
      )}

      {/* Quick Create Issue Modal */}
      {newIssueOpen && (
        <div
          className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 backdrop-blur-xs p-4"
          onClick={() => setNewIssueOpen(false)}
        >
          <form
            onSubmit={handleCreateIssue}
            onClick={(e) => e.stopPropagation()}
            className="w-full max-w-xl rounded-2xl border border-zinc-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 p-6 shadow-2xl space-y-4"
          >
            <div className="flex items-center justify-between">
              <span className="text-xs font-mono px-2 py-0.5 rounded bg-zinc-100 dark:bg-zinc-800 text-zinc-500">
                {activeKey} • New Issue
              </span>
              <span className="text-[11px] text-zinc-400">Ctrl+Enter to save</span>
            </div>
            <input
              autoFocus
              required
              value={issTitle}
              onChange={(e) => setIssTitle(e.target.value)}
              placeholder="Issue title..."
              className="w-full text-base font-medium bg-transparent border-b border-zinc-200 dark:border-zinc-800 pb-2 focus:outline-none focus:border-indigo-500"
            />
            <MarkdownEditor
              value={issDesc}
              onChange={setIssDesc}
              rows={4}
              onSubmitShortcut={() => {
                if (issTitle.trim()) {
                  void handleCreateIssue({ preventDefault: () => {} } as React.FormEvent);
                }
              }}
            />
            <div className="flex flex-wrap items-center justify-between gap-2 pt-1">
              <div className="flex items-center gap-2">
                <select
                  value={issPrio}
                  onChange={(e) => setIssPrio(Number(e.target.value))}
                  className="text-xs px-2.5 py-1.5 rounded-lg border border-zinc-200 dark:border-zinc-700 bg-white dark:bg-zinc-900"
                >
                  {PRIORITIES.map((p) => (
                    <option key={p.value} value={p.value}>
                      {p.label}
                    </option>
                  ))}
                </select>
                <select
                  value={issAssignee}
                  onChange={(e) => setIssAssignee(e.target.value)}
                  className="text-xs px-2.5 py-1.5 rounded-lg border border-zinc-200 dark:border-zinc-700 bg-white dark:bg-zinc-900"
                >
                  <option value="">Unassigned</option>
                  {users.map((u) => (
                    <option key={u.id} value={u.id}>
                      {u.name}
                    </option>
                  ))}
                </select>
                <select
                  value={issCycle}
                  onChange={(e) => setIssCycle(e.target.value)}
                  className="text-xs px-2.5 py-1.5 rounded-lg border border-zinc-200 dark:border-zinc-700 bg-white dark:bg-zinc-900"
                >
                  <option value="">No cycle</option>
                  {cycles.map((c) => (
                    <option key={c.id} value={c.id}>
                      {c.name}
                    </option>
                  ))}
                </select>
              </div>

              <div className="flex items-center gap-2">
                <button
                  type="button"
                  onClick={() => setNewIssueOpen(false)}
                  className="px-3 py-1.5 rounded-lg text-xs text-zinc-500 cursor-pointer"
                >
                  Cancel
                </button>
                <button
                  type="submit"
                  className="px-4 py-1.5 rounded-lg bg-indigo-600 hover:bg-indigo-500 text-white text-xs font-medium cursor-pointer"
                >
                  Create Issue
                </button>
              </div>
            </div>
          </form>
        </div>
      )}

      <IssueDrawer
        issueRef={selectedIssueRef}
        states={states}
        labels={labels}
        cycles={cycles}
        users={users}
        onClose={() => setSelectedIssueRef(null)}
        onSelectIssueRef={setSelectedIssueRef}
      />

      <CommandPalette
        open={cmdOpen}
        onClose={() => setCmdOpen(false)}
        onSelectIssue={(iss) => setSelectedIssueRef(iss.identifier)}
        onNewIssue={() => {
          setNewIssueDefaultState('');
          setNewIssueOpen(true);
        }}
        onChangeView={setView}
        onToggleTheme={() => setDark((d) => !d)}
        onOpenSettings={() => setSettingsOpen(true)}
      />

      <SettingsModal
        open={settingsOpen}
        onClose={() => setSettingsOpen(false)}
        currentUser={user}
        currentProject={currentProject}
        labels={labels}
        onRefresh={() => void loadWorkspace(activeKey)}
      />
    </div>
  );
}
