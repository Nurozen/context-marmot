import { updateNode } from './api';
import type { APINode, APIEdge } from './types';
import { nodeColor } from './types';

/** Status-to-color mapping for status badges */
const STATUS_COLORS: Record<string, string> = {
  active: '#54A24B',
  superseded: '#999999',
  archived: '#E45756',
};

/**
 * Right-hand detail sidebar that displays full information about a selected node.
 * Dispatches `edge-navigate` CustomEvents when a user clicks an edge target.
 */
export class DetailPanel {
  private container: HTMLElement;
  private content: HTMLElement;
  private closeBtn: HTMLElement;
  private currentNodeId: string | null = null;

  constructor() {
    this.container = document.getElementById('detail-panel') as HTMLElement;
    this.content = document.getElementById('detail-content') as HTMLElement;
    this.closeBtn = document.getElementById('detail-close') as HTMLElement;

    this.closeBtn.addEventListener('click', () => this.hide());

    // Click-outside to close: clicks on #graph-container close the panel
    document.getElementById('graph-container')?.addEventListener('click', (e) => {
      // Only close if the click target is the container itself or SVG background
      const target = e.target as HTMLElement;
      if (
        this.isVisible() &&
        (target.id === 'graph-container' || target.id === 'graph-svg')
      ) {
        this.hide();
      }
    });
  }

  show(node: APINode): void {
    this.currentNodeId = node.id;
    this.content.innerHTML = '';

    // ── Header: ID + badges ──
    const header = document.createElement('div');
    header.className = 'detail-header';

    const nodeIdEl = document.createElement('div');
    nodeIdEl.className = 'node-id';
    nodeIdEl.textContent = node.id;
    header.appendChild(nodeIdEl);

    const badgeRow = document.createElement('div');
    badgeRow.style.marginBottom = '16px';

    // Type badge
    const typeBadge = document.createElement('span');
    typeBadge.className = 'badge';
    typeBadge.style.background = nodeColor(node.type);
    typeBadge.textContent = node.type;
    badgeRow.appendChild(typeBadge);

    // Status badge
    const statusBadge = document.createElement('span');
    statusBadge.className = 'badge';
    statusBadge.style.background = STATUS_COLORS[node.status] ?? '#666666';
    statusBadge.textContent = node.status;
    badgeRow.appendChild(statusBadge);

    header.appendChild(badgeRow);
    this.content.appendChild(header);

    // ── Staleness warning ──
    if (node.is_stale) {
      const staleWarning = document.createElement('div');
      staleWarning.className = 'detail-section';
      staleWarning.innerHTML =
        '<p style="color:var(--color-warning);font-weight:600;font-size:12px;">' +
        '&#x26A0; This node is stale &mdash; its source may have changed.</p>';
      this.content.appendChild(staleWarning);
    }

    // ── Namespace ──
    const nsSection = this.createSection('Namespace');
    const nsText = document.createElement('p');
    nsText.textContent = node.namespace;
    nsSection.appendChild(nsText);
    this.content.appendChild(nsSection);

    // ── Summary (editable) ──
    const summarySection = this.createSection('Summary');
    const summaryArea = document.createElement('textarea');
    this.styleTextarea(summaryArea, 3);
    summaryArea.value = node.summary;
    summarySection.appendChild(summaryArea);
    this.content.appendChild(summarySection);

    // ── Context (editable) ──
    const contextSection = this.createSection('Context');
    const contextArea = document.createElement('textarea');
    this.styleTextarea(contextArea, 6);
    contextArea.value = node.context;
    contextSection.appendChild(contextArea);
    this.content.appendChild(contextSection);

    // ── Source reference ──
    if (node.source) {
      const srcSection = this.createSection('Source');
      const pre = document.createElement('pre');
      pre.textContent =
        `${node.source.path}\n` +
        `Lines ${node.source.lines[0]}-${node.source.lines[1]}\n` +
        `Hash  ${node.source.hash}`;
      srcSection.appendChild(pre);
      this.content.appendChild(srcSection);
    }

    // ── Edges (grouped by relation) ──
    if (node.edges.length > 0) {
      const edgesSection = this.createSection(`Edges (${node.edges.length})`);
      const grouped = this.groupEdges(node.edges, node.id);
      const list = document.createElement('ul');
      list.className = 'edge-list';

      for (const [relation, edges] of grouped) {
        for (const edge of edges) {
          const targetId = edge.source === node.id ? edge.target : edge.source;
          const li = document.createElement('li');

          const relSpan = document.createElement('span');
          relSpan.className = 'edge-relation';
          relSpan.textContent = relation;
          li.appendChild(relSpan);

          const targetSpan = document.createElement('span');
          targetSpan.textContent = targetId;
          targetSpan.style.fontFamily = 'var(--font-mono)';
          targetSpan.style.fontSize = '11px';
          li.appendChild(targetSpan);

          li.addEventListener('click', () => {
            document.dispatchEvent(
              new CustomEvent<string>('edge-navigate', { detail: targetId }),
            );
          });

          list.appendChild(li);
        }
      }

      edgesSection.appendChild(list);
      this.content.appendChild(edgesSection);
    }

    // ── Temporal info ──
    if (node.valid_from || node.valid_until || node.superseded_by) {
      const temporalSection = this.createSection('Temporal');
      const items: string[] = [];
      if (node.valid_from) items.push(`Valid from: ${node.valid_from}`);
      if (node.valid_until) items.push(`Valid until: ${node.valid_until}`);
      if (node.superseded_by) items.push(`Superseded by: ${node.superseded_by}`);
      const pre = document.createElement('pre');
      pre.textContent = items.join('\n');
      temporalSection.appendChild(pre);
      this.content.appendChild(temporalSection);
    }

    // ── Save button ──
    const saveBtn = document.createElement('button');
    saveBtn.textContent = 'Save Changes';
    saveBtn.disabled = true;
    Object.assign(saveBtn.style, {
      marginTop: '12px',
      width: '100%',
      padding: '8px 0',
      fontSize: '12px',
      fontWeight: '600',
      color: 'var(--text-primary)',
      background: 'var(--accent)',
      border: 'none',
      borderRadius: 'var(--radius-md)',
      cursor: 'pointer',
      opacity: '0.5',
      transition: 'opacity var(--transition-fast)',
    });

    const enableSave = () => {
      saveBtn.disabled = false;
      saveBtn.style.opacity = '1';
    };

    summaryArea.addEventListener('input', enableSave);
    contextArea.addEventListener('input', enableSave);

    saveBtn.addEventListener('click', () => {
      if (!this.currentNodeId) return;
      saveBtn.disabled = true;
      saveBtn.style.opacity = '0.5';
      saveBtn.textContent = 'Saving...';

      void updateNode(this.currentNodeId, {
        summary: summaryArea.value,
        context: contextArea.value,
      })
        .then(() => {
          saveBtn.textContent = 'Saved';
          setTimeout(() => {
            saveBtn.textContent = 'Save Changes';
          }, 1500);
        })
        .catch(() => {
          saveBtn.textContent = 'Error — Retry';
          saveBtn.disabled = false;
          saveBtn.style.opacity = '1';
        });
    });

    this.content.appendChild(saveBtn);

    // Show the panel
    this.container.classList.remove('hidden');
  }

