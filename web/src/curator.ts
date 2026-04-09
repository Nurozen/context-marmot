/* ──────────────────────────────────────────────────────────────────
   Curator — bottom-drawer chat + slash-command panel
   Phase 24.5 + 24.6: Full implementation with slash commands,
   autocomplete, drag resize, and issues panel.
   ────────────────────────────────────────────────────────────────── */

import type { APINode, GraphResponse } from './types';

/* ------------------------------------------------------------------ */
/*  Types                                                              */
/* ------------------------------------------------------------------ */

interface ChatMessage {
  role: 'user' | 'system';
  content: string;
  timestamp: number;
}

interface ChatAction {
  type: string;
  [key: string]: unknown;
}

interface ChatResponse {
  message: {
    content: string;
    actions?: ChatAction[];
  };
}

/** Shape returned by /api/verify/:ns */
interface Suggestion {
  type: 'orphan' | 'missing_type' | 'duplicate' | 'stale' | 'untagged';
  message: string;
  node_ids: string[];
  actions: string[];
}

/** Slash command definition for autocomplete */
interface SlashCommand {
  name: string;
  hint: string;
}

const SLASH_COMMANDS: SlashCommand[] = [
  { name: 'tag', hint: '<name> -- Add tag to selected nodes' },
  { name: 'untag', hint: '<name> -- Remove tag' },
  { name: 'type', hint: '<type> -- Change node type' },
  { name: 'merge', hint: '<A> <B> -- Merge B into A' },
  { name: 'delete', hint: '-- Delete selected node(s)' },
  { name: 'link', hint: '<A> <rel> <B> -- Create edge' },
  { name: 'unlink', hint: '<A> <rel> <B> -- Remove edge' },
  { name: 'verify', hint: '-- Run health check' },
];

const NODE_TYPES = [
  'function',
  'module',
  'class',
  'concept',
  'decision',
  'reference',
  'composite',
];

const RELATION_TYPES = [
  'contains',
  'imports',
  'extends',
  'implements',
  'calls',
  'reads',
  'writes',
  'references',
  'cross_project',
  'associated',
];

/* ------------------------------------------------------------------ */
/*  Curator                                                            */
/* ------------------------------------------------------------------ */

export class Curator {
  /* DOM handles */
  private drawer: HTMLElement;
  private input: HTMLInputElement;
  private sendBtn: HTMLElement;
  private messagesEl: HTMLElement;
  private issuesList: HTMLElement;
  private badge: HTMLElement;
  private hintEl: HTMLElement;

  /* State */
  private chatHistory: ChatMessage[] = [];
  private selectedNodes: string[] = [];
  private nodeIds: string[] = [];
  private graphData: GraphResponse | null = null;
  private currentNamespace = 'default';
  private sessionId: string;
  private expanded = false;
  private drawerHeight = 320;
  private inputFocused = false;

  /* Command mode */
  private commandMode = false;
  private autocompleteEl: HTMLElement | null = null;
  private autocompleteIdx = -1;

  /* Input history */
  private inputHistory: string[] = [];
  private historyIdx = -1;

  constructor() {
    this.drawer = document.getElementById('curator-drawer')!;
    this.input = document.getElementById('chat-input') as HTMLInputElement;
    this.sendBtn = document.getElementById('chat-send')!;
    this.messagesEl = document.getElementById('chat-messages')!;
    this.issuesList = document.getElementById('issues-list')!;
    this.badge = document.getElementById('issues-badge')!;
    this.hintEl = document.getElementById('chat-context-hint')!;
    this.sessionId = crypto.randomUUID?.() ?? `s-${Date.now()}`;

    this.bindEvents();
    this.initDragResize();
  }

  /* ================================================================ */
  /*  Event binding                                                    */
  /* ================================================================ */

