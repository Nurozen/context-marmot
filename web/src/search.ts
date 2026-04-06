import { searchNodes } from './api';
import type { SearchResult } from './types';
import { nodeColor } from './types';

/**
 * Search component that provides a dropdown of matching nodes
 * below the toolbar search input, with debounced API calls.
 */
export class Search {
  private input: HTMLInputElement;
  private resultsContainer: HTMLElement;
  private debounceTimer: ReturnType<typeof setTimeout> | null = null;
  private onSelect: (nodeId: string) => void;
  private results: SearchResult[] = [];

  constructor(onSelect: (nodeId: string) => void) {
    this.input = document.getElementById('search-input') as HTMLInputElement;
    this.onSelect = onSelect;

    // Create dropdown container positioned below the search input
    this.resultsContainer = document.createElement('div');
    Object.assign(this.resultsContainer.style, {
      position: 'absolute',
      top: '100%',
      left: '0',
      right: '0',
      maxHeight: '320px',
      overflowY: 'auto',
      background: 'var(--bg-elevated)',
      border: '1px solid var(--border-subtle)',
      borderTop: 'none',
      borderRadius: '0 0 var(--radius-md) var(--radius-md)',
      zIndex: '300',
      display: 'none',
      boxShadow: '0 8px 24px rgba(0,0,0,0.4)',
    });

    // Position the parent of search input as relative anchor
    const searchParent = this.input.parentElement;
    if (searchParent) {
      searchParent.style.position = 'relative';
      searchParent.appendChild(this.resultsContainer);
    }

    // Debounced input handler
    this.input.addEventListener('input', () => {
      if (this.debounceTimer !== null) clearTimeout(this.debounceTimer);
      this.debounceTimer = setTimeout(() => {
        void this.performSearch(this.input.value.trim());
      }, 300);
    });

    // Enter selects first result
    this.input.addEventListener('keydown', (e: KeyboardEvent) => {
      if (e.key === 'Enter' && this.results.length > 0) {
        e.preventDefault();
        this.selectResult(this.results[0].node_id);
      }
      if (e.key === 'Escape') {
        this.clear();
        this.input.blur();
      }
    });

    // Click outside closes results
    document.addEventListener('click', (e: MouseEvent) => {
      if (
        !this.input.contains(e.target as Node) &&
        !this.resultsContainer.contains(e.target as Node)
      ) {
        this.hideResults();
      }
    });
  }

  focus(): void {
    this.input.focus();
    this.input.select();
  }

  clear(): void {
    this.input.value = '';
    this.hideResults();
  }

  private async performSearch(query: string): Promise<void> {
    if (query.length < 2) {
      this.hideResults();
      return;
    }
    try {
      const results = await searchNodes(query);
      this.showResults(results);
    } catch {
      this.hideResults();
    }
  }

  private showResults(results: SearchResult[]): void {
    this.results = results;
    this.resultsContainer.innerHTML = '';

    if (results.length === 0) {
      const empty = document.createElement('div');
      Object.assign(empty.style, {
        padding: '12px 14px',
        fontSize: '12px',
        color: 'var(--text-muted)',
      });
      empty.textContent = 'No results found';
      this.resultsContainer.appendChild(empty);
      this.resultsContainer.style.display = 'block';
      return;
    }

    for (const result of results) {
      const item = document.createElement('div');
      Object.assign(item.style, {
        display: 'flex',
        alignItems: 'center',
        gap: '8px',
        padding: '8px 14px',
        cursor: 'pointer',
        transition: 'background 120ms ease',
        borderBottom: '1px solid var(--border-subtle)',
      });

      item.addEventListener('mouseenter', () => {
        item.style.background = 'var(--bg-hover)';
      });
      item.addEventListener('mouseleave', () => {
        item.style.background = 'transparent';
      });

      // Type badge dot
      const dot = document.createElement('span');
      Object.assign(dot.style, {
        width: '8px',
        height: '8px',
        borderRadius: '50%',
        background: nodeColor(result.type),
        flexShrink: '0',
      });
      item.appendChild(dot);

      // Node ID
      const idSpan = document.createElement('span');
      Object.assign(idSpan.style, {
        fontFamily: 'var(--font-mono)',
        fontSize: '11px',
        color: 'var(--text-primary)',
        flexShrink: '0',
        maxWidth: '180px',
        overflow: 'hidden',
        textOverflow: 'ellipsis',
        whiteSpace: 'nowrap',
      });
      idSpan.textContent = result.node_id;
      item.appendChild(idSpan);

      // Score
      const scoreSpan = document.createElement('span');
      Object.assign(scoreSpan.style, {
        fontFamily: 'var(--font-mono)',
        fontSize: '10px',
        color: 'var(--text-muted)',
        flexShrink: '0',
      });
      scoreSpan.textContent = result.score.toFixed(2);
      item.appendChild(scoreSpan);

      // Truncated summary
      const summarySpan = document.createElement('span');
      Object.assign(summarySpan.style, {
        fontSize: '11px',
        color: 'var(--text-secondary)',
        overflow: 'hidden',
        textOverflow: 'ellipsis',
        whiteSpace: 'nowrap',
        flex: '1',
        minWidth: '0',
      });
      summarySpan.textContent =
        result.summary.length > 80
          ? result.summary.slice(0, 80) + '...'
          : result.summary;
      item.appendChild(summarySpan);

      item.addEventListener('click', () => {
        this.selectResult(result.node_id);
      });

      this.resultsContainer.appendChild(item);
    }

    this.resultsContainer.style.display = 'block';
  }

  private hideResults(): void {
    this.resultsContainer.style.display = 'none';
    this.results = [];
  }

  private selectResult(nodeId: string): void {
    this.onSelect(nodeId);
    this.clear();
    this.input.blur();
  }
}
