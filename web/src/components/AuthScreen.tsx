import React, { useState } from 'react';
import { Zap } from 'lucide-react';
import { api, User } from '../api';

export function AuthScreen({
  hasUsers,
  inviteCode,
  onAuthenticated,
}: {
  hasUsers: boolean;
  inviteCode: string | null;
  onAuthenticated: (user: User) => void;
}) {
  const mode: 'setup' | 'invite' | 'login' = !hasUsers ? 'setup' : inviteCode ? 'invite' : 'login';
  const [email, setEmail] = useState('');
  const [name, setName] = useState('');
  const [password, setPassword] = useState('');
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(false);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError('');
    setLoading(true);
    try {
      if (mode === 'setup') {
        const res = await api.setup(email, name, password);
        onAuthenticated(res.user);
      } else if (mode === 'invite' && inviteCode) {
        const res = await api.acceptInvite(inviteCode, email, name, password);
        window.history.replaceState({}, '', '/');
        onAuthenticated(res.user);
      } else {
        const res = await api.login(email, password);
        onAuthenticated(res.user);
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Authentication failed');
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className="min-h-screen flex items-center justify-center bg-zinc-50 dark:bg-zinc-950 p-4">
      <div className="w-full max-w-md rounded-2xl border border-zinc-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 p-8 shadow-xl">
        <div className="flex items-center gap-2.5 mb-6">
          <div className="w-9 h-9 rounded-xl bg-indigo-600 flex items-center justify-center text-white shadow-sm">
            <Zap className="w-5 h-5" />
          </div>
          <div>
            <h1 className="text-lg font-bold tracking-tight text-zinc-900 dark:text-zinc-100">Velo</h1>
            <p className="text-xs text-zinc-500 dark:text-zinc-400">Local-first tracker for devs & AI agents</p>
          </div>
        </div>

        <h2 className="text-base font-semibold text-zinc-900 dark:text-zinc-100 mb-1">
          {mode === 'setup'
            ? 'Create initial admin account'
            : mode === 'invite'
              ? 'Accept team invitation'
              : 'Sign in to workspace'}
        </h2>
        <p className="text-xs text-zinc-500 dark:text-zinc-400 mb-5">
          {mode === 'setup'
            ? 'First-run setup: your data stays 100% local in SQLite.'
            : mode === 'invite'
              ? 'Complete your account details to join this Velo workspace.'
              : 'Session stays active for 30 days on this device.'}
        </p>

        {error && (
          <div className="mb-4 px-3 py-2 rounded-lg bg-rose-500/10 border border-rose-500/30 text-xs text-rose-600 dark:text-rose-400">
            {error}
          </div>
        )}

        <form onSubmit={handleSubmit} className="space-y-3.5">
          {(mode === 'setup' || mode === 'invite') && (
            <div>
              <label className="block text-xs font-medium text-zinc-700 dark:text-zinc-300 mb-1">Name</label>
              <input
                type="text"
                required
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="Ada Lovelace"
                className="w-full px-3 py-2 rounded-lg text-sm border border-zinc-300 dark:border-zinc-700 bg-transparent text-zinc-900 dark:text-zinc-100 focus:outline-none focus:border-indigo-500"
              />
            </div>
          )}

          <div>
            <label className="block text-xs font-medium text-zinc-700 dark:text-zinc-300 mb-1">Email</label>
            <input
              type="email"
              required
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              placeholder="dev@local.team"
              className="w-full px-3 py-2 rounded-lg text-sm border border-zinc-300 dark:border-zinc-700 bg-transparent text-zinc-900 dark:text-zinc-100 focus:outline-none focus:border-indigo-500"
            />
          </div>

          <div>
            <label className="block text-xs font-medium text-zinc-700 dark:text-zinc-300 mb-1">Password</label>
            <input
              type="password"
              required
              minLength={6}
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              placeholder="At least 6 characters"
              className="w-full px-3 py-2 rounded-lg text-sm border border-zinc-300 dark:border-zinc-700 bg-transparent text-zinc-900 dark:text-zinc-100 focus:outline-none focus:border-indigo-500"
            />
          </div>

          <button
            type="submit"
            disabled={loading}
            className="w-full py-2.5 rounded-lg bg-indigo-600 hover:bg-indigo-500 text-white text-sm font-medium transition disabled:opacity-50 cursor-pointer"
          >
            {loading
              ? 'Please wait...'
              : mode === 'setup'
                ? 'Initialize Workspace'
                : mode === 'invite'
                  ? 'Join Workspace'
                  : 'Sign In'}
          </button>
        </form>
      </div>
    </div>
  );
}
