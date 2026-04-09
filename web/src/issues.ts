/* ──────────────────────────────────────────────────────────────
   Issues / Guided-Curation Panel
   Phase 24.9 — fetches suggestions from the backend Curation
   Suggestions Engine and renders interactive fix cards.
   ────────────────────────────────────────────────────────────── */

export interface Suggestion {
  id: string;
  type:
    | 'orphan'
    | 'missing_summary'
    | 'duplicate'
    | 'stale'
    | 'untyped'
    | 'disconnected';
  severity: 'error' | 'warning' | 'info';
  node_ids: string[];
  message: string;
  fix: {
    command: string;
    auto: boolean;
    args?: Record<string, unknown>;
  };
}

/* Icon map per suggestion type */
const ICONS: Record<string, string> = {
  orphan: '\u26A0',           // warning sign
  missing_summary: '\u270E',  // pencil
  duplicate: '\u2687',        // die face-6 (duplicate)
  stale: '\u231B',            // hourglass
  untyped: '\u2753',          // question mark
  disconnected: '\u2B61',     // disconnected arrow
};

/* Node type choices for the untyped dropdown */
const NODE_TYPES = [
  'function',
  'module',
  'class',
  'concept',
  'decision',
  'reference',
  'composite',
  'interface',
  'type',
  'method',
  'package',
  'summary',
];

export type CommandCallback = (cmd: string) => Promise<void>;

export class IssuesPanel {
  private container: HTMLElement;
  private suggestions: Suggestion[] = [];
  private dismissed: Set<string> = new Set();
  private badgeEl: HTMLElement;
  private onExecuteCommand: CommandCallback;
  private nodeCount = 0;

  constructor(
    container: HTMLElement,
    badge: HTMLElement,
    onExecuteCommand: CommandCallback,
  ) {
    this.container = container;
    this.badgeEl = badge;
    this.onExecuteCommand = onExecuteCommand;
  }

  /* ── Public API ─────────────────────────────────────────────── */

  async load(namespace?: string): Promise<void> {
    const params = new URLSearchParams();
    if (namespace) params.set('ns', namespace);
    params.set('limit', '50');

    try {
      const res = await fetch(`/api/curator/suggestions?${params}`);
      if (!res.ok) {
        console.warn('[issues] suggestions endpoint returned', res.status);
        this.suggestions = [];
        this.updateBadge();
        this.render();
        return;
      }
      const data = await res.json();
      this.suggestions = (data.suggestions ?? []) as Suggestion[];
      this.nodeCount = data.node_count ?? 0;
    } catch (err) {
      console.warn('[issues] failed to load suggestions:', err);
      this.suggestions = [];
    }

    this.dismissed.clear();
    this.updateBadge();
    this.render();
  }

  getCount(): number {
    return this.activeSuggestions().length;
  }

  clear(): void {
    this.suggestions = [];
    this.dismissed.clear();
    this.updateBadge();
    this.container.innerHTML = '';
  }

  /* ── Internal helpers ───────────────────────────────────────── */

  private activeSuggestions(): Suggestion[] {
    return this.suggestions.filter((s) => !this.dismissed.has(s.id));
  }

  private updateBadge(): void {
    const count = this.getCount();
    this.badgeEl.textContent = String(count);
    if (count === 0) {
      this.badgeEl.hidden = true;
    } else {
      this.badgeEl.hidden = false;
    }
  }

  /* ── Render ─────────────────────────────────────────────────── */

  private render(): void {
    this.container.innerHTML = '';

    // Health summary
    this.container.appendChild(this.renderHealthSummary(this.nodeCount));

    const active = this.activeSuggestions();
    if (active.length === 0) {
      const empty = document.createElement('div');
      empty.className = 'issues-empty';
      empty.textContent = 'No issues found — your graph looks healthy.';
      this.container.appendChild(empty);
      return;
    }

    for (const s of active) {
      this.container.appendChild(this.renderCard(s));
    }
  }

  private renderHealthSummary(nodeCount: number): HTMLElement {
    const issueCount = this.getCount();
    const pct =
      nodeCount > 0
        ? Math.round(((nodeCount - issueCount) / nodeCount) * 100)
        : 100;

    const wrapper = document.createElement('div');
    wrapper.className = 'issues-health';

    // Stats line
    const statsLine = document.createElement('div');
    statsLine.className = 'issues-health-stats';
    statsLine.innerHTML = `
      <span class="health-stat">${nodeCount} node${nodeCount !== 1 ? 's' : ''}</span>
      <span class="health-divider">\u00B7</span>
      <span class="health-stat issues-count">${issueCount} issue${issueCount !== 1 ? 's' : ''}</span>
    `;
    wrapper.appendChild(statsLine);

    // Progress bar
    const bar = document.createElement('div');
    bar.className = 'issues-progress';
    const fill = document.createElement('div');
    fill.className = 'issues-progress-fill';
    fill.style.width = `${pct}%`;
    bar.appendChild(fill);
    wrapper.appendChild(bar);

    const pctLabel = document.createElement('div');
    pctLabel.className = 'issues-pct-label';
    pctLabel.textContent = `${pct}% curated`;
    wrapper.appendChild(pctLabel);

    return wrapper;
  }

