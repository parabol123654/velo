import React, { useRef, useState } from 'react';
import { ImagePlus, Eye, Edit3 } from 'lucide-react';
import { api } from '../api';

function renderInline(text: string): React.ReactNode[] {
  // Tokenize inline images ![alt](url), links [text](url), inline code `code`, bold **text**
  const pattern = /(!\[[^\]]*\]\([^)]+\)|\[[^\]]+\]\([^)]+\)|`[^`]+`|\*\*[^*]+\*\*)/g;
  const parts = text.split(pattern);
  return parts.map((part, idx) => {
    if (!part) return null;
    if (part.startsWith('![') && part.includes('](') && part.endsWith(')')) {
      const sep = part.indexOf('](');
      const alt = part.slice(2, sep);
      const url = part.slice(sep + 2, -1);
      if (url.startsWith('/') || url.startsWith('http://') || url.startsWith('https://')) {
        return (
          <a key={idx} href={url} target="_blank" rel="noreferrer" className="block my-2">
            <img
              src={url}
              alt={alt}
              className="max-h-72 rounded-lg border border-zinc-200 dark:border-zinc-800 object-contain bg-zinc-950/30"
            />
          </a>
        );
      }
    }
    if (part.startsWith('[') && part.includes('](') && part.endsWith(')')) {
      const sep = part.indexOf('](');
      const label = part.slice(1, sep);
      const url = part.slice(sep + 2, -1);
      if (url.startsWith('/') || url.startsWith('http://') || url.startsWith('https://')) {
        return (
          <a
            key={idx}
            href={url}
            target="_blank"
            rel="noreferrer"
            className="text-indigo-500 dark:text-indigo-400 underline underline-offset-2 hover:opacity-80"
          >
            {label}
          </a>
        );
      }
    }
    if (part.startsWith('`') && part.endsWith('`') && part.length >= 2) {
      return (
        <code
          key={idx}
          className="px-1.5 py-0.5 rounded bg-zinc-200/70 dark:bg-zinc-800 font-mono text-[0.85em] text-pink-600 dark:text-pink-300"
        >
          {part.slice(1, -1)}
        </code>
      );
    }
    if (part.startsWith('**') && part.endsWith('**') && part.length >= 4) {
      return (
        <strong key={idx} className="font-semibold text-zinc-900 dark:text-zinc-100">
          {part.slice(2, -2)}
        </strong>
      );
    }
    return <React.Fragment key={idx}>{part}</React.Fragment>;
  });
}

export function MarkdownView({
  content,
  onToggleChecklist,
}: {
  content: string;
  onToggleChecklist?: (nextContent: string) => void;
}) {
  if (!content.trim()) {
    return <p className="text-sm text-zinc-400 dark:text-zinc-500 italic">No description provided.</p>;
  }

  const lines = content.split('\n');
  const blocks: React.ReactNode[] = [];
  let i = 0;

  const toggleLine = (lineIdx: number, currentlyChecked: boolean) => {
    if (!onToggleChecklist) return;
    const next = [...lines];
    next[lineIdx] = next[lineIdx].replace(
      currentlyChecked ? /^(\s*[-*]\s+)\[[xX]\]/ : /^(\s*[-*]\s+)\[ \]/,
      currentlyChecked ? '$1[ ]' : '$1[x]'
    );
    onToggleChecklist(next.join('\n'));
  };

  while (i < lines.length) {
    const line = lines[i];

    if (line.trimStart().startsWith('```')) {
      const codeLines: string[] = [];
      i++;
      while (i < lines.length && !lines[i].trimStart().startsWith('```')) {
        codeLines.push(lines[i]);
        i++;
      }
      i++; // skip closing fence
      blocks.push(
        <pre
          key={`code-${i}`}
          className="my-2 p-3 rounded-lg bg-zinc-900 text-zinc-100 border border-zinc-800 overflow-x-auto text-xs font-mono leading-relaxed"
        >
          <code>{codeLines.join('\n')}</code>
        </pre>
      );
      continue;
    }

    if (!line.trim()) {
      i++;
      continue;
    }

    if (line.startsWith('### ')) {
      blocks.push(
        <h3 key={i} className="text-sm font-semibold text-zinc-900 dark:text-zinc-100 mt-3 mb-1">
          {renderInline(line.slice(4))}
        </h3>
      );
      i++;
      continue;
    }
    if (line.startsWith('## ')) {
      blocks.push(
        <h2 key={i} className="text-base font-semibold text-zinc-900 dark:text-zinc-100 mt-3 mb-1">
          {renderInline(line.slice(3))}
        </h2>
      );
      i++;
      continue;
    }
    if (line.startsWith('# ')) {
      blocks.push(
        <h1 key={i} className="text-lg font-bold text-zinc-900 dark:text-zinc-100 mt-3 mb-1">
          {renderInline(line.slice(2))}
        </h1>
      );
      i++;
      continue;
    }

    if (line.startsWith('> ')) {
      blocks.push(
        <blockquote
          key={i}
          className="border-l-2 border-indigo-500/60 pl-3 my-1.5 text-sm text-zinc-600 dark:text-zinc-400"
        >
          {renderInline(line.slice(2))}
        </blockquote>
      );
      i++;
      continue;
    }

    const taskMatch = line.match(/^(\s*[-*]\s+)\[([ xX])\]\s+(.*)$/);
    if (taskMatch) {
      const checked = taskMatch[2].toLowerCase() === 'x';
      const lineIdx = i;
      blocks.push(
        <label
          key={i}
          className="flex items-start gap-2 py-0.5 text-sm cursor-pointer select-none group"
        >
          <input
            type="checkbox"
            checked={checked}
            disabled={!onToggleChecklist}
            onChange={() => toggleLine(lineIdx, checked)}
            className="mt-1 rounded border-zinc-400 dark:border-zinc-600 accent-indigo-500 cursor-pointer"
          />
          <span className={checked ? 'line-through text-zinc-400 dark:text-zinc-500' : 'text-zinc-800 dark:text-zinc-200'}>
            {renderInline(taskMatch[3])}
          </span>
        </label>
      );
      i++;
      continue;
    }

    const bulletMatch = line.match(/^\s*[-*]\s+(.*)$/);
    if (bulletMatch) {
      blocks.push(
        <div key={i} className="flex items-start gap-2 py-0.5 text-sm text-zinc-800 dark:text-zinc-200">
          <span className="text-zinc-400 mt-0.5">•</span>
          <div>{renderInline(bulletMatch[1])}</div>
        </div>
      );
      i++;
      continue;
    }

    blocks.push(
      <p key={i} className="text-sm leading-relaxed text-zinc-700 dark:text-zinc-300 my-1">
        {renderInline(line)}
      </p>
    );
    i++;
  }

  return <div className="space-y-0.5">{blocks}</div>;
}

