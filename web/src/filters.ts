import { NODE_COLORS, DEFAULT_NODE_COLOR } from './types';

type EdgeClass = 'all' | 'structural' | 'behavioral';

/**
 * Filter controls that wire to the graph view.
 * Manages type-filter chips and edge-class radio buttons from the sidebar.
 */
export class Filters {
  private typeFilters: Map<string, boolean> = new Map();
  private edgeClassFilter: EdgeClass = 'all';
  private onFilterChange: () => void;
  private typeContainer: HTMLElement;

  constructor(onFilterChange: () => void) {
    this.onFilterChange = onFilterChange;
    this.typeContainer = document.getElementById('type-filters') as HTMLElement;

    // Seed with all known types (checked by default)
    const knownTypes = [
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
    this.renderTypeChips(knownTypes);

    // Wire existing edge-class radio buttons
    const edgeContainer = document.getElementById('edge-filters') as HTMLElement;
    const radios = edgeContainer.querySelectorAll<HTMLInputElement>(
      'input[type="radio"][name="edge-class"]',
    );
    for (const radio of radios) {
      radio.addEventListener('change', () => {
        if (radio.checked) {
          this.edgeClassFilter = radio.value as EdgeClass;
          this.onFilterChange();
        }
      });
    }
  }

  /**
   * Re-populate type chips based on types actually present in the loaded graph.
   * Preserves existing checked state where possible.
   */
  updateAvailableTypes(types: string[]): void {
    // Merge new types with existing, preserving check state
    const previousState = new Map(this.typeFilters);
    this.typeFilters.clear();

    for (const t of types) {
      this.typeFilters.set(t, previousState.get(t) ?? true);
    }

    this.renderTypeChips(types);
  }

  getActiveTypes(): Set<string> {
    const active = new Set<string>();
    for (const [type, enabled] of this.typeFilters) {
      if (enabled) active.add(type);
    }
    return active;
  }

  getEdgeClass(): EdgeClass {
    return this.edgeClassFilter;
  }

  // ── Private ──────────────────────────────────────────────────

  private renderTypeChips(types: string[]): void {
    this.typeContainer.innerHTML = '';

    for (const type of types) {
      // Ensure it's tracked
      if (!this.typeFilters.has(type)) {
        this.typeFilters.set(type, true);
      }

      const label = document.createElement('label');

      const dot = document.createElement('span');
      dot.className = 'color-dot';
      dot.style.background = NODE_COLORS[type] ?? DEFAULT_NODE_COLOR;

      const checkbox = document.createElement('input');
      checkbox.type = 'checkbox';
      checkbox.checked = this.typeFilters.get(type) ?? true;

      checkbox.addEventListener('change', () => {
        this.typeFilters.set(type, checkbox.checked);
        this.onFilterChange();
      });

      const text = document.createTextNode(type);

      label.appendChild(dot);
      label.appendChild(checkbox);
      label.appendChild(text);
      this.typeContainer.appendChild(label);
    }
  }
}