  private renderCard(s: Suggestion): HTMLElement {
    const card = document.createElement('div');
    card.className = `issue-card issue-${s.severity}`;
    card.dataset.id = s.id;

    // Icon
    const icon = document.createElement('div');
    icon.className = 'issue-icon';
    icon.textContent = ICONS[s.type] ?? '\u26A0';
    card.appendChild(icon);

    // Body
    const body = document.createElement('div');
    body.className = 'issue-body';

    const msg = document.createElement('div');
    msg.className = 'issue-message';
    msg.textContent = s.message;
    body.appendChild(msg);

    // Node pills
    if (s.node_ids.length > 0) {
      const pills = document.createElement('div');
      pills.className = 'issue-nodes';
      for (const nid of s.node_ids) {
        const pill = document.createElement('span');
        pill.className = 'node-ref';
        pill.textContent = shortId(nid);
        pill.title = nid;
        pill.addEventListener('click', () => {
          document.dispatchEvent(
            new CustomEvent('curator-highlight-node', { detail: nid }),
          );
        });
        pills.appendChild(pill);
      }
      body.appendChild(pills);
    }

    card.appendChild(body);

    // Actions
    const actions = document.createElement('div');
    actions.className = 'issue-actions';

    this.addActionButtons(actions, s, card);

    card.appendChild(actions);
    return card;
  }

  /* ── Action buttons per suggestion type ─────────────────────── */

  private addActionButtons(
    container: HTMLElement,
    s: Suggestion,
    card: HTMLElement,
  ): void {
    switch (s.type) {
      case 'orphan':
        container.appendChild(
          this.btn('Delete', 'danger', () => this.applyFix(s, card)),
        );
        container.appendChild(
          this.btn('Keep', 'default', () => this.dismiss(s, card)),
        );
        container.appendChild(
          this.btn('Connect to\u2026', 'primary', () =>
            this.onExecuteCommand(`/link ${s.node_ids[0] ?? ''} `),
          ),
        );
        break;

      case 'missing_summary':
        container.appendChild(
          this.btn('Add Summary', 'primary', () => this.applyFix(s, card)),
        );
        container.appendChild(
          this.btn('Dismiss', 'default', () => this.dismiss(s, card)),
        );
        break;

      case 'duplicate':
        container.appendChild(
          this.btn('Merge', 'primary', () => this.applyFix(s, card)),
        );
        container.appendChild(
          this.btn('Keep Both', 'default', () => this.dismiss(s, card)),
        );
        break;

      case 'stale':
        container.appendChild(
          this.btn('Re-verify', 'primary', () => this.applyFix(s, card)),
        );
        container.appendChild(
          this.btn('Archive', 'danger', () =>
            this.onExecuteCommand(
              `/delete ${s.node_ids[0] ?? ''}`,
            ).then(() => this.removeCard(s, card)),
          ),
        );
        container.appendChild(
          this.btn('Dismiss', 'default', () => this.dismiss(s, card)),
        );
        break;

      case 'untyped':
        container.appendChild(this.typeDropdown(s, card));
        container.appendChild(
          this.btn('Dismiss', 'default', () => this.dismiss(s, card)),
        );
        break;

      case 'disconnected':
        container.appendChild(
          this.btn('Dismiss', 'default', () => this.dismiss(s, card)),
        );
        break;
    }
  }

  /* ── Helpers ────────────────────────────────────────────────── */

  private btn(
    label: string,
    variant: 'primary' | 'danger' | 'default',
    onClick: () => void,
  ): HTMLButtonElement {
    const b = document.createElement('button');
    b.className = `issue-btn${variant === 'primary' ? ' issue-btn-primary' : ''}${variant === 'danger' ? ' issue-btn-danger' : ''}`;
    b.textContent = label;
    b.addEventListener('click', (e) => {
      e.stopPropagation();
      onClick();
    });
    return b;
  }

  private typeDropdown(s: Suggestion, card: HTMLElement): HTMLSelectElement {
    const sel = document.createElement('select');
    sel.className = 'issue-btn issue-btn-primary issue-type-select';

    const placeholder = document.createElement('option');
    placeholder.textContent = 'Set Type \u25BE';
    placeholder.value = '';
    placeholder.disabled = true;
    placeholder.selected = true;
    sel.appendChild(placeholder);

    for (const t of NODE_TYPES) {
      const opt = document.createElement('option');
      opt.value = t;
      opt.textContent = t;
      sel.appendChild(opt);
    }

    sel.addEventListener('change', () => {
      const chosen = sel.value;
      if (chosen) {
        void this.onExecuteCommand(`/type ${chosen} ${s.node_ids[0] ?? ''}`).then(
          () => this.removeCard(s, card),
        );
      }
    });

    return sel;
  }

  /* ── Card lifecycle ─────────────────────────────────────────── */

  private async applyFix(s: Suggestion, card: HTMLElement): Promise<void> {
    try {
      await this.onExecuteCommand(s.fix.command);
      this.removeCard(s, card);
    } catch (err) {
      console.error('[issues] fix failed:', err);
    }
  }

  private dismiss(s: Suggestion, card: HTMLElement): void {
    this.removeCard(s, card);
  }

  private removeCard(s: Suggestion, card: HTMLElement): void {
    this.dismissed.add(s.id);
    card.classList.add('dismissing');
    card.addEventListener('transitionend', () => {
      card.remove();
      this.updateBadge();

      // If no more issues, re-render to show the "all clear" state
      if (this.getCount() === 0) {
        this.render();
      }
    });
    // Fallback if transitionend doesn't fire
    setTimeout(() => {
      if (card.parentElement) {
        card.remove();
        this.updateBadge();
        if (this.getCount() === 0) {
          this.render();
        }
      }
    }, 400);
  }
}

/* ── Utility ────────────────────────────────────────────────── */

function shortId(id: string): string {
  const parts = id.split('/');
  return parts[parts.length - 1];
}
