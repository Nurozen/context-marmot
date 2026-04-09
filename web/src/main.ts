import { fetchGraph, fetchGraphAll, fetchNamespaces } from './api';
import { GraphView } from './graph-view';
import { renderLegend } from './legend';
import { DetailPanel } from './detail-panel';
import { Filters } from './filters';
import { Search } from './search';
import { initKeyboard } from './keyboard';
import { IssuesPanel } from './issues';
import { Curator } from './curator';
import type { GraphResponse } from './types';

let currentNamespace = 'default';
let currentData: GraphResponse | null = null;
let graphView: GraphView | null = null;

/* Module-level interaction components (initialized in init) */
let detailPanel: DetailPanel;
let filters: Filters;
let search: Search;
let issuesPanel: IssuesPanel;
let curator: Curator;

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
    const activeTags = filters.getActiveTags();
    // Only apply tag filter if there are tags in the graph
    const allTags = currentData?.nodes.flatMap((n) => n.tags ?? []) ?? [];
    if (allTags.length > 0) {
      graphView?.filterByTags(activeTags);
    } else {
      graphView?.filterByTags(null);
    }
  });

  search = new Search((nodeId: string) => {
    highlightNode(nodeId);
  });

  /* ── Issues panel ────────────────────────────────────────────── */
  const issuesContainer = document.getElementById('issues-list')!;
  const issuesBadge = document.getElementById('issues-badge')!;
  issuesPanel = new IssuesPanel(issuesContainer, issuesBadge, async (cmd) => {
    console.log('[curator] executing command:', cmd);
    // TODO: route through curator command handler when available
  });

  /* ── Curator (chat + graph integration) ───────────────────────── */
  curator = new Curator();

  /* Highlight nodes from issue card pills or chat node-ref pills */
  document.addEventListener('curator-highlight-node', ((e: CustomEvent) => {
    const nodeId = (e.detail as { nodeId: string }).nodeId ?? e.detail;
    if (typeof nodeId === 'string') {
      highlightNode(nodeId);
      graphView?.pulseNode(nodeId);
    }
  }) as EventListener);

  /* Listen for curator requesting a graph reload (after mutations) */
  document.addEventListener('curator-reload-graph', () => {
    void loadGraph();
  });

  initKeyboard({
    onSearch: () => search.focus(),
    onEscape: () => {
      search.clear();
      detailPanel.hide();
      curator.collapse();
      graphView?.clearMultiSelect();
    },
    onRefresh: () => {
      void loadGraph();
    },
    onFitView: () => {
      // TODO: call graphView.fitToViewport() when implemented
      console.log('Fit to viewport (not yet implemented)');
    },
    onCuratorToggle: () => {
      curator.toggle();
    },
    onCuratorSlash: () => {
      curator.open(true);
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

  /* ── Group-by selector ────────────────────────────────────────── */
  const groupBySelect = document.getElementById('groupby-select') as HTMLSelectElement;
  groupBySelect.addEventListener('change', () => {
    graphView?.setGroupBy(groupBySelect.value as 'none' | 'type' | 'namespace' | 'tag' | 'folder');
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
    if (namespaces.length > 1) {
      const allOpt = document.createElement('option');
      allOpt.value = '_all';
      allOpt.textContent = `All namespaces (${namespaces.reduce((s, n) => s + n.node_count, 0)})`;
      select.prepend(allOpt);
    }
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

  /* ── Live-reload via SSE ───────────────────────────────────── */
  const evtSource = new EventSource('/api/events');
  evtSource.addEventListener('graph-changed', () => {
    console.log('[live-reload] graph changed on disk, refreshing…');
    void loadGraph();
  });
  evtSource.addEventListener('open', () => {
    // On reconnect, reload graph in case changes were missed
    void loadGraph();
  });
  evtSource.onerror = () => {
    console.warn('[live-reload] SSE connection lost, will auto-reconnect');
  };
}

/* ------------------------------------------------------------------ */
/*  Graph loading                                                      */
/* ------------------------------------------------------------------ */

async function loadGraph(): Promise<void> {
  const includeSuperseded =
    (document.getElementById('superseded-toggle') as HTMLInputElement)?.checked || false;
  try {
    if (currentNamespace === '_all') {
      currentData = await fetchGraphAll(includeSuperseded);
    } else {
      currentData = await fetchGraph(currentNamespace, includeSuperseded);
    }
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

    /* Update tag filter chips */
    const tagsInGraph = [...new Set(currentData.nodes.flatMap((n) => n.tags ?? []))].sort();
    filters.updateAvailableTags(tagsInGraph);

    /* Render graph (update() auto-sets groupBy='namespace' for multi-ns data) */
    graphView?.update(currentData);

    /* Sync the group-by dropdown UI for the all-namespaces view */
    if (currentNamespace === '_all') {
      const groupBySelect = document.getElementById('groupby-select') as HTMLSelectElement;
      groupBySelect.value = 'namespace';
    }

    /* Update curator with fresh graph data */
    curator.updateGraphData(currentData, currentNamespace);

    /* Load curation suggestions after graph data is available */
    issuesPanel.load(currentNamespace === '_all' ? undefined : currentNamespace);
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
