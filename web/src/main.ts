import { fetchGraph, fetchNamespaces } from './api';
import { GraphView } from './graph-view';
import { renderLegend } from './legend';
import { DetailPanel } from './detail-panel';
import { Filters } from './filters';
import { Search } from './search';
import { initKeyboard } from './keyboard';
import type { GraphResponse } from './types';

let currentNamespace = 'default';
let currentData: GraphResponse | null = null;
let graphView: GraphView | null = null;

/* Module-level interaction components (initialized in init) */
let detailPanel: DetailPanel;
let filters: Filters;
let search: Search;

async function init(): Promise<void> {
  const select = document.getElementById('namespace-select') as HTMLSelectElement;

  /* Instantiate the D3 graph view */
  graphView = new GraphView();

  /* Render the legend */
  renderLegend();

  /* ── Interaction modules ──────────────────────────────────────── */

  detailPanel = new DetailPanel();

  filters = new Filters(() => {
    graphView?.filterByTypes(filters.getActiveTypes());
    graphView?.filterByEdgeClass(filters.getEdgeClass());
  });

  search = new Search((nodeId: string) => {
    highlightNode(nodeId);
  });

  initKeyboard({
    onSearch: () => search.focus(),
    onEscape: () => {
      search.clear();
      detailPanel.hide();
    },
    onRefresh: () => {
      void loadGraph();
    },
    onFitView: () => {
      // TODO: call graphView.fitToViewport() when implemented
      console.log('Fit to viewport (not yet implemented)');
    },
  });

  /* ── Custom events from GraphView ─────────────────────────────── */

  // GraphView dispatches node-selected on #graph-svg with a SimNode detail.
  // We look up the full APINode from currentData to populate the detail panel.
  document.getElementById('graph-svg')?.addEventListener('node-selected', ((
    e: CustomEvent,
  ) => {
    const simNode = e.detail as { id: string } | null;
    if (!simNode || !currentData) return;
    const apiNode = currentData.nodes.find((n) => n.id === simNode.id);
    if (apiNode) detailPanel.show(apiNode);
  }) as EventListener);

  document.getElementById('graph-svg')?.addEventListener('node-deselected', () => {
    detailPanel.hide();
  });

  // Fired when clicking an edge target in the detail panel
  document.addEventListener('edge-navigate', ((e: CustomEvent<string>) => {
    highlightNode(e.detail);
  }) as EventListener);

  /* ── Heat toggle ──────────────────────────────────────────────── */
  const heatToggle = document.getElementById('heat-toggle') as HTMLInputElement;
  heatToggle.addEventListener('change', () => {
    graphView?.setHeatOverlay(heatToggle.checked, currentData?.heat_pairs);
  });

  /* ── Superseded toggle reloads graph ──────────────────────────── */
  document.getElementById('superseded-toggle')?.addEventListener('change', () => {
    void loadGraph();
  });

  /* ── Populate namespace selector ──────────────────────────────── */
  try {
    const namespaces = await fetchNamespaces();
    namespaces.forEach((ns) => {
      const opt = document.createElement('option');
      opt.value = ns.name;
      opt.textContent = `${ns.name} (${ns.node_count})`;
      select.appendChild(opt);
    });
    if (namespaces.length > 0) {
      currentNamespace = namespaces[0].name;
      select.value = currentNamespace;
    }
  } catch {
    // API not available yet -- add a default option
    const opt = document.createElement('option');
    opt.value = 'default';
    opt.textContent = 'default';
    select.appendChild(opt);
  }

  select.addEventListener('change', () => {
    currentNamespace = select.value;
    void loadGraph();
  });

  document.getElementById('refresh-btn')?.addEventListener('click', () => {
    void loadGraph();
  });

  /* Load initial graph */
  await loadGraph();
}

/* ------------------------------------------------------------------ */
/*  Graph loading                                                      */
/* ------------------------------------------------------------------ */

async function loadGraph(): Promise<void> {
  const includeSuperseded =
    (document.getElementById('superseded-toggle') as HTMLInputElement)?.checked || false;
  try {
    currentData = await fetchGraph(currentNamespace, includeSuperseded);
    console.log(
      'Graph loaded:',
      currentData.node_count,
      'nodes,',
      currentData.edge_count,
      'edges',
    );

    /* Update filter chips with types actually present in the data */
    const typesInGraph = [...new Set(currentData.nodes.map((n) => n.type))];
    filters.updateAvailableTypes(typesInGraph);

    /* Render graph */
    graphView?.update(currentData);
  } catch (err) {
    console.error('Failed to load graph:', err);
  }
}

/* ------------------------------------------------------------------ */
/*  Node highlighting                                                  */
/* ------------------------------------------------------------------ */

/**
 * Highlight (and optionally select) a node by its ID.
 * Used by search results and edge-navigate events.
 */
function highlightNode(nodeId: string): void {
  if (!currentData) return;

  const node = currentData.nodes.find((n) => n.id === nodeId);
  if (node) {
    detailPanel.show(node);
    graphView?.highlightNode(nodeId);
  }
}

/* Expose for potential external use */
export { currentData, currentNamespace };

document.addEventListener('DOMContentLoaded', () => {
  void init();
});