  private bindEvents(): void {
    /* Input events */
    this.input.addEventListener('input', () => this.handleInput());
    this.input.addEventListener('keydown', (e) => this.handleKeydown(e));
    this.sendBtn.addEventListener('click', () => this.submit());

    /* Track focus for @mention injection */
    this.input.addEventListener('focus', () => {
      this.inputFocused = true;
    });
    this.input.addEventListener('blur', () => {
      setTimeout(() => {
        this.inputFocused = false;
      }, 150);
    });

    /* Drawer handle: double-click toggles */
    this.drawer.querySelector('.curator-handle')?.addEventListener('dblclick', () => {
      this.toggle();
    });

    /* Tab switching */
    this.drawer.querySelectorAll('.curator-tab').forEach((tab) => {
      tab.addEventListener('click', () => {
        const target = (tab as HTMLElement).dataset.tab!;
        this.switchTab(target);
      });
    });

    /* Click on node-ref pills in chat messages */
    this.messagesEl.addEventListener('click', (e) => {
      const ref = (e.target as HTMLElement).closest('.node-ref') as HTMLElement | null;
      if (ref) {
        const nodeId = ref.dataset.nodeId;
        if (nodeId) {
          document.dispatchEvent(
            new CustomEvent('curator-highlight-node', { detail: { nodeId } }),
          );
        }
      }
    });

    /* Listen for multi-select changes from graph-view */
    document.addEventListener('curator-selection-changed', ((e: CustomEvent) => {
      this.setSelectedNodes(e.detail.nodeIds as string[]);
    }) as EventListener);

    /* Listen for @mention injection (node click while input focused) */
    document.addEventListener('curator-inject-mention', ((e: CustomEvent) => {
      const nodeId = e.detail.nodeId as string;
      this.injectMention(nodeId);
    }) as EventListener);
  }

  /* ================================================================ */
  /*  Public API                                                       */
  /* ================================================================ */

  /** Called after every loadGraph() to keep node IDs and graph data in sync. */
  updateGraphData(data: GraphResponse, namespace: string): void {
    this.graphData = data;
    this.currentNamespace = namespace;
    this.nodeIds = data.nodes.map((n) => n.id);
  }

  /** Set node IDs directly (convenience). */
  setNodeIds(ids: string[]): void {
    this.nodeIds = ids;
  }

  /** Set the current namespace. */
  setNamespace(ns: string): void {
    this.currentNamespace = ns;
  }

  /** Toggle the drawer open/closed. */
  toggle(): void {
    if (this.expanded) {
      this.collapse();
    } else {
      this.expand();
    }
  }

  /** Open the drawer and focus the input. */
  expand(): void {
    this.expanded = true;
    this.drawer.classList.remove('curator-collapsed');
    this.drawer.classList.add('curator-expanded');
    this.drawer.style.height = `${this.drawerHeight}px`;
    document.documentElement.style.setProperty('--drawer-h', `${this.drawerHeight}px`);
    this.input.focus();
  }

  /** Collapse the drawer. */
  collapse(): void {
    this.expanded = false;
    this.drawer.classList.remove('curator-expanded');
    this.drawer.classList.add('curator-collapsed');
    this.drawer.style.height = '';
    document.documentElement.style.setProperty('--drawer-h', '42px');
    this.hideAutocomplete();
    this.input.blur();
  }

  /** Open the drawer and optionally enter command mode. */
  open(enterCommandMode = false): void {
    if (!this.expanded) this.expand();
    if (enterCommandMode && this.input.value === '') {
      this.input.value = '/';
      this.enterCommandMode();
      this.handleInput();
    }
    this.input.focus();
  }

  /** Whether the curator input is currently focused. */
  isInputFocused(): boolean {
    return this.inputFocused;
  }

  /* ================================================================ */
  /*  Tab switching                                                    */
  /* ================================================================ */

  private switchTab(tab: string): void {
    this.drawer.querySelectorAll('.curator-tab').forEach((b) => b.classList.remove('active'));
    this.drawer
      .querySelector(`.curator-tab[data-tab="${tab}"]`)
      ?.classList.add('active');
    this.drawer.querySelectorAll('.curator-panel').forEach((p) => p.classList.remove('active'));
    const panelId = tab === 'chat' ? 'curator-chat' : 'curator-issues';
    document.getElementById(panelId)?.classList.add('active');
  }

  /* ================================================================ */
  /*  Input handling                                                   */
  /* ================================================================ */

  private handleInput(): void {
    const val = this.input.value;

    if (val.startsWith('/')) {
      if (!this.commandMode) this.enterCommandMode();
      this.showAutocomplete(val);
    } else {
      if (this.commandMode) this.exitCommandMode();
      this.hideAutocomplete();
    }
  }

