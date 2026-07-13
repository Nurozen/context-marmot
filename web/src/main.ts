import {
  fetchGraph,
  fetchGraphAll,
  fetchNamespaces,
  fetchWarrenGraph,
  fetchWarrens,
  refreshWarrenState,
} from './api';
import { GraphView } from './graph-view';
import type { GroupMode } from './graph-view';
import { renderLegend } from './legend';
import { DetailPanel } from './detail-panel';
import { Filters } from './filters';
import { Search } from './search';
import { initKeyboard } from './keyboard';
import { IssuesPanel } from './issues';
import { Curator } from './curator';
import { WarrenPanel } from './warren-panel';
import { showToast } from './toast';
import type { APIEdge, APINode, GraphResponse, WorkspaceWarren } from './types';

let currentNamespace = 'default';
let currentData: GraphResponse | null = null;
let graphView: GraphView | null = null;

/* Module-level interaction components (initialized in init) */
let detailPanel: DetailPanel;
let filters: Filters;
let search: Search;
let issuesPanel: IssuesPanel;
let curator: Curator;
let warrenPanel: WarrenPanel;

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

  search = new Search(
    (nodeId: string) => {
      highlightNode(nodeId);
    },
    () =>
      currentNamespace === '_all' || currentNamespace === '_warrens'
        ? undefined
        : currentNamespace,
  );

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

  /* Listen for curator requesting an issues refresh (e.g. /verify) */
  document.addEventListener('curator-refresh-issues', () => {
    void issuesPanel.load(issuesNamespaceFilter());
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
    graphView?.setGroupBy(groupBySelect.value as GroupMode);
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
    const warrenData = await fetchWarrens();
    graphView?.setLocalVaultId(warrenData.local_vault_id ?? '');
    const warrenEntries = Object.entries(warrenData.warrens ?? {}).sort(([a], [b]) =>
      a.localeCompare(b),
    );
    if (warrenEntries.length > 0 && select.options.length > 0) {
      const divider = document.createElement('option');
      divider.disabled = true;
      divider.textContent = 'Warrens';
      select.appendChild(divider);
    }
    for (const [warrenId, warren] of warrenEntries) {
      const opt = document.createElement('option');
      opt.value = `_warren/${warrenId}`;
      opt.textContent = warrenOptionLabel(warrenId, warren);
      select.appendChild(opt);
    }
    if (warrenEntries.length > 0) {
      // Aggregate view: local graph + every active warren project.
      const allWarrensOpt = document.createElement('option');
      allWarrensOpt.value = '_warrens';
      allWarrensOpt.textContent = allWarrensOptionLabel(warrenData.warrens ?? {});
      select.appendChild(allWarrensOpt);
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

  /* ── Warren management panel ──────────────────────────────────── */
  // After any panel-driven state change (mount/unmount/refresh) reload the
  // graph AND re-label the warren selector options, whose "(N active,
  // M identity)" counts would otherwise go stale.
  warrenPanel = new WarrenPanel(async () => {
    await loadGraph();
    await refreshWarrenSelectorLabels();
  });
  void warrenPanel.init();

  document.getElementById('refresh-btn')?.addEventListener('click', () => {
    void (async () => {
      // For a warren view, ask the server to reload its warren state
      // (mounts, routes, runtime bridges) from disk before re-fetching.
      if (currentNamespace.startsWith('_warren/')) {
        const warrenId = currentNamespace.slice('_warren/'.length);
        try {
          await refreshWarrenState(warrenId);
        } catch (err) {
          // Surface the failure (the graph reload below still runs)
          // instead of the old silent swallow.
          warrenPanel.reportError(
            `Warren refresh failed: ${err instanceof Error ? err.message : String(err)}`,
          );
        }
      }
      await loadGraph();
      await refreshWarrenSelectorLabels();
    })();
  });

  /* ── Filter sidebar toggle (overlay drawer on narrow viewports) ── */
  const filterPanel = document.getElementById('filter-panel');
  const filterToggle = document.getElementById('filter-toggle');
  const narrowViewport = window.matchMedia('(max-width: 900px)');
  const syncFilterPanel = (): void => {
    // Collapsed by default on narrow viewports (where it overlays the
    // canvas); always visible inline on wide viewports.
    filterPanel?.classList.toggle('collapsed', narrowViewport.matches);
  };
  syncFilterPanel();
  narrowViewport.addEventListener('change', syncFilterPanel);
  filterToggle?.addEventListener('click', () => {
    filterPanel?.classList.toggle('collapsed');
  });
  // Tapping the canvas dismisses the overlay drawer on narrow viewports.
  document.getElementById('graph-container')?.addEventListener('click', () => {
    if (narrowViewport.matches) filterPanel?.classList.add('collapsed');
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
  // Deliberate page teardown (reload/navigation) aborts the EventSource,
  // which used to log a scary "connection lost" warning plus an
  // ERR_ABORTED /api/events entry on every reload. Close the stream
  // cleanly and skip the warning when the page is going away.
  let pageUnloading = false;
  const closeEventsOnUnload = (): void => {
    pageUnloading = true;
    evtSource.close();
  };
  window.addEventListener('pagehide', closeEventsOnUnload);
  window.addEventListener('beforeunload', closeEventsOnUnload);
  evtSource.onerror = () => {
    if (pageUnloading) return;
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
    } else if (currentNamespace === '_warrens') {
      currentData = await loadAllWarrensGraph(includeSuperseded);
    } else if (currentNamespace.startsWith('_warren/')) {
      const warrenId = currentNamespace.slice('_warren/'.length);
      currentData = await fetchWarrenGraph(warrenId);
      // Feed skip reasons to the warren panel so its rows carry tooltips.
      warrenPanel?.setSkippedReasons(warrenId, currentData.skipped_reasons ?? {});
      // Skipped projects (unreachable warren checkout, unreadable project
      // graph) come back as a 200 with skip reasons — without a toast an
      // unreachable warren renders as a silently empty graph (C2).
      const skipped = currentData.skipped ?? [];
      if (skipped.length > 0) {
        const reasons = currentData.skipped_reasons ?? {};
        const detail = skipped.map((p) => `${p}: ${reasons[p] ?? 'skipped'}`).join('; ');
        showToast(
          `Warren "${warrenId}": ${skipped.length} project(s) skipped from graph — ${detail}`,
          'error',
        );
      }
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

    /* Zero-node views (e.g. every project of a warren skipped as
       unreachable) render an explicit empty-state instead of blank space. */
    updateGraphEmptyState(currentData);

    /* Sync the group-by dropdown UI for the aggregate views */
    if (currentNamespace === '_all' || currentNamespace.startsWith('_warren/')) {
      const groupBySelect = document.getElementById('groupby-select') as HTMLSelectElement;
      groupBySelect.value = 'namespace';
    } else if (currentNamespace === '_warrens') {
      /* The all-warrens aggregate clusters by owning vault: namespaces are
         near-uniform across projects, so project grouping is the readable
         default (update() auto-picks namespace for multi-ns data). */
      const groupBySelect = document.getElementById('groupby-select') as HTMLSelectElement;
      groupBySelect.value = 'project';
      graphView?.setGroupBy('project');
    }

    /* Update curator with fresh graph data */
    curator.updateGraphData(currentData, currentNamespace);

    /* Load curation suggestions after graph data is available */
    issuesPanel.load(issuesNamespaceFilter());
  } catch (err) {
    // Surface the failure to the user (the previous graph stays rendered,
    // which used to make failed loads invisible outside the console).
    const detail = err instanceof Error ? err.message : String(err);
    showToast(`Failed to load graph for "${currentNamespace}": ${detail}`, 'error');
    console.error('Failed to load graph:', err);
  }
}

/* ------------------------------------------------------------------ */
/*  Empty-state overlay                                                */
/* ------------------------------------------------------------------ */

/**
 * Shows (or removes) the graph-area empty-state message. A warren whose
 * every project is skipped (unreachable checkout, unreadable graph) comes
 * back as a 200 with zero nodes — without this the canvas is fully blank
 * and only the transient toast hints at why.
 */
function updateGraphEmptyState(data: GraphResponse): void {
  const container = document.getElementById('graph-container');
  if (!container) return;
  let el = document.getElementById('graph-empty-state');
  if (data.nodes.length > 0) {
    el?.remove();
    return;
  }
  if (!el) {
    el = document.createElement('div');
    el.id = 'graph-empty-state';
    container.appendChild(el);
  }
  const skipped = data.skipped ?? [];
  if (skipped.length > 0) {
    const reasons = data.skipped_reasons ?? {};
    const summary = [...new Set(skipped.map((p) => reasons[p] ?? 'skipped'))].join('; ');
    el.textContent = `No nodes: ${skipped.length} project(s) skipped — ${summary}. See the Warrens panel.`;
  } else {
    el.textContent = 'No nodes in this view.';
  }
}

/* ------------------------------------------------------------------ */
/*  All-warrens aggregate view                                         */
/* ------------------------------------------------------------------ */

/**
 * Builds the "_warrens" aggregate: the local graph (all namespaces) plus
 * every active warren project across all warrens. Each warren loads
 * independently — a failed or skipped warren surfaces as an error toast
 * (reusing the per-warren skip mechanism) and never blocks the view.
 *
 * Identity nodes (@<local-vault>/…) are folded back onto their unqualified
 * live-vault counterparts so the workspace never renders twice; the same
 * dedup keeps a vault mounted through several warrens to a single island.
 */
async function loadAllWarrensGraph(includeSuperseded: boolean): Promise<GraphResponse> {
  const warrenData = await fetchWarrens();
  const localVault = warrenData.local_vault_id ?? '';
  const warrenIds = Object.keys(warrenData.warrens ?? {}).sort();
  const local = await fetchGraphAll(includeSuperseded);

  const normalizeId = (id: string): string =>
    localVault !== '' && id.startsWith(`@${localVault}/`)
      ? id.slice(localVault.length + 2)
      : id;

  const nodeIds = new Set<string>();
  const edgeKeys = new Set<string>();
  const merged: GraphResponse = {
    namespace: '_warrens',
    nodes: [],
    edges: [],
    node_count: 0,
    edge_count: 0,
    heat_pairs: local.heat_pairs,
    namespaces: [],
  };
  const nsSet = new Set<string>(local.namespaces ?? local.nodes.map((n) => n.namespace));

  const pushNode = (n: APINode): void => {
    if (nodeIds.has(n.id)) return;
    nodeIds.add(n.id);
    merged.nodes.push(n);
  };
  const pushEdge = (e: APIEdge): void => {
    const key = `${e.source} ${e.target} ${e.relation}`;
    if (edgeKeys.has(key)) return;
    edgeKeys.add(key);
    merged.edges.push(e);
  };

  local.nodes.forEach(pushNode);
  local.edges.forEach(pushEdge);

  const results = await Promise.allSettled(warrenIds.map((id) => fetchWarrenGraph(id)));
  results.forEach((res, i) => {
    const warrenId = warrenIds[i];
    if (res.status === 'rejected') {
      const detail =
        res.reason instanceof Error ? res.reason.message : String(res.reason);
      showToast(`All warrens: failed to load warren "${warrenId}" — ${detail}`, 'error');
      return;
    }
    const g = res.value;
    // Same skip surfacing as the single-warren view: panel tooltips + toast.
    warrenPanel?.setSkippedReasons(warrenId, g.skipped_reasons ?? {});
    const skipped = g.skipped ?? [];
    if (skipped.length > 0) {
      const reasons = g.skipped_reasons ?? {};
      const detail = skipped.map((p) => `${p}: ${reasons[p] ?? 'skipped'}`).join('; ');
      showToast(
        `Warren "${warrenId}": ${skipped.length} project(s) skipped from graph — ${detail}`,
        'error',
      );
    }
    for (const n of g.nodes) {
      pushNode({
        ...n,
        id: normalizeId(n.id),
        edges: (n.edges ?? []).map((e) => ({
          ...e,
          source: normalizeId(e.source),
          target: normalizeId(e.target),
        })),
      });
    }
    for (const e of g.edges) {
      pushEdge({ ...e, source: normalizeId(e.source), target: normalizeId(e.target) });
    }
    for (const ns of g.namespaces ?? []) nsSet.add(ns);
  });

  merged.node_count = merged.nodes.length;
  merged.edge_count = merged.edges.length;
  merged.namespaces = [...nsSet].sort();
  return merged;
}

/* ------------------------------------------------------------------ */
/*  Warren selector labels                                             */
/* ------------------------------------------------------------------ */

/** Builds the "(N active, M identity)" selector label for one warren. */
function warrenOptionLabel(warrenId: string, warren: WorkspaceWarren): string {
  const count = warren.active_projects?.length ?? 0;
  const identified = warren.identified_projects?.length ?? 0;
  let label = `Warren ${warrenId} (${count} active`;
  if (identified > 0) label += `, ${identified} identity`;
  label += ')';
  return label;
}

/** Builds the "All warrens" aggregate option label with the total active count. */
function allWarrensOptionLabel(warrens: Record<string, WorkspaceWarren>): string {
  const totalActive = Object.values(warrens).reduce(
    (s, w) => s + (w.active_projects?.length ?? 0),
    0,
  );
  return `All warrens (${totalActive} active)`;
}

/**
 * Re-fetches /api/warrens and rewrites the selector's warren option labels
 * so their active/identity counts track panel mounts and unmounts.
 */
async function refreshWarrenSelectorLabels(): Promise<void> {
  const select = document.getElementById('namespace-select') as HTMLSelectElement | null;
  if (!select) return;
  let warrens: Record<string, WorkspaceWarren>;
  try {
    warrens = (await fetchWarrens()).warrens ?? {};
  } catch {
    return; // Selector labels are best-effort; the panel surfaces failures.
  }
  for (const opt of Array.from(select.options)) {
    if (opt.value === '_warrens') {
      opt.textContent = allWarrensOptionLabel(warrens);
      continue;
    }
    if (!opt.value.startsWith('_warren/')) continue;
    const warrenId = opt.value.slice('_warren/'.length);
    const warren = warrens[warrenId];
    if (warren) opt.textContent = warrenOptionLabel(warrenId, warren);
  }
}

/** Namespace filter for the issues panel: aggregate views get no filter. */
function issuesNamespaceFilter(): string | undefined {
  return currentNamespace === '_all' ||
    currentNamespace === '_warrens' ||
    currentNamespace.startsWith('_warren/')
    ? undefined
    : currentNamespace;
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
