import { useEffect, useState } from 'react';
import { X, Trash2, Plus, GitBranch, MessageSquare, Activity as ActivityIcon, Edit2, Check } from 'lucide-react';
import {
  api,
  assignableUsers,
  Activity,
  Comment,
  Cycle,
  Issue,
  Label,
  PRIORITIES,
  User,
  WorkflowState,
} from '../api';
import { MarkdownEditor, MarkdownView } from './Markdown';

export function IssueDrawer({
  issueRef,
  states,
  labels,
  cycles,
  users,
  onClose,
  onSelectIssueRef,
  currentUser,
}: {
  issueRef: string | null;
  states: WorkflowState[];
  labels: Label[];
  cycles: Cycle[];
  users: User[];
  onClose: () => void;
  onSelectIssueRef: (ref: string) => void;
  currentUser: User;
}) {
  const [issue, setIssue] = useState<Issue | null>(null);
  const [comments, setComments] = useState<Comment[]>([]);
  const [activities, setActivities] = useState<Activity[]>([]);
  const [editingTitle, setEditingTitle] = useState(false);
  const [titleDraft, setTitleDraft] = useState('');
  const [editingDesc, setEditingDesc] = useState(false);
  const [descDraft, setDescDraft] = useState('');
  const [subTitle, setSubTitle] = useState('');
  const [commentBody, setCommentBody] = useState('');
  const [editingComment, setEditingComment] = useState<string | null>(null);
  const [commentDraft, setCommentDraft] = useState('');

  const loadDetail = async (ref: string) => {
    const d = await api.getIssueDetail(ref);
    setIssue(d.issue);
    setComments(d.comments);
    setActivities(d.activities);
    setTitleDraft(d.issue.title);
    setDescDraft(d.issue.description);
  };

  useEffect(() => {
    if (!issueRef) {
      setIssue(null);
      return;
    }
    setEditingTitle(false);
    setEditingDesc(false);
    void loadDetail(issueRef);
  }, [issueRef]);

  if (!issueRef || !issue) return null;

  const patchField = async (patch: Record<string, unknown>) => {
    const updated = await api.updateIssue(issue.identifier, patch);
    setIssue(updated);
    const d = await api.getIssueDetail(updated.identifier);
    setActivities(d.activities);
  };

  const toggleLabel = async (labelId: string) => {
    const current = issue.labels.map((l) => l.id);
    const next = current.includes(labelId)
      ? current.filter((id) => id !== labelId)
      : [...current, labelId];
    await patchField({ label_ids: next });
  };

  const handleAddSubIssue = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!subTitle.trim()) return;
    await api.createIssue({
      project_ref: issue.project_key,
      parent_ref: issue.identifier,
      title: subTitle.trim(),
    });
    setSubTitle('');
    await loadDetail(issue.identifier);
  };

  const handleAddComment = async () => {
    if (!commentBody.trim()) return;
    await api.addComment(issue.identifier, commentBody);
    setCommentBody('');
    await loadDetail(issue.identifier);
  };

  const saveComment = async (id: string) => {
    const updated = await api.updateComment(id, commentDraft);
    setComments((prev) => prev.map((c) => (c.id === id ? updated : c)));
    setEditingComment(null);
  };

  const removeComment = async (id: string) => {
    if (!confirm('Delete this comment?')) return;
    await api.deleteComment(id);
    setComments((prev) => prev.filter((c) => c.id !== id));
  };

  const handleDelete = async () => {
    if (!confirm(`Delete ${issue.identifier}?`)) return;
    await api.deleteIssue(issue.identifier);
    onClose();
  };

  return (
    <div className="fixed inset-0 z-40 flex justify-end bg-black/40 backdrop-blur-[1px]" onClick={onClose}>
      <div
        className="w-full max-w-3xl h-full bg-white dark:bg-zinc-950 border-l border-zinc-200 dark:border-zinc-800 shadow-2xl flex flex-col overflow-hidden"
        onClick={(e) => e.stopPropagation()}
      >
        {/* Header */}
        <div className="flex items-center justify-between px-6 py-3.5 border-b border-zinc-200 dark:border-zinc-800">
          <div className="flex items-center gap-2 text-xs">
            <span className="font-mono font-semibold px-2 py-0.5 rounded bg-zinc-100 dark:bg-zinc-800 text-zinc-700 dark:text-zinc-300">
              {issue.identifier}
            </span>
            {issue.parent_identifier && (
              <button
                onClick={() => onSelectIssueRef(issue.parent_identifier!)}
                className="text-indigo-500 hover:underline flex items-center gap-1 cursor-pointer"
              >
                <GitBranch className="w-3 h-3" /> Parent: {issue.parent_identifier}
              </button>
            )}
          </div>
          <div className="flex items-center gap-2">
            <button
              onClick={handleDelete}
              title="Delete issue"
              className="p-1.5 rounded-lg text-zinc-400 hover:text-rose-500 hover:bg-rose-500/10 cursor-pointer"
            >
              <Trash2 className="w-4 h-4" />
            </button>
            <button
              onClick={onClose}
              className="p-1.5 rounded-lg text-zinc-400 hover:text-zinc-700 dark:hover:text-zinc-200 hover:bg-zinc-100 dark:hover:bg-zinc-800 cursor-pointer"
            >
              <X className="w-4 h-4" />
            </button>
          </div>
        </div>

        {/* Body */}
        <div className="flex-1 overflow-y-auto p-6 space-y-6">
          {/* Title */}
          <div>
            {editingTitle ? (
              <div className="flex items-center gap-2">
                <input
                  autoFocus
                  value={titleDraft}
                  onChange={(e) => setTitleDraft(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter') {
                      void patchField({ title: titleDraft }).then(() => setEditingTitle(false));
                    } else if (e.key === 'Escape') {
                      setTitleDraft(issue.title);
                      setEditingTitle(false);
                    }
                  }}
                  className="flex-1 text-lg font-semibold px-2.5 py-1 rounded-lg border border-indigo-500 bg-transparent text-zinc-900 dark:text-zinc-100 focus:outline-none"
                />
                <button
                  onClick={() => void patchField({ title: titleDraft }).then(() => setEditingTitle(false))}
                  className="p-1.5 rounded-lg bg-indigo-600 text-white cursor-pointer"
                >
                  <Check className="w-4 h-4" />
                </button>
              </div>
            ) : (
              <h2
                onClick={() => setEditingTitle(true)}
                className="text-xl font-semibold text-zinc-900 dark:text-zinc-100 cursor-pointer hover:opacity-80 flex items-center gap-2 group"
              >
                <span>{issue.title}</span>
                <Edit2 className="w-3.5 h-3.5 opacity-0 group-hover:opacity-50" />
              </h2>
            )}
          </div>

          {/* Metadata Controls */}
          <div className="grid grid-cols-2 sm:grid-cols-4 gap-3 p-3.5 rounded-xl bg-zinc-50 dark:bg-zinc-900/60 border border-zinc-200 dark:border-zinc-800/80 text-xs">
            <div>
              <span className="block text-zinc-400 mb-1">Status</span>
              <select
                value={issue.state_id}
                onChange={(e) => void patchField({ state_id: e.target.value })}
                className="w-full px-2 py-1.5 rounded-md border border-zinc-200 dark:border-zinc-700 bg-white dark:bg-zinc-900 text-zinc-800 dark:text-zinc-200"
              >
                {states.map((s) => (
                  <option key={s.id} value={s.id}>
                    {s.name}
                  </option>
                ))}
              </select>
            </div>

            <div>
              <span className="block text-zinc-400 mb-1">Priority</span>
              <select
                value={issue.priority}
                onChange={(e) => void patchField({ priority: Number(e.target.value) })}
                className="w-full px-2 py-1.5 rounded-md border border-zinc-200 dark:border-zinc-700 bg-white dark:bg-zinc-900 text-zinc-800 dark:text-zinc-200"
              >
                {PRIORITIES.map((p) => (
                  <option key={p.value} value={p.value}>
                    {p.label}
                  </option>
                ))}
              </select>
            </div>

            <div>
              <span className="block text-zinc-400 mb-1">Assignee</span>
              <select
                value={issue.assignee_id ?? ''}
                onChange={(e) => void patchField({ assignee_id: e.target.value })}
                className="w-full px-2 py-1.5 rounded-md border border-zinc-200 dark:border-zinc-700 bg-white dark:bg-zinc-900 text-zinc-800 dark:text-zinc-200"
              >
                <option value="">Unassigned</option>
                {assignableUsers(users, issue.assignee_id).map((u) => (
                  <option key={u.id} value={u.id}>
                    {u.name}
                  </option>
                ))}
              </select>
            </div>

            <div>
              <span className="block text-zinc-400 mb-1">Cycle / Sprint</span>
              <select
                value={issue.cycle_id ?? ''}
                onChange={(e) => void patchField({ cycle_id: e.target.value })}
                className="w-full px-2 py-1.5 rounded-md border border-zinc-200 dark:border-zinc-700 bg-white dark:bg-zinc-900 text-zinc-800 dark:text-zinc-200"
              >
                <option value="">No cycle</option>
                {cycles.map((c) => (
                  <option key={c.id} value={c.id}>
                    {c.name}
                  </option>
                ))}
              </select>
            </div>
          </div>

          {/* Labels */}
          <div className="flex flex-wrap items-center gap-1.5">
            <span className="text-xs text-zinc-400 mr-1">Labels:</span>
            {labels.map((lb) => {
              const active = issue.labels.some((x) => x.id === lb.id);
              return (
                <button
                  key={lb.id}
                  onClick={() => void toggleLabel(lb.id)}
                  className={`px-2 py-0.5 rounded-full text-xs border transition cursor-pointer ${
                    active
                      ? 'border-transparent text-white font-medium'
                      : 'border-zinc-200 dark:border-zinc-800 text-zinc-500 hover:border-zinc-400'
                  }`}
                  style={active ? { backgroundColor: lb.color } : undefined}
                >
                  {lb.name}
                </button>
              );
            })}
          </div>

          {/* Description */}
          <div>
            <div className="flex items-center justify-between mb-2">
              <span className="text-xs font-semibold uppercase tracking-wider text-zinc-400">Description</span>
              {!editingDesc && (
                <button
                  onClick={() => setEditingDesc(true)}
                  className="text-xs text-indigo-500 hover:underline cursor-pointer"
                >
                  Edit Markdown
                </button>
              )}
            </div>
            {editingDesc ? (
              <div className="space-y-2">
                <MarkdownEditor
                  value={descDraft}
                  onChange={setDescDraft}
                  rows={7}
                  issueId={issue.id}
                  onSubmitShortcut={() => {
                    void patchField({ description: descDraft }).then(() => setEditingDesc(false));
                  }}
                />
                <div className="flex justify-end gap-2">
                  <button
                    onClick={() => {
                      setDescDraft(issue.description);
                      setEditingDesc(false);
                    }}
                    className="px-3 py-1.5 rounded-lg text-xs text-zinc-500 hover:bg-zinc-100 dark:hover:bg-zinc-800 cursor-pointer"
                  >
                    Cancel
                  </button>
                  <button
                    onClick={() => void patchField({ description: descDraft }).then(() => setEditingDesc(false))}
                    className="px-3 py-1.5 rounded-lg text-xs bg-indigo-600 hover:bg-indigo-500 text-white font-medium cursor-pointer"
                  >
                    Save Description
                  </button>
                </div>
              </div>
            ) : (
              <div className="p-3.5 rounded-xl border border-zinc-200 dark:border-zinc-800/80 bg-zinc-50/40 dark:bg-zinc-900/30">
                <MarkdownView
                  content={issue.description}
                  onToggleChecklist={(nextMd) => {
                    setDescDraft(nextMd);
                    void patchField({ description: nextMd });
                  }}
                />
              </div>
            )}
          </div>

          {/* Sub-issues */}
          <div>
            <div className="flex items-center justify-between mb-2">
              <span className="text-xs font-semibold uppercase tracking-wider text-zinc-400">
                Sub-issues ({issue.sub_issue_done_count}/{issue.sub_issue_count})
              </span>
            </div>
            {issue.sub_issues && issue.sub_issues.length > 0 && (
              <div className="mb-2 divide-y divide-zinc-200 dark:divide-zinc-800 rounded-xl border border-zinc-200 dark:border-zinc-800 overflow-hidden">
                {issue.sub_issues.map((sub) => (
                  <button
                    key={sub.id}
                    onClick={() => onSelectIssueRef(sub.identifier)}
                    className="w-full flex items-center justify-between px-3 py-2 text-left text-xs hover:bg-zinc-50 dark:hover:bg-zinc-900 cursor-pointer"
                  >
                    <div className="flex items-center gap-2 min-w-0">
                      <span className="w-2 h-2 rounded-full" style={{ backgroundColor: sub.state_color }} />
                      <span className="font-mono text-zinc-400">{sub.identifier}</span>
                      <span
                        className={
                          sub.state_category === 'completed'
                            ? 'line-through text-zinc-400 truncate'
                            : 'text-zinc-800 dark:text-zinc-200 truncate'
                        }
                      >
                        {sub.title}
                      </span>
                    </div>
                    <span className="text-zinc-400 shrink-0">{sub.state_name}</span>
                  </button>
                ))}
              </div>
            )}
            <form onSubmit={handleAddSubIssue} className="flex items-center gap-2">
              <input
                type="text"
                value={subTitle}
                onChange={(e) => setSubTitle(e.target.value)}
                placeholder="Add a sub-issue..."
                className="flex-1 px-3 py-1.5 rounded-lg text-xs border border-zinc-200 dark:border-zinc-800 bg-transparent text-zinc-900 dark:text-zinc-100 focus:outline-none focus:border-indigo-500"
              />
              <button
                type="submit"
                className="flex items-center gap-1 px-2.5 py-1.5 rounded-lg text-xs bg-zinc-100 dark:bg-zinc-800 hover:bg-zinc-200 dark:hover:bg-zinc-700 text-zinc-700 dark:text-zinc-200 cursor-pointer"
              >
                <Plus className="w-3.5 h-3.5" /> Add
              </button>
            </form>
          </div>

          {/* Activity Log */}
          {activities.length > 0 && (
            <div>
              <div className="flex items-center gap-1.5 text-xs font-semibold uppercase tracking-wider text-zinc-400 mb-2">
                <ActivityIcon className="w-3.5 h-3.5" /> Activity
              </div>
              <div className="space-y-1.5 text-xs text-zinc-500 dark:text-zinc-400">
                {activities.map((ac) => (
                  <div key={ac.id} className="flex items-center justify-between gap-2">
                    <span>
                      <strong className="text-zinc-700 dark:text-zinc-300">{ac.actor_name}</strong>{' '}
                      {ac.action === 'created'
                        ? 'created this issue'
                        : `changed ${ac.field} from "${ac.old_value || 'none'}" to "${ac.new_value || 'none'}"`}
                    </span>
                    <span className="text-[11px] text-zinc-400 shrink-0">
                      {new Date(ac.created_at).toLocaleString()}
                    </span>
                  </div>
                ))}
              </div>
            </div>
          )}

          {/* Comments */}
          <div>
            <div className="flex items-center gap-1.5 text-xs font-semibold uppercase tracking-wider text-zinc-400 mb-3">
              <MessageSquare className="w-3.5 h-3.5" /> Comments ({comments.length})
            </div>
            <div className="space-y-3 mb-4">
              {comments.map((cm) => (
                <div
                  key={cm.id}
                  className="p-3.5 rounded-xl border border-zinc-200 dark:border-zinc-800 bg-zinc-50/50 dark:bg-zinc-900/40"
                >
                  <div className="flex items-center justify-between text-xs mb-1.5">
                    <span className="font-semibold text-zinc-800 dark:text-zinc-200">{cm.author_name}</span>
                    <span className="flex items-center gap-2 text-zinc-400">
                      {new Date(cm.created_at).toLocaleString()}
                      {cm.updated_at !== cm.created_at && <span title={new Date(cm.updated_at).toLocaleString()}>(edited)</span>}
                      {(cm.author_id === currentUser.id || currentUser.role === 'admin') && editingComment !== cm.id && (
                        <>
                          <button
                            title="Edit comment"
                            onClick={() => {
                              setEditingComment(cm.id);
                              setCommentDraft(cm.body);
                            }}
                            className="hover:text-zinc-700 dark:hover:text-zinc-200 cursor-pointer"
                          >
                            <Edit2 className="w-3 h-3" />
                          </button>
                          <button
                            title="Delete comment"
                            onClick={() => void removeComment(cm.id)}
                            className="hover:text-rose-500 cursor-pointer"
                          >
                            <Trash2 className="w-3 h-3" />
                          </button>
                        </>
                      )}
                    </span>
                  </div>
                  {editingComment === cm.id ? (
                    <div className="space-y-2">
                      <MarkdownEditor
                        value={commentDraft}
                        onChange={setCommentDraft}
                        rows={3}
                        issueId={issue.id}
                        onSubmitShortcut={() => void saveComment(cm.id)}
                      />
                      <div className="flex justify-end gap-2 text-xs">
                        <button onClick={() => setEditingComment(null)} className="px-2.5 py-1 text-zinc-500 cursor-pointer">
                          Cancel
                        </button>
                        <button
                          onClick={() => void saveComment(cm.id)}
                          disabled={!commentDraft.trim()}
                          className="px-2.5 py-1 rounded-md bg-indigo-600 hover:bg-indigo-500 disabled:opacity-50 text-white cursor-pointer"
                        >
                          Save
                        </button>
                      </div>
                    </div>
                  ) : (
                    <MarkdownView content={cm.body} />
                  )}
                </div>
              ))}
            </div>

            <div className="space-y-2">
              <MarkdownEditor
                value={commentBody}
                onChange={setCommentBody}
                rows={3}
                issueId={issue.id}
                placeholder="Leave a comment... (Ctrl+Enter to post, Ctrl+V to paste image)"
                onSubmitShortcut={() => void handleAddComment()}
              />
              <div className="flex justify-end">
                <button
                  onClick={() => void handleAddComment()}
                  disabled={!commentBody.trim()}
                  className="px-3.5 py-1.5 rounded-lg bg-indigo-600 hover:bg-indigo-500 text-white text-xs font-medium disabled:opacity-40 cursor-pointer"
                >
                  Comment
                </button>
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}