  private handleKeydown(e: KeyboardEvent): void {
    /* Autocomplete navigation */
    if (this.autocompleteEl) {
      if (e.key === 'ArrowDown') {
        e.preventDefault();
        this.moveAutocomplete(1);
        return;
      }
      if (e.key === 'ArrowUp') {
        e.preventDefault();
        this.moveAutocomplete(-1);
        return;
      }
      if (e.key === 'Tab') {
        e.preventDefault();
        this.acceptAutocomplete();
        return;
      }
    }

    /* History navigation (when no autocomplete shown) */
    if (e.key === 'ArrowUp' && !this.autocompleteEl) {
      e.preventDefault();
      this.navigateHistory(-1);
      return;
    }
    if (e.key === 'ArrowDown' && !this.autocompleteEl) {
      e.preventDefault();
      this.navigateHistory(1);
      return;
    }

    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      this.submit();
      return;
    }

    if (e.key === 'Escape') {
      e.preventDefault();
      this.hideAutocomplete();
      if (this.commandMode) {
        this.input.value = '';
        this.exitCommandMode();
      }
      this.collapse();
    }
  }

  private submit(): void {
    const raw = this.input.value.trim();
    if (!raw) return;

    this.inputHistory.push(raw);
    this.historyIdx = -1;
    this.input.value = '';
    this.exitCommandMode();
    this.hideAutocomplete();

    /* Auto-expand drawer when submitting a message */
    if (!this.expanded) this.expand();

    if (raw.startsWith('/')) {
      this.addUserMessage(raw);
      void this.executeCommand(raw);
    } else {
      void this.sendNaturalLanguage(raw);
    }
  }

  /* ================================================================ */
  /*  Command mode visual                                              */
  /* ================================================================ */

  private enterCommandMode(): void {
    this.commandMode = true;
    this.input.classList.add('chat-input-command-mode');
  }

  private exitCommandMode(): void {
    this.commandMode = false;
    this.input.classList.remove('chat-input-command-mode');
  }

  /* ================================================================ */
  /*  Autocomplete                                                     */
  /* ================================================================ */

  private showAutocomplete(val: string): void {
    const items = this.computeAutocomplete(val);
    if (items.length === 0) {
      this.hideAutocomplete();
      return;
    }

    if (!this.autocompleteEl) {
      this.autocompleteEl = document.createElement('div');
      this.autocompleteEl.className = 'curator-autocomplete';
      const inputBar = this.drawer.querySelector('.curator-input-bar')!;
      (inputBar as HTMLElement).style.position = 'relative';
      inputBar.appendChild(this.autocompleteEl);
    }

    this.autocompleteIdx = 0;
    this.autocompleteEl.innerHTML = items
      .map(
        (it, i) =>
          `<div class="curator-autocomplete-item${i === 0 ? ' selected' : ''}" data-value="${escAttr(it.value)}">` +
          `<span class="ac-cmd">${escHtml(it.label)}</span>` +
          (it.hint ? `<span class="ac-hint">${escHtml(it.hint)}</span>` : '') +
          `</div>`,
      )
      .join('');

    /* Click to accept */
    this.autocompleteEl.querySelectorAll('.curator-autocomplete-item').forEach((el) => {
      el.addEventListener('mousedown', (ev) => {
        ev.preventDefault();
        const v = (el as HTMLElement).dataset.value!;
        this.input.value = v;
        this.hideAutocomplete();
        this.input.focus();
        this.handleInput();
      });
    });
  }

  private computeAutocomplete(
    val: string,
  ): Array<{ label: string; hint: string; value: string }> {
    const parts = val.slice(1).split(/\s+/);
    const cmdPart = parts[0] ?? '';

    /* Still typing the command name */
    if (parts.length <= 1) {
      return SLASH_COMMANDS.filter((c) => c.name.startsWith(cmdPart.toLowerCase()))
        .slice(0, 8)
        .map((c) => ({
          label: `/${c.name}`,
          hint: c.hint,
          value: `/${c.name} `,
        }));
    }

    const cmd = cmdPart.toLowerCase();
    const lastPart = (parts[parts.length - 1] ?? '').toLowerCase();
    const prefix = parts.slice(0, -1).join(' ');

    /* /type -- autocomplete type names */
    if (cmd === 'type') {
      return NODE_TYPES.filter((t) => t.startsWith(lastPart))
        .slice(0, 8)
        .map((t) => ({
          label: t,
          hint: '',
          value: `/${prefix} ${t}`,
        }));
    }

    /* /link or /unlink -- second arg is relation */
    if ((cmd === 'link' || cmd === 'unlink') && parts.length === 3) {
      return RELATION_TYPES.filter((r) => r.startsWith(lastPart))
        .slice(0, 8)
        .map((r) => ({
          label: r,
          hint: '',
          value: `/${prefix} ${r} `,
        }));
    }

    /* Default: fuzzy-match node IDs */
    return this.fuzzyMatchNodes(lastPart)
      .slice(0, 8)
      .map((id) => ({
        label: shortLabel(id),
        hint: id,
        value: `/${prefix} ${id} `,
      }));
  }

  private fuzzyMatchNodes(query: string): string[] {
    if (!query) return this.nodeIds.slice(0, 8);
    const q = query.toLowerCase();
    return this.nodeIds
      .filter((id) => id.toLowerCase().includes(q))
      .sort((a, b) => {
        const aIdx = a.toLowerCase().indexOf(q);
        const bIdx = b.toLowerCase().indexOf(q);
        return aIdx - bIdx;
      });
  }

  private moveAutocomplete(dir: number): void {
    if (!this.autocompleteEl) return;
    const items = this.autocompleteEl.querySelectorAll('.curator-autocomplete-item');
    if (items.length === 0) return;
    items[this.autocompleteIdx]?.classList.remove('selected');
    this.autocompleteIdx = (this.autocompleteIdx + dir + items.length) % items.length;
    items[this.autocompleteIdx]?.classList.add('selected');
    (items[this.autocompleteIdx] as HTMLElement).scrollIntoView({ block: 'nearest' });
  }

  private acceptAutocomplete(): void {
    if (!this.autocompleteEl) return;
    const items = this.autocompleteEl.querySelectorAll('.curator-autocomplete-item');
    const sel = items[this.autocompleteIdx] as HTMLElement | undefined;
    if (sel) {
      this.input.value = sel.dataset.value ?? this.input.value;
      this.hideAutocomplete();
      this.input.focus();
      this.handleInput();
    }
  }

  hideAutocomplete(): void {
    if (this.autocompleteEl) {
      this.autocompleteEl.remove();
      this.autocompleteEl = null;
      this.autocompleteIdx = -1;
    }
  }

  /* ================================================================ */
  /*  Command execution                                                */
  /* ================================================================ */

  private async executeCommand(raw: string): Promise<void> {
    const parts = raw.slice(1).trim().split(/\s+/);
    const cmd = parts[0]?.toLowerCase() ?? '';
    const args = parts.slice(1);

    try {
      switch (cmd) {
        case 'tag':
          await this.cmdTag(args);
          break;
        case 'untag':
          await this.cmdUntag(args);
          break;
        case 'type':
          await this.cmdType(args);
          break;
        case 'merge':
          await this.cmdMerge(args);
          break;
        case 'delete':
          await this.cmdDelete();
          break;
        case 'link':
          await this.cmdLink(args);
          break;
        case 'unlink':
          await this.cmdUnlink(args);
          break;
        case 'verify':
          await this.loadSuggestions();
          this.addCommandResult('Health check complete. See Issues tab.', true);
          this.switchTab('issues');
          break;
        default:
          this.addCommandResult(`Unknown command: /${cmd}`, false);
      }
    } catch (err) {
      this.addCommandResult(`Error: ${(err as Error).message}`, false);
    }
  }

  private async cmdTag(args: string[]): Promise<void> {
    const tagName = args[0];
    if (!tagName) {
      this.addCommandResult('Usage: /tag <name>', false);
      return;
    }
    const targets = this.selectedNodes.length > 0 ? this.selectedNodes : [];
    if (targets.length === 0) {
      this.addCommandResult(
        'No nodes selected. Select nodes first or specify a node ID.',
        false,
      );
      return;
    }
    for (const id of targets) {
      await this.apiPost(`/api/node/${encodeURIComponent(id)}/tag`, { tag: tagName });
    }
    this.addCommandResult(
      `Tagged ${targets.length} node${targets.length > 1 ? 's' : ''} with \`${tagName}\``,
      true,
    );
    this.reloadGraph();
  }

  private async cmdUntag(args: string[]): Promise<void> {
    const tagName = args[0];
    if (!tagName) {
      this.addCommandResult('Usage: /untag <name>', false);
      return;
    }
    const targets = this.selectedNodes.length > 0 ? this.selectedNodes : [];
    if (targets.length === 0) {
      this.addCommandResult('No nodes selected.', false);
      return;
    }
    for (const id of targets) {
      await this.apiDelete(
        `/api/node/${encodeURIComponent(id)}/tag/${encodeURIComponent(tagName)}`,
      );
    }
    this.addCommandResult(
      `Removed tag \`${tagName}\` from ${targets.length} node${targets.length > 1 ? 's' : ''}`,
      true,
    );
    this.reloadGraph();
  }

  private async cmdType(args: string[]): Promise<void> {
    const newType = args[0];
    if (!newType) {
      this.addCommandResult('Usage: /type <type>', false);
      return;
    }
    const targets = this.selectedNodes.length > 0 ? this.selectedNodes : [];
    if (targets.length === 0) {
      this.addCommandResult('No nodes selected.', false);
      return;
    }
    for (const id of targets) {
      await this.apiPut(`/api/node/${encodeURIComponent(id)}`, { type: newType });
    }
    this.addCommandResult(
      `Changed type to \`${newType}\` for ${targets.length} node${targets.length > 1 ? 's' : ''}`,
      true,
    );
    this.reloadGraph();
  }

  private async cmdMerge(args: string[]): Promise<void> {
    if (args.length < 2) {
      this.addCommandResult('Usage: /merge <nodeA> <nodeB>', false);
      return;
    }
    const [a, b] = args;
    await this.apiPost('/api/merge', { target: a, source: b });
    this.addCommandResult(`Merged \`${b}\` into \`${a}\``, true);
    this.reloadGraph();
  }

  private async cmdDelete(): Promise<void> {
    const targets = this.selectedNodes.length > 0 ? [...this.selectedNodes] : [];
    if (targets.length === 0) {
      this.addCommandResult('No nodes selected for deletion.', false);
      return;
    }
    /* Show confirmation in chat */
    const names = targets.map((t) => shortLabel(t)).join(', ');
    this.addSystemMessage(
      `Delete ${targets.length} node${targets.length > 1 ? 's' : ''}? (${names})`,
      [
        {
          label: 'Confirm',
          onClick: async () => {
            for (const id of targets) {
              await this.apiDelete(`/api/node/${encodeURIComponent(id)}`);
            }
            this.addCommandResult(
              `Deleted ${targets.length} node${targets.length > 1 ? 's' : ''}`,
              true,
            );
            this.reloadGraph();
          },
        },
        {
          label: 'Cancel',
          onClick: () => {
            this.addCommandResult('Deletion cancelled.', false);
          },
        },
      ],
    );
  }

  private async cmdLink(args: string[]): Promise<void> {
    if (args.length < 3) {
      this.addCommandResult('Usage: /link <source> <relation> <target>', false);
      return;
    }
    const [source, relation, target] = args;
    await this.apiPost('/api/edge', { source, relation, target });
    this.addCommandResult(`Created edge: ${source} --${relation}--> ${target}`, true);
    this.reloadGraph();
  }

  private async cmdUnlink(args: string[]): Promise<void> {
    if (args.length < 3) {
      this.addCommandResult('Usage: /unlink <source> <relation> <target>', false);
      return;
    }
    const [source, relation, target] = args;
    await this.apiDelete(
      `/api/edge?source=${encodeURIComponent(source)}&relation=${encodeURIComponent(relation)}&target=${encodeURIComponent(target)}`,
    );
    this.addCommandResult(`Removed edge: ${source} --${relation}--> ${target}`, true);
    this.reloadGraph();
  }

  /* ================================================================ */
  /*  Natural Language Chat                                            */
  /* ================================================================ */

  async sendNaturalLanguage(message: string): Promise<void> {
    this.addUserMessage(message);

    const thinkingEl = this.addThinkingIndicator();

    try {
      const response = await fetch('/api/chat', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          message,
          history: this.getRecentHistory(10),
          selected_nodes: this.selectedNodes,
          namespace: this.currentNamespace,
          session_id: this.sessionId,
        }),
      });

      thinkingEl.remove();

      if (!response.ok) {
        const errText =
          response.status === 404 || response.status === 501
            ? 'LLM not configured -- slash commands are available. Set MARMOT_LLM_KEY to enable AI.'
            : `Chat request failed (${response.status})`;
        this.addSystemMessage(errText);
        return;
      }

      const data = (await response.json()) as ChatResponse;
      this.addSystemMessage(data.message.content);

      if (data.message.actions) {
        for (const action of data.message.actions) {
          this.processAction(action);
        }
      }
    } catch {
      thinkingEl.remove();
      this.addSystemMessage(
        'LLM not configured -- slash commands are available. Set MARMOT_LLM_KEY to enable AI.',
      );
    }
  }

  /* ================================================================ */
  /*  Message rendering                                                */
  /* ================================================================ */

  addUserMessage(text: string): void {
    const msg: ChatMessage = { role: 'user', content: text, timestamp: Date.now() };
    this.chatHistory.push(msg);

    const el = document.createElement('div');
    el.className = 'chat-message chat-message-user';
    el.textContent = text;
    this.messagesEl.appendChild(el);
    this.scrollToBottom();
  }

  addSystemMessage(
    text: string,
    actions?: Array<{ label: string; onClick: () => void }>,
  ): void {
    const msg: ChatMessage = { role: 'system', content: text, timestamp: Date.now() };
    this.chatHistory.push(msg);

    const el = document.createElement('div');
    el.className = 'chat-message chat-message-system';
    el.innerHTML = this.renderMessageWithNodeRefs(text);
    this.messagesEl.appendChild(el);

    if (actions && actions.length > 0) {
      const bar = document.createElement('div');
      bar.className = 'chat-message-actions';
      for (const a of actions) {
        const btn = document.createElement('button');
        btn.textContent = a.label;
        btn.addEventListener('click', () => {
          void a.onClick();
          bar.querySelectorAll('button').forEach((b) => {
            (b as HTMLButtonElement).disabled = true;
          });
        });
        bar.appendChild(btn);
      }
      el.appendChild(bar);
    }

    this.scrollToBottom();
  }

  addCommandResult(text: string, success: boolean): void {
    const el = document.createElement('div');
    el.className = `chat-message chat-message-command ${success ? 'success' : 'error'}`;
    el.innerHTML = this.renderMessageWithNodeRefs(text);
    this.messagesEl.appendChild(el);
    this.scrollToBottom();
  }

  private addThinkingIndicator(): HTMLElement {
    const el = document.createElement('div');
    el.className = 'chat-message chat-thinking';
    el.innerHTML =
      '<span class="thinking-dot"></span><span class="thinking-dot"></span><span class="thinking-dot"></span>';
    this.messagesEl.appendChild(el);
    this.scrollToBottom();
    return el;
  }

  /** Render node IDs in backticks as clickable pills. */
  private renderMessageWithNodeRefs(text: string): string {
    const escaped = escHtml(text);
    return escaped.replace(/`([a-zA-Z0-9_\-\/\.]+)`/g, (_match, id: string) => {
      if (this.nodeIds.includes(id)) {
        return `<span class="node-ref" data-node-id="${escAttr(id)}">${escHtml(shortLabel(id))}</span>`;
      }
      return `<code>${escHtml(id)}</code>`;
    });
  }

  private scrollToBottom(): void {
    this.messagesEl.scrollTop = this.messagesEl.scrollHeight;
  }

  /* ================================================================ */
  /*  Issues / Suggestions                                             */
  /* ================================================================ */

  async loadSuggestions(): Promise<void> {
    try {
      const res = await fetch(
        `/api/verify/${encodeURIComponent(this.currentNamespace)}`,
      );
      if (!res.ok) {
        this.badge.hidden = true;
        return;
      }
      const data = await res.json();
      const suggestions: Suggestion[] = data.issues ?? data.suggestions ?? [];

      /* Update badge */
      if (suggestions.length > 0) {
        this.badge.textContent = String(suggestions.length);
        this.badge.hidden = false;
      } else {
        this.badge.hidden = true;
      }

      /* Render issue cards */
      this.issuesList.innerHTML = '';

      /* Progress bar */
      const totalNodes = this.nodeIds.length || 1;
      const pct = Math.round(
        ((totalNodes - suggestions.length) / totalNodes) * 100,
      );
      const progress = document.createElement('div');
      progress.className = 'issues-progress';
      progress.innerHTML =
        `<div class="issues-progress-label">Your graph is ${pct}% curated</div>` +
        `<div class="issues-progress-bar"><div class="issues-progress-fill" style="width:${pct}%"></div></div>`;
      this.issuesList.appendChild(progress);

      for (const s of suggestions) {
        this.issuesList.appendChild(this.renderSuggestion(s));
      }
    } catch {
      this.badge.hidden = true;
    }
  }

  private renderSuggestion(s: Suggestion): HTMLElement {
    const card = document.createElement('div');
    card.className = 'issue-card';

    const typeLabel =
      s.type === 'orphan'
        ? 'Orphan node'
        : s.type === 'missing_type'
          ? 'Missing type'
          : s.type === 'duplicate'
            ? 'Possible duplicate'
            : s.type === 'stale'
              ? 'Stale source'
              : 'Untagged cluster';

    card.innerHTML =
      `<div class="issue-card-type">${escHtml(typeLabel)}</div>` +
      `<div class="issue-card-message">${escHtml(s.message)}</div>`;

    const actions = document.createElement('div');
    actions.className = 'issue-card-actions';

    if (s.type === 'orphan') {
      actions.appendChild(
        this.makeIssueBtn('Delete', 'danger', async () => {
          for (const id of s.node_ids) {
            await this.apiDelete(`/api/node/${encodeURIComponent(id)}`);
          }
          card.remove();
          this.reloadGraph();
        }),
      );
      actions.appendChild(
        this.makeIssueBtn('Keep', 'neutral', () => {
          card.remove();
        }),
      );
    } else if (s.type === 'missing_type') {
      actions.appendChild(
        this.makeIssueBtn('Set type...', 'positive', () => {
          this.open(true);
          this.input.value = '/type ';
          this.handleInput();
        }),
      );
      actions.appendChild(
        this.makeIssueBtn('Dismiss', 'neutral', () => {
          card.remove();
        }),
      );
    } else {
      actions.appendChild(
        this.makeIssueBtn('Dismiss', 'neutral', () => {
          card.remove();
        }),
      );
    }

    card.appendChild(actions);
    return card;
  }

  private makeIssueBtn(
    label: string,
    style: 'danger' | 'neutral' | 'positive',
    onClick: () => void,
  ): HTMLButtonElement {
    const btn = document.createElement('button');
    btn.textContent = label;
    btn.className = `issue-btn-${style}`;
    btn.addEventListener('click', () => void onClick());
    return btn;
  }

  /* ================================================================ */
  /*  Multi-select & context                                           */
  /* ================================================================ */

  setSelectedNodes(nodeIds: string[]): void {
    this.selectedNodes = [...nodeIds];
    this.updatePlaceholder();
    this.updateContextHint();
  }

  updatePlaceholder(): void {
    if (this.selectedNodes.length === 0) {
      this.input.placeholder = 'Ask about your graph, or type / for commands...';
    } else if (this.selectedNodes.length === 1) {
      this.input.placeholder = `Ask about ${shortLabel(this.selectedNodes[0])}...`;
    } else {
      this.input.placeholder = `Ask about ${this.selectedNodes.length} selected nodes...`;
    }
  }

  /** Show context hints (edge count, tags, type) below the input. */
  private updateContextHint(): void {
    if (!this.graphData || this.selectedNodes.length === 0) {
      this.hintEl.textContent = '';
      return;
    }

    const hints: string[] = [];

    for (const nodeId of this.selectedNodes.slice(0, 3)) {
      const node = this.graphData.nodes.find((n) => n.id === nodeId);
      if (!node) continue;

      const parts: string[] = [];
      const incoming = this.graphData.edges.filter((e) => e.target === nodeId).length;
      parts.push(`${incoming} incoming`);
      parts.push(`Type: ${node.type}`);
      if (node.tags && node.tags.length > 0) {
        parts.push(`Tagged: ${node.tags.join(', ')}`);
      }
      hints.push(`${shortLabel(nodeId)}: ${parts.join(' \u00B7 ')}`);
    }

    if (this.selectedNodes.length > 3) {
      hints.push(`+${this.selectedNodes.length - 3} more`);
    }

    this.hintEl.textContent = hints.join('  |  ');
  }

  /* ================================================================ */
  /*  @mention injection                                               */
  /* ================================================================ */

  private injectMention(nodeId: string): void {
    if (!this.expanded) return;

    const cursorPos = this.input.selectionStart ?? this.input.value.length;
    const before = this.input.value.substring(0, cursorPos);
    const after = this.input.value.substring(cursorPos);

    const prefix = before.length > 0 && !before.endsWith(' ') ? ' ' : '';
    const mention = `${prefix}@${nodeId} `;

    this.input.value = before + mention + after;
    this.input.focus();
    const newPos = cursorPos + mention.length;
    this.input.setSelectionRange(newPos, newPos);

    if (!this.selectedNodes.includes(nodeId)) {
      this.selectedNodes.push(nodeId);
      this.updateContextHint();
    }
  }

  /* ================================================================ */
  /*  History navigation                                               */
  /* ================================================================ */

  private getRecentHistory(
    count: number,
  ): Array<{ role: string; content: string }> {
    return this.chatHistory.slice(-count).map((m) => ({
      role: m.role,
      content: m.content,
    }));
  }

  private navigateHistory(dir: number): void {
    if (this.inputHistory.length === 0) return;
    if (this.historyIdx === -1 && dir === -1) {
      this.historyIdx = this.inputHistory.length - 1;
    } else {
      this.historyIdx = Math.max(
        0,
        Math.min(this.inputHistory.length - 1, this.historyIdx + dir),
      );
    }
    this.input.value = this.inputHistory[this.historyIdx] ?? '';
    this.handleInput();
  }

  /* ================================================================ */
  /*  Action processing (from LLM responses)                           */
  /* ================================================================ */

  private processAction(action: ChatAction): void {
    switch (action.type) {
      case 'highlight': {
        const nodeId = action.node_id as string;
        if (nodeId) {
          document.dispatchEvent(
            new CustomEvent('curator-highlight-node', { detail: { nodeId } }),
          );
        }
        break;
      }
      case 'select': {
        const nodeIds = action.node_ids as string[];
        if (nodeIds) {
          this.setSelectedNodes(nodeIds);
        }
        break;
      }
      default:
        console.log('[curator] Unknown action type:', action.type);
    }
  }

  /* ================================================================ */
  /*  Drag resize                                                      */
  /* ================================================================ */

  private initDragResize(): void {
    const handle = this.drawer.querySelector('.curator-handle')!;
    let startY = 0;
    let startH = 0;
    let dragging = false;

    const onMove = (e: MouseEvent) => {
      if (!dragging) return;
      const delta = startY - e.clientY;
      const newH = Math.max(
        160,
        Math.min(window.innerHeight * 0.4, startH + delta),
      );
      this.drawerHeight = newH;
      this.drawer.style.height = `${newH}px`;
      document.documentElement.style.setProperty('--drawer-h', `${newH}px`);
    };

    const onUp = () => {
      dragging = false;
      document.removeEventListener('mousemove', onMove);
      document.removeEventListener('mouseup', onUp);
      document.body.style.cursor = '';
      document.body.style.userSelect = '';
    };

    handle.addEventListener('mousedown', (e: Event) => {
      const me = e as MouseEvent;
      if (!this.expanded) {
        this.expand();
        return;
      }
      dragging = true;
      startY = me.clientY;
      startH = this.drawer.getBoundingClientRect().height;
      document.body.style.cursor = 'row-resize';
      document.body.style.userSelect = 'none';
      document.addEventListener('mousemove', onMove);
      document.addEventListener('mouseup', onUp);
      me.preventDefault();
    });
  }

  /* ================================================================ */
  /*  Graph reload helper                                              */
  /* ================================================================ */

  private reloadGraph(): void {
    document.dispatchEvent(new CustomEvent('curator-reload-graph'));
  }

  /* ================================================================ */
  /*  API helpers                                                      */
  /* ================================================================ */

  private async apiPost(
    url: string,
    body: Record<string, unknown>,
  ): Promise<unknown> {
    const res = await fetch(url, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    });
    if (!res.ok) throw new Error(await res.text());
    /* Some endpoints return empty 204 */
    const text = await res.text();
    return text ? JSON.parse(text) : {};
  }

  private async apiPut(
    url: string,
    body: Record<string, unknown>,
  ): Promise<unknown> {
    const res = await fetch(url, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    });
    if (!res.ok) throw new Error(await res.text());
    const text = await res.text();
    return text ? JSON.parse(text) : {};
  }

  private async apiDelete(url: string): Promise<void> {
    const res = await fetch(url, { method: 'DELETE' });
    if (!res.ok) throw new Error(await res.text());
  }
}

/* ------------------------------------------------------------------ */
/*  Helpers                                                            */
/* ------------------------------------------------------------------ */

function shortLabel(id: string): string {
  const parts = id.split('/');
  return parts[parts.length - 1];
}

function escHtml(s: string): string {
  return s
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;');
}

function escAttr(s: string): string {
  return s.replace(/"/g, '&quot;').replace(/'/g, '&#39;');
}