export function MarkdownEditor({
  value,
  onChange,
  placeholder = 'Write Markdown... (Ctrl+V to paste screenshots, - [ ] for task lists)',
  rows = 5,
  issueId,
  onSubmitShortcut,
}: {
  value: string;
  onChange: (next: string) => void;
  placeholder?: string;
  rows?: number;
  issueId?: string;
  onSubmitShortcut?: () => void;
}) {
  const [tab, setTab] = useState<'write' | 'preview'>('write');
  const [uploading, setUploading] = useState(false);
  const taRef = useRef<HTMLTextAreaElement>(null);
  const fileRef = useRef<HTMLInputElement>(null);

  const insertAtCursor = (snippet: string) => {
    const el = taRef.current;
    if (!el) {
      onChange(value + (value ? '\n' : '') + snippet);
      return;
    }
    const start = el.selectionStart ?? value.length;
    const end = el.selectionEnd ?? value.length;
    const next = value.slice(0, start) + snippet + value.slice(end);
    onChange(next);
  };

  const handleUploadFile = async (file: File) => {
    setUploading(true);
    try {
      const res = await api.uploadAttachment(file, issueId);
      insertAtCursor(res.markdown + '\n');
    } catch (err) {
      alert(err instanceof Error ? err.message : 'Upload failed');
    } finally {
      setUploading(false);
    }
  };

  const handlePaste = async (e: React.ClipboardEvent<HTMLTextAreaElement>) => {
    const items = e.clipboardData?.items;
    if (!items) return;
    for (const item of Array.from(items)) {
      if (item.kind === 'file') {
        const f = item.getAsFile();
        if (f) {
          e.preventDefault();
          await handleUploadFile(f);
          return;
        }
      }
    }
  };

  return (
    <div className="rounded-lg border border-zinc-200 dark:border-zinc-800 bg-white dark:bg-zinc-900/70 overflow-hidden">
      <div className="flex items-center justify-between px-2.5 py-1.5 border-b border-zinc-200 dark:border-zinc-800 bg-zinc-50 dark:bg-zinc-900/90 text-xs">
        <div className="flex items-center gap-1">
          <button
            type="button"
            onClick={() => setTab('write')}
            className={`flex items-center gap-1 px-2 py-1 rounded ${
              tab === 'write'
                ? 'bg-zinc-200/80 dark:bg-zinc-800 text-zinc-900 dark:text-zinc-100 font-medium'
                : 'text-zinc-500 hover:text-zinc-800 dark:hover:text-zinc-300'
            }`}
          >
            <Edit3 className="w-3 h-3" /> Write
          </button>
          <button
            type="button"
            onClick={() => setTab('preview')}
            className={`flex items-center gap-1 px-2 py-1 rounded ${
              tab === 'preview'
                ? 'bg-zinc-200/80 dark:bg-zinc-800 text-zinc-900 dark:text-zinc-100 font-medium'
                : 'text-zinc-500 hover:text-zinc-800 dark:hover:text-zinc-300'
            }`}
          >
            <Eye className="w-3 h-3" /> Preview
          </button>
        </div>

        <div className="flex items-center gap-2">
          <input
            ref={fileRef}
            type="file"
            accept="image/*,.log,.txt,.json,.pdf"
            className="hidden"
            onChange={(e) => {
              const f = e.target.files?.[0];
              if (f) void handleUploadFile(f);
              e.target.value = '';
            }}
          />
          <button
            type="button"
            onClick={() => fileRef.current?.click()}
            disabled={uploading}
            className="flex items-center gap-1 text-zinc-500 hover:text-zinc-800 dark:hover:text-zinc-200"
          >
            <ImagePlus className="w-3.5 h-3.5" />
            <span>{uploading ? 'Uploading...' : 'Attach / Paste Image'}</span>
          </button>
        </div>
      </div>

      {tab === 'write' ? (
        <textarea
          ref={taRef}
          rows={rows}
          value={value}
          onChange={(e) => onChange(e.target.value)}
          onPaste={handlePaste}
          onKeyDown={(e) => {
            if ((e.metaKey || e.ctrlKey) && e.key === 'Enter' && onSubmitShortcut) {
              e.preventDefault();
              onSubmitShortcut();
            }
          }}
          placeholder={placeholder}
          className="w-full p-3 text-sm bg-transparent text-zinc-900 dark:text-zinc-100 placeholder:text-zinc-400 dark:placeholder:text-zinc-500 focus:outline-none resize-y font-sans"
        />
      ) : (
        <div className="p-3 min-h-[100px]">
          <MarkdownView content={value} onToggleChecklist={onChange} />
        </div>
      )}
    </div>
  );
}