  hide(): void {
    this.container.classList.add('hidden');
    this.currentNodeId = null;
    document.dispatchEvent(new CustomEvent('node-deselected'));
  }

  isVisible(): boolean {
    return !this.container.classList.contains('hidden');
  }

  // ── Helpers ──────────────────────────────────────────────────

  private createSection(title: string): HTMLElement {
    const section = document.createElement('div');
    section.className = 'detail-section';
    const h4 = document.createElement('h4');
    h4.textContent = title;
    section.appendChild(h4);
    return section;
  }

  private styleTextarea(el: HTMLTextAreaElement, rows: number): void {
    el.rows = rows;
    Object.assign(el.style, {
      width: '100%',
      fontFamily: 'var(--font-sans)',
      fontSize: '12px',
      lineHeight: '1.6',
      color: 'var(--text-secondary)',
      background: 'var(--bg-tertiary)',
      border: '1px solid var(--border-subtle)',
      borderRadius: 'var(--radius-sm)',
      padding: '8px 10px',
      resize: 'vertical',
      outline: 'none',
    });
  }

  private groupEdges(
    edges: APIEdge[],
    nodeId: string,
  ): Map<string, APIEdge[]> {
    const grouped = new Map<string, APIEdge[]>();
    for (const edge of edges) {
      const key = edge.relation;
      const arr = grouped.get(key);
      if (arr) {
        arr.push(edge);
      } else {
        grouped.set(key, [edge]);
      }
    }
    // Sort each group so that outgoing edges (source === nodeId) come first
    for (const [, arr] of grouped) {
      arr.sort((a, b) => {
        const aOut = a.source === nodeId ? 0 : 1;
        const bOut = b.source === nodeId ? 0 : 1;
        return aOut - bOut;
      });
    }
    return grouped;
  }
}
