import { describe, expect, it } from 'vitest';
import { renderToStaticMarkup } from 'react-dom/server';
import { MarkdownView, toggleChecklistLine } from './Markdown';

describe('toggleChecklistLine', () => {
  const doc = 'Intro\n- [ ] write spec\n  * [x] nested done\n- not a task';

  it('checks an unchecked item', () => {
    expect(toggleChecklistLine(doc, 1, false).split('\n')[1]).toBe('- [x] write spec');
  });

  it('unchecks a checked item and keeps indentation and bullet', () => {
    expect(toggleChecklistLine(doc, 2, true).split('\n')[2]).toBe('  * [ ] nested done');
  });

  it('leaves other lines untouched', () => {
    const out = toggleChecklistLine(doc, 1, false).split('\n');
    expect([out[0], out[2], out[3]]).toEqual(['Intro', '  * [x] nested done', '- not a task']);
  });

  it('does nothing on a line that is not a task', () => {
    expect(toggleChecklistLine(doc, 3, false)).toBe(doc);
  });
});

describe('MarkdownView', () => {
  it('shows a placeholder for empty content', () => {
    expect(renderToStaticMarkup(<MarkdownView content="   " />)).toContain('No description provided.');
  });

  it('renders checklist items as checkboxes, disabled when read-only', () => {
    const html = renderToStaticMarkup(<MarkdownView content={'- [x] done\n- [ ] todo'} />);
    expect(html.match(/type="checkbox"/g)).toHaveLength(2);
    expect(html).toContain('checked=""');
    expect(html).toContain('disabled=""');
  });

  it('enables checkboxes when a toggle handler is given', () => {
    const html = renderToStaticMarkup(<MarkdownView content="- [ ] todo" onToggleChecklist={() => {}} />);
    expect(html).not.toContain('disabled=""');
  });

  it('renders fenced code verbatim without inline formatting', () => {
    const html = renderToStaticMarkup(<MarkdownView content={'```\n**not bold**\n```'} />);
    expect(html).toContain('<code>**not bold**</code>');
  });
});
