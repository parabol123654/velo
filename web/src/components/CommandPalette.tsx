import { useEffect, useState } from 'react';
import { Search, Plus, LayoutGrid, List, Settings, SunMoon, ArrowRight } from 'lucide-react';
import { api, Issue } from '../api';

export function CommandPalette({
  open,
  onClose,
  onSelectIssue,
  onNewIssue,
  onChangeView,
  onToggleTheme,
  onOpenSettings,
}: {
  open: boolean;
  onClose: () => void;
  onSelectIssue: (issue: Issue) => void;
  onNewIssue: () => void;
  onChangeView: (v: 'board' | 'list' | 'backlog' | 'cycles') => void;
  onToggleTheme: () => void;
  onOpenSettings: () => void;
}) {
  const [query, setQuery] = useState('');
  const [results, setResults] = useState<Issue[]>([]);
  // A query syntax error from the server (e.g. an unknown filter key), shown instead of "no results".
  const [searchError, setSearchError] = useState('');
  const [selectedIdx, setSelectedIdx] = useState(0);

  useEffect(() => {
    if (!open) {
      setQuery('');
      setResults([]);
      setSelectedIdx(0);
      return;
    }
  }, [open]);

  useEffect(() => {
    if (!open) return;
    const q = query.trim();
    if (!q) {
      setResults([]);
      setSearchError('');
      setSelectedIdx(0);
      return;
    }
    const timer = setTimeout(() => {
      api
        .searchIssues(q)
        .then((res) => {
          setResults(res);
          setSearchError('');
          setSelectedIdx(0);
        })
        .catch((e: Error) => {
          setResults([]);
          setSearchError(e.message.replace(/^validation error: /, ''));
        });
    }, 80);
    return () => clearTimeout(timer);
  }, [query, open]);

  if (!open) return null;

  const quickActions = [
    {
      label: 'Create new issue',
      shortcut: 'C',
      icon: Plus,
      run: () => {
        onClose();
        onNewIssue();
      },
    },
    {
      label: 'Switch to Kanban Board',
      shortcut: '1',
      icon: LayoutGrid,
      run: () => {
        onClose();
        onChangeView('board');
      },
    },
    {
      label: 'Switch to Issue List',
      shortcut: '2',
      icon: List,
      run: () => {
        onClose();
        onChangeView('list');
      },
    },
    {
      label: 'Settings, API Tokens & MCP Config',
      shortcut: ',',
      icon: Settings,
      run: () => {
        onClose();
        onOpenSettings();
      },
    },
    {
      label: 'Toggle Dark / Light Theme',
      shortcut: 'T',
      icon: SunMoon,
      run: () => {
        onClose();
        onToggleTheme();
      },
    },
  ];

  const showingIssues = query.trim().length > 0;
  const totalItems = showingIssues ? results.length : quickActions.length;

  return (
    <div
      className="fixed inset-0 z-50 flex items-start justify-center pt-20 bg-black/50 backdrop-blur-xs p-4"
      onClick={onClose}
    >
      <div
        className="w-full max-w-xl rounded-xl border border-zinc-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 shadow-2xl overflow-hidden"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center gap-3 px-4 py-3 border-b border-zinc-200 dark:border-zinc-800">
          <Search className="w-4 h-4 text-zinc-400 shrink-0" />
          <input
            autoFocus
            type="text"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Escape') {
                onClose();
              } else if (e.key === 'ArrowDown' && totalItems > 0) {
                e.preventDefault();
                setSelectedIdx((i) => (i + 1) % totalItems);
              } else if (e.key === 'ArrowUp' && totalItems > 0) {
                e.preventDefault();
                setSelectedIdx((i) => (i - 1 + totalItems) % totalItems);
              } else if (e.key === 'Enter' && totalItems > 0) {
                e.preventDefault();
                if (showingIssues) {
                  const chosen = results[selectedIdx];
                  if (chosen) {
                    onClose();
                    onSelectIssue(chosen);
                  }
                } else {
                  quickActions[selectedIdx]?.run();
                }
              }
            }}
            placeholder="Search issues via SQLite FTS5 (e.g. DEV-1, bug) or run a command..."
            className="w-full bg-transparent text-sm text-zinc-900 dark:text-zinc-100 placeholder:text-zinc-400 focus:outline-none"
          />
          <kbd className="text-[10px] px-1.5 py-0.5 rounded bg-zinc-100 dark:bg-zinc-800 text-zinc-500">ESC</kbd>
        </div>

        <div className="max-h-80 overflow-y-auto p-1.5">
          {showingIssues ? (
            searchError ? (
              <div className="py-6 px-4 text-center text-xs text-red-500">{searchError}</div>
            ) : results.length === 0 ? (
              <div className="py-8 text-center text-xs text-zinc-500">No matching issues found.</div>
            ) : (
              results.map((iss, idx) => (
                <button
                  key={iss.id}
                  onClick={() => {
                    onClose();
                    onSelectIssue(iss);
                  }}
                  className={`w-full flex items-center justify-between gap-3 px-3 py-2 rounded-lg text-left text-sm cursor-pointer ${
                    idx === selectedIdx
                      ? 'bg-indigo-600/10 text-indigo-600 dark:text-indigo-300'
                      : 'text-zinc-700 dark:text-zinc-200 hover:bg-zinc-100 dark:hover:bg-zinc-800/70'
                  }`}
                >
                  <div className="flex items-center gap-2.5 min-w-0">
                    <span
                      className="w-2 h-2 rounded-full shrink-0"
                      style={{ backgroundColor: iss.state_color }}
                    />
                    <span className="font-mono text-xs text-zinc-500 shrink-0">{iss.identifier}</span>
                    <span className="truncate">{iss.title}</span>
                  </div>
                  <div className="flex items-center gap-2 shrink-0">
                    <span className="text-[11px] text-zinc-400">{iss.state_name}</span>
                    <ArrowRight className="w-3.5 h-3.5 text-zinc-400" />
                  </div>
                </button>
              ))
            )
          ) : (
            quickActions.map((act, idx) => {
              const Icon = act.icon;
              return (
                <button
                  key={act.label}
                  onClick={act.run}
                  className={`w-full flex items-center justify-between px-3 py-2 rounded-lg text-sm cursor-pointer ${
                    idx === selectedIdx
                      ? 'bg-indigo-600/10 text-indigo-600 dark:text-indigo-300'
                      : 'text-zinc-700 dark:text-zinc-200 hover:bg-zinc-100 dark:hover:bg-zinc-800/70'
                  }`}
                >
                  <div className="flex items-center gap-2.5">
                    <Icon className="w-4 h-4 opacity-75" />
                    <span>{act.label}</span>
                  </div>
                  <kbd className="text-[10px] px-1.5 py-0.5 rounded bg-zinc-100 dark:bg-zinc-800 text-zinc-500 font-mono">
                    {act.shortcut}
                  </kbd>
                </button>
              );
            })
          )}
        </div>
      </div>
    </div>
  );
}
