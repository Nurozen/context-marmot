import * as d3 from 'd3';
import type { APIEdge, APIHeatPair, APINode, GraphResponse } from './types';
import { nodeColor } from './types';
import { Minimap } from './minimap';
import { HeatOverlay } from './heat-overlay';

/* ------------------------------------------------------------------ */
/*  Internal types for the simulation                                 */
/* ------------------------------------------------------------------ */

interface SimNode extends d3.SimulationNodeDatum {
  id: string;
  type: string;
  namespace: string;
  status: string;
  summary: string;
  context: string;
  edge_count: number;
  is_stale: boolean;
  tags: string[];
  superseded_by?: string;
  radius: number;
  /** Fixed after drag */
  fx?: number | null;
  fy?: number | null;
}

interface SimLink extends d3.SimulationLinkDatum<SimNode> {
  relation: string;
  class: 'structural' | 'behavioral' | 'bridge';
}

/* ------------------------------------------------------------------ */
/*  Helpers                                                           */
/* ------------------------------------------------------------------ */

function shortLabel(id: string): string {
  const parts = id.split('/');
  return parts[parts.length - 1];
}

function nodeRadius(edgeCount: number): number {
  return Math.min(6 + Math.sqrt(edgeCount) * 2.5, 30);
}

/* ------------------------------------------------------------------ */
/*  GraphView                                                          */
/* ------------------------------------------------------------------ */

export class GraphView {
  /* DOM handles */
  private svg: d3.Selection<SVGSVGElement, unknown, HTMLElement, unknown>;
  private container: d3.Selection<SVGGElement, unknown, HTMLElement, unknown>;
  private defs: d3.Selection<SVGDefsElement, unknown, HTMLElement, unknown>;

  private nsLabelGroup!: d3.Selection<SVGGElement, unknown, HTMLElement, unknown>;
  private folderHullGroup!: d3.Selection<SVGGElement, unknown, HTMLElement, unknown>;
  private linkGroup!: d3.Selection<SVGGElement, unknown, HTMLElement, unknown>;
  private bridgeGroup!: d3.Selection<SVGGElement, unknown, HTMLElement, unknown>;
  private heatGroup!: d3.Selection<SVGGElement, unknown, HTMLElement, unknown>;
  private nodeGroup!: d3.Selection<SVGGElement, unknown, HTMLElement, unknown>;

  /* Simulation */
  private simulation: d3.Simulation<SimNode, SimLink>;
  private nodes: SimNode[] = [];
  private links: SimLink[] = [];

  /* State */
  private currentTransform: d3.ZoomTransform = d3.zoomIdentity;
  private zoomBehavior: d3.ZoomBehavior<SVGSVGElement, unknown>;
  private visibleTypes: Set<string> = new Set();
  private edgeClassFilter: 'all' | 'structural' | 'behavioral' | 'bridge' = 'all';
  private bridgeNodeIds: Set<string> = new Set();
  private activeNamespaces: string[] = [];
  private bridgeLinks: SimLink[] = [];
  private normalLinks: SimLink[] = [];
  private heatEnabled = false;
  private heatPairs: APIHeatPair[] = [];
  private groupBy: 'none' | 'type' | 'namespace' | 'tag' | 'folder' = 'type';

  /* Multi-selection for curator integration */
  private multiSelectedIds: Set<string> = new Set();

  /* Pulse animation layer */
  private pulseGroup!: d3.Selection<SVGGElement, unknown, HTMLElement, unknown>;

  /* Minimap & heat overlay */
  private minimap: Minimap | null = null;
  private heatOverlay: HeatOverlay = new HeatOverlay();

  /* Tooltip */
  private tooltip: d3.Selection<HTMLDivElement, unknown, null, undefined>;

  constructor() {
    this.svg = d3.select<SVGSVGElement, unknown>('#graph-svg');
    this.defs = this.svg.append('defs');

    /* Arrow marker for directed edges */
    this.defs
      .append('marker')
      .attr('id', 'arrow')
      .attr('viewBox', '0 -4 8 8')
      .attr('refX', 12)
      .attr('refY', 0)
      .attr('markerWidth', 6)
      .attr('markerHeight', 6)
      .attr('orient', 'auto')
      .append('path')
      .attr('d', 'M0,-3L7,0L0,3')
      .attr('fill', '#555');

    /* Main transform group */
    this.container = this.svg.append('g').attr('class', 'graph-layer');
    this.nsLabelGroup = this.container.append('g').attr('class', 'ns-labels');
    this.folderHullGroup = this.container.append('g').attr('class', 'folder-hulls');
    this.linkGroup = this.container.append('g').attr('class', 'links');
    this.bridgeGroup = this.container.append('g').attr('class', 'bridge-arcs');
    this.heatGroup = this.container.append('g').attr('class', 'heat-links');
    this.nodeGroup = this.container.append('g').attr('class', 'nodes');
    this.pulseGroup = this.container.append('g').attr('class', 'pulse-rings');

    /* Zoom */
    this.zoomBehavior = d3
      .zoom<SVGSVGElement, unknown>()
      .scaleExtent([0.1, 8])
      .on('zoom', (event: d3.D3ZoomEvent<SVGSVGElement, unknown>) => {
        this.currentTransform = event.transform;
        this.container.attr('transform', event.transform.toString());
        this.updateLabelVisibility();
        this.updateMinimap();
      });
    this.svg.call(this.zoomBehavior);

    /* Click on empty canvas */
    this.svg.on('click', (event: MouseEvent) => {
      if ((event.target as Element).tagName === 'svg') {
        document.getElementById('graph-svg')?.dispatchEvent(
          new CustomEvent('node-deselected'),
        );
      }
    });

    /* Tooltip element */
    let ttEl = document.querySelector<HTMLDivElement>('.graph-tooltip');
    if (!ttEl) {
      ttEl = document.createElement('div');
      ttEl.className = 'graph-tooltip';
      document.getElementById('graph-container')!.appendChild(ttEl);
    }
    this.tooltip = d3.select<HTMLDivElement, unknown>(ttEl);

    /* Simulation (initially empty) */
    this.simulation = d3
      .forceSimulation<SimNode, SimLink>()
      .force(
        'link',
        d3.forceLink<SimNode, SimLink>().id((d) => d.id).distance(80),
      )
      .force('charge', d3.forceManyBody<SimNode>().strength(-200))
      .force('collide', d3.forceCollide<SimNode>().radius((d) => d.radius + 2))
      .on('tick', () => this.tick());

    this.simulation.stop();

    /* Minimap */
    const minimapContainer = document.getElementById('minimap');
    if (minimapContainer) {
      this.minimap = new Minimap(minimapContainer);
    }
  }

  /* ---------------------------------------------------------------- */
  /*  Public API                                                       */
  /* ---------------------------------------------------------------- */

  update(data: GraphResponse): void {
    const { width, height } = this.dimensions();

    /* Build node map for quick lookup */
    const nodeMap = new Map<string, APINode>();
    data.nodes.forEach((n) => nodeMap.set(n.id, n));

    /* Snapshot existing node positions so we can preserve them across reloads.
       This prevents the entire graph from re-settling when only a few nodes
       are added/removed (e.g. during live-reload via SSE). */
    const prevPositions = new Map<string, { x: number; y: number; fx?: number | null; fy?: number | null }>();
    for (const n of this.nodes) {
      if (n.x != null && n.y != null) {
        prevPositions.set(n.id, { x: n.x, y: n.y, fx: n.fx, fy: n.fy });
      }
    }

    /* Build SimNodes — restore positions for returning nodes */
    this.nodes = data.nodes.map((n) => {
      const prev = prevPositions.get(n.id);
      return {
        id: n.id,
        type: n.type,
        namespace: n.namespace,
        status: n.status,
        summary: n.summary,
        context: n.context,
        edge_count: n.edge_count,
        is_stale: n.is_stale,
        tags: n.tags ?? [],
        superseded_by: n.superseded_by,
        radius: nodeRadius(n.edge_count),
        ...(prev ? { x: prev.x, y: prev.y, fx: prev.fx, fy: prev.fy } : {}),
      };
    });

    /* Build SimLinks — only include links whose both endpoints exist */
    const nodeIds = new Set(this.nodes.map((n) => n.id));
    this.links = data.edges
      .filter((e) => nodeIds.has(e.source) && nodeIds.has(e.target))
      .map((e) => ({
        source: e.source,
        target: e.target,
        relation: e.relation,
        class: e.class,
      }));

    /* Split links into normal and bridge */
    this.normalLinks = this.links.filter((l) => l.class !== 'bridge');
    this.bridgeLinks = this.links.filter((l) => l.class === 'bridge');

    /* Compute set of node IDs participating in bridge edges */
    this.bridgeNodeIds = new Set<string>();
    for (const bl of this.bridgeLinks) {
      const srcId = typeof bl.source === 'object' ? (bl.source as SimNode).id : (bl.source as string);
      const tgtId = typeof bl.target === 'object' ? (bl.target as SimNode).id : (bl.target as string);
      this.bridgeNodeIds.add(srcId);
      this.bridgeNodeIds.add(tgtId);
    }

    /* Store active namespaces */
    this.activeNamespaces = data.namespaces ?? [];

    /* Populate visible types */
    this.visibleTypes = new Set(data.nodes.map((n) => n.type));

    /* Heat pairs */
    this.heatPairs = data.heat_pairs ?? [];

    /* Dynamic charge based on node count */
    const charge = this.nodes.length > 200 ? -150 : this.nodes.length > 80 ? -200 : -300;

    /* Reconfigure simulation — forceCenter is managed by applyGroupForces()
       so that it gets removed when island grouping is active. */
    this.simulation
      .nodes(this.nodes)
      .force(
        'link',
        d3
          .forceLink<SimNode, SimLink>(this.links)
          .id((d) => d.id)
          .distance(80),
      )
      .force('charge', d3.forceManyBody<SimNode>().strength(charge))
      .force('collide', d3.forceCollide<SimNode>().radius((d) => d.radius + 2));

    /* Auto-group by namespace when multi-namespace view */
    if (this.activeNamespaces.length > 1) {
      this.groupBy = 'namespace';
    }

    this.folderHullGroup.selectAll('*').remove();

    this.applyGroupForces(width, height);

    /* If most nodes already have positions (live-reload), use a gentle alpha
       so only new nodes settle in. Full alpha(1) for initial load. */
    const returningCount = this.nodes.filter((n) => prevPositions.has(n.id)).length;
    const isReload = returningCount > 0 && returningCount >= this.nodes.length * 0.5;
    this.simulation.alpha(isReload ? 0.3 : 1).restart();

    this.renderLinks();
    this.renderBridgeArcs();
    this.renderHeatLinks();
    this.renderNodes();
    this.renderNsLabels();
  }

  filterByTypes(types: Set<string>): void {
    this.visibleTypes = types;
    this.applyVisibility();
    this.reheat(0.3);
  }

  private visibleTags: Set<string> | null = null;

  filterByTags(tags: Set<string> | null): void {
    this.visibleTags = tags;
    this.applyVisibility();
    this.reheat(0.3);
  }

  filterByEdgeClass(cls: 'all' | 'structural' | 'behavioral' | 'bridge'): void {
    this.edgeClassFilter = cls;
    this.applyVisibility();
  }

  highlightNode(id: string): void {
    const node = this.nodes.find((n) => n.id === id);
    if (!node || node.x == null || node.y == null) return;

    const { width, height } = this.dimensions();
    const scale = 2;
    const tx = width / 2 - node.x * scale;
    const ty = height / 2 - node.y * scale;
    const transform = d3.zoomIdentity.translate(tx, ty).scale(scale);

    this.svg
      .transition()
      .duration(600)
      .call(this.zoomBehavior.transform, transform);

    /* Pulse animation */
    const el = this.nodeGroup.selectAll<SVGGElement, SimNode>('.node').filter((d) => d.id === id);
    el.select('circle')
      .transition()
      .duration(200)
      .attr('r', (d) => d.radius * 1.8)
      .transition()
      .duration(400)
      .attr('r', (d) => d.radius);
  }

  /**
   * Animated pulse highlight: 3 concentric amber rings expanding from the node.
   * Pans the camera to center the node.
   */
  pulseNode(nodeId: string): void {
    const node = this.nodes.find((n) => n.id === nodeId);
    if (!node || node.x == null || node.y == null) return;

    /* Pan to center the node */
    const { width, height } = this.dimensions();
    const scale = 2;
    const tx = width / 2 - node.x * scale;
    const ty = height / 2 - node.y * scale;
    const transform = d3.zoomIdentity.translate(tx, ty).scale(scale);
    this.svg.transition().duration(600).call(this.zoomBehavior.transform, transform);

    /* Create 3 concentric pulse rings at staggered delays */
    const cx = node.x;
    const cy = node.y;
    const r = node.radius;

    for (let i = 0; i < 3; i++) {
      const ring = this.pulseGroup
        .append('circle')
        .attr('class', 'node-pulse-ring')
        .attr('cx', cx)
        .attr('cy', cy)
        .attr('r', r)
        .attr('fill', 'none')
        .attr('stroke', '#d4a853')
        .attr('stroke-width', 2)
        .attr('opacity', 0);

      ring
        .transition()
        .delay(i * 300)
        .duration(0)
        .attr('opacity', 0.4)
        .transition()
        .duration(2000)
        .ease(d3.easeCubicOut)
        .attr('r', r * 3)
        .attr('opacity', 0)
        .attr('stroke-width', 0.5)
        .remove();
    }
  }

  /** Toggles a node in/out of the multi-selection set (Shift+click). */
  toggleMultiSelect(nodeId: string): void {
    if (this.multiSelectedIds.has(nodeId)) {
      this.multiSelectedIds.delete(nodeId);
    } else {
      this.multiSelectedIds.add(nodeId);
    }
    this.applyMultiSelectStyling();
    document.dispatchEvent(
      new CustomEvent('curator-selection-changed', {
        detail: { nodeIds: [...this.multiSelectedIds] },
      }),
    );
  }

  /** Clears multi-selection. */
  clearMultiSelect(): void {
    this.multiSelectedIds.clear();
    this.applyMultiSelectStyling();
    document.dispatchEvent(
      new CustomEvent('curator-selection-changed', {
        detail: { nodeIds: [] },
      }),
    );
  }

  /** Apply dashed amber ring to multi-selected nodes. */
  private applyMultiSelectStyling(): void {
    this.nodeGroup
      .selectAll<SVGGElement, SimNode>('.node')
      .classed('multiselected', (d) => this.multiSelectedIds.has(d.id));
  }

  setHeatOverlay(enabled: boolean, heatPairs?: APIHeatPair[]): void {
    this.heatEnabled = enabled;
    if (heatPairs) this.heatPairs = heatPairs;

    /* Update the heat overlay module */
    if (enabled) {
      this.heatOverlay.setData(this.heatPairs);
      this.heatOverlay.enable();
    } else {
      this.heatOverlay.disable();
    }

    this.renderHeatLinks();
    this.applyNodeGlow();
  }

  /* ---------------------------------------------------------------- */
  /*  Rendering                                                        */
  /* ---------------------------------------------------------------- */

  private renderLinks(): void {
    const linkSel = this.linkGroup
      .selectAll<SVGLineElement, SimLink>('.link')
      .data(this.normalLinks, (d) => `${(d.source as SimNode).id ?? d.source}-${(d.target as SimNode).id ?? d.target}-${d.relation}`);

    linkSel.exit().remove();

    const enter = linkSel
      .enter()
      .append('line')
      .attr('class', (d) => `link ${d.class} edge-entering`)
      .attr('stroke', '#555')
      .attr('stroke-opacity', (d) => (d.class === 'behavioral' ? 0.4 : 0.6))
      .attr('stroke-dasharray', (d) => (d.class === 'behavioral' ? '6,4' : null))
      .attr('marker-end', 'url(#arrow)');

    /* New edges draw in via stroke-dashoffset animation.
       The CSS class .edge-entering drives a 500ms transition. We remove the
       class after the animation so it doesn't interfere with other dasharray
       settings. */
    enter.each(function () {
      const el = this as SVGLineElement;
      // The actual length is set after first tick positions the line,
      // so we use a generous default.
      el.style.strokeDasharray = '1000';
      el.style.strokeDashoffset = '1000';
      requestAnimationFrame(() => {
        el.style.transition = 'stroke-dashoffset 500ms ease-out';
        el.style.strokeDashoffset = '0';
        setTimeout(() => {
          el.style.strokeDasharray = '';
          el.style.strokeDashoffset = '';
          el.style.transition = '';
          el.classList.remove('edge-entering');
        }, 520);
      });
    });

    enter.merge(linkSel);
    this.applyVisibility();
  }

  private renderHeatLinks(): void {
    if (!this.heatEnabled) {
      this.heatGroup.selectAll('.heat-link').classed('visible', false);
      return;
    }

    const nodeMap = new Map<string, SimNode>();
    this.nodes.forEach((n) => nodeMap.set(n.id, n));

    /* Filter to pairs where both nodes are visible */
    let heatData = this.heatPairs.filter(
      (p) => nodeMap.has(p.a) && nodeMap.has(p.b),
    );

    /* Sort by weight descending and take only the top pairs.
       Show at most 50 links, or the top 40% — whichever is smaller. */
    heatData.sort((a, b) => b.weight - a.weight);
    const maxLinks = Math.min(50, Math.ceil(heatData.length * 0.4));
    heatData = heatData.slice(0, maxLinks);

    const maxW = heatData[0]?.weight ?? 1;
    const minW = heatData[heatData.length - 1]?.weight ?? 0;
    const range = maxW - minW || 1;

    const heatSel = this.heatGroup
      .selectAll<SVGLineElement, APIHeatPair>('.heat-link')
      .data(heatData, (d) => `${d.a}-${d.b}`);

    heatSel.exit().remove();

    const enter = heatSel
      .enter()
      .append('line')
      .attr('class', 'heat-link visible')
      .attr('stroke-width', (d) => 1.5 + ((d.weight - minW) / range) * 4)
      .attr('stroke-opacity', (d) => 0.08 + ((d.weight - minW) / range) * 0.45);

    enter.merge(heatSel)
      .classed('visible', true)
      .attr('stroke-width', (d) => 1.5 + ((d.weight - minW) / range) * 4)
      .attr('stroke-opacity', (d) => 0.08 + ((d.weight - minW) / range) * 0.45);
  }

  /** Compute a quadratic Bezier path string for a bridge arc that curves upward. */
  private bridgeArcPath(d: SimLink): string {
    const src = d.source as SimNode;
    const tgt = d.target as SimNode;
    const x1 = src.x ?? 0;
    const y1 = src.y ?? 0;
    const x2 = tgt.x ?? 0;
    const y2 = tgt.y ?? 0;

    /* Midpoint */
    const mx = (x1 + x2) / 2;
    const my = (y1 + y2) / 2;

    /* Distance between endpoints */
    const dx = x2 - x1;
    const dy = y2 - y1;
    const dist = Math.sqrt(dx * dx + dy * dy) || 1;

    /* Perpendicular offset (40% of distance, arcing upward) */
    const offset = dist * 0.4;
    /* Normal vector perpendicular to the line */
    const nx = -dy / dist;
    const ny = dx / dist;

    /* Control point — offset in the direction that curves "upward" (negative y) */
    const cpx = mx + nx * offset;
    const cpy = my + ny * offset;

    return `M ${x1},${y1} Q ${cpx},${cpy} ${x2},${y2}`;
  }

  private renderBridgeArcs(): void {
    const arcSel = this.bridgeGroup
      .selectAll<SVGPathElement, SimLink>('.bridge-arc')
      .data(this.bridgeLinks, (d) => {
        const srcId = typeof d.source === 'object' ? (d.source as SimNode).id : (d.source as string);
        const tgtId = typeof d.target === 'object' ? (d.target as SimNode).id : (d.target as string);
        return `${srcId}-${tgtId}-${d.relation}`;
      });

    arcSel.exit().remove();

    const enter = arcSel
      .enter()
      .append('path')
      .attr('class', 'bridge-arc')
      .attr('d', (d) => this.bridgeArcPath(d));

    enter.merge(arcSel).attr('d', (d) => this.bridgeArcPath(d));
  }

  private renderNsLabels(): void {
    if (this.activeNamespaces.length <= 1) {
      this.nsLabelGroup.selectAll('.ns-label').remove();
      return;
    }

    const labelSel = this.nsLabelGroup
      .selectAll<SVGTextElement, string>('.ns-label')
      .data(this.activeNamespaces, (d) => d);

    labelSel.exit().remove();

    labelSel
      .enter()
      .append('text')
      .attr('class', 'ns-label')
      .text((d) => d);
    /* Positions are set during tick() via updateNsLabelPositions */
  }

  /** Recompute centroid positions of namespace labels based on current node positions. */
  private updateNsLabelPositions(): void {
    if (this.activeNamespaces.length <= 1) return;

    /* Compute centroid per namespace */
    const nsCentroids = new Map<string, { sx: number; sy: number; count: number }>();
    for (const n of this.nodes) {
      if (n.x == null || n.y == null) continue;
      let entry = nsCentroids.get(n.namespace);
      if (!entry) {
        entry = { sx: 0, sy: 0, count: 0 };
        nsCentroids.set(n.namespace, entry);
      }
      entry.sx += n.x;
      entry.sy += n.y;
      entry.count += 1;
    }

    this.nsLabelGroup
      .selectAll<SVGTextElement, string>('.ns-label')
      .attr('x', (d) => {
        const c = nsCentroids.get(d);
        return c && c.count > 0 ? c.sx / c.count : 0;
      })
      .attr('y', (d) => {
        const c = nsCentroids.get(d);
        return c && c.count > 0 ? c.sy / c.count : 0;
      });
  }

  private renderNodes(): void {
    const nodeSel = this.nodeGroup
      .selectAll<SVGGElement, SimNode>('.node')
      .data(this.nodes, (d) => d.id);

    /* Deleted nodes: shrink + fade out over 300ms */
    nodeSel.exit()
      .classed('node-exiting', true)
      .transition()
      .duration(300)
      .ease(d3.easeCubicIn)
      .style('opacity', 0)
      .select('circle')
      .attr('r', 0)
      .remove();

    const enter = nodeSel.enter().append('g').attr('class', (d) => {
      let cls = 'node';
      if (d.is_stale) cls += ' stale';
      if (d.superseded_by) cls += ' superseded';
      if (this.bridgeNodeIds.has(d.id)) cls += ' bridge-node';
      return cls;
    });

    /* Circle — new nodes scale in with a spring animation */
    enter
      .style('opacity', 0)
      .transition()
      .duration(400)
      .ease(d3.easeCubicOut)
      .style('opacity', 1);

    enter
      .append('circle')
      .attr('r', 0)
      .attr('fill', (d) => nodeColor(d.type))
      .attr('opacity', (d) => (d.superseded_by ? 0.4 : 1))
      .transition()
      .duration(400)
      .ease(d3.easeBackOut.overshoot(1.5))
      .attr('r', (d) => d.radius);

    /* Label */
    enter
      .append('text')
      .attr('dy', (d) => d.radius + 12)
      .text((d) => shortLabel(d.id))
      .attr('opacity', 0);

    /* Interaction: drag */
    const dragBehavior = d3
      .drag<SVGGElement, SimNode>()
      .on('start', (_event, d) => {
        if (!_event.active) this.simulation.alphaTarget(0.3).restart();
        d.fx = d.x;
        d.fy = d.y;
      })
      .on('drag', (_event, d) => {
        d.fx = _event.x;
        d.fy = _event.y;
      })
      .on('end', (_event, d) => {
        if (!_event.active) this.simulation.alphaTarget(0);
        /* Keep node pinned after drag */
        d.fx = _event.x;
        d.fy = _event.y;
      });

    enter.call(dragBehavior);

    /* Click — supports Shift+click multi-select and @mention injection */
    enter.on('click', (event: MouseEvent, d: SimNode) => {
      event.stopPropagation();

      if (event.shiftKey) {
        /* Shift+click: toggle multi-selection */
        this.toggleMultiSelect(d.id);
        return;
      }

      /* If the curator input is focused, inject @mention instead of normal select */
      const chatInput = document.getElementById('chat-input') as HTMLInputElement | null;
      if (chatInput && document.activeElement === chatInput) {
        document.dispatchEvent(
          new CustomEvent('curator-inject-mention', { detail: { nodeId: d.id } }),
        );
        return;
      }

      /* Normal click: select node + show detail panel */
      document.getElementById('graph-svg')?.dispatchEvent(
        new CustomEvent<SimNode>('node-selected', { detail: d }),
      );
      this.selectNode(d.id);
    });

    /* Hover */
    enter
      .on('mouseenter', (event: MouseEvent, d: SimNode) => {
        this.highlightNeighbors(d);
        this.showTooltip(event, d);
      })
      .on('mousemove', (event: MouseEvent) => {
        this.moveTooltip(event);
      })
      .on('mouseleave', () => {
        this.clearHighlight();
        this.hideTooltip();
      });

    /* Merge enter + update for drag on existing nodes too */
    const merged = enter.merge(nodeSel);
    merged.call(dragBehavior);

    this.updateLabelVisibility();
  }

  private selectNode(id: string): void {
    this.nodeGroup.selectAll<SVGGElement, SimNode>('.node').classed('selected', (d) => d.id === id);
  }

  /* ---------------------------------------------------------------- */
  /*  Hover highlighting                                               */
  /* ---------------------------------------------------------------- */

  private highlightNeighbors(d: SimNode): void {
    const connected = new Set<string>();
    connected.add(d.id);

    this.links.forEach((l) => {
      const srcId = typeof l.source === 'object' ? (l.source as SimNode).id : (l.source as string);
      const tgtId = typeof l.target === 'object' ? (l.target as SimNode).id : (l.target as string);
      if (srcId === d.id) connected.add(tgtId);
      if (tgtId === d.id) connected.add(srcId);
    });

    this.nodeGroup.selectAll<SVGGElement, SimNode>('.node').classed('dimmed', (n) => !connected.has(n.id));

    this.linkGroup.selectAll<SVGLineElement, SimLink>('.link').each(function (l) {
      const srcId = typeof l.source === 'object' ? (l.source as SimNode).id : (l.source as string);
      const tgtId = typeof l.target === 'object' ? (l.target as SimNode).id : (l.target as string);
      const isConnected = srcId === d.id || tgtId === d.id;
      d3.select(this)
        .classed('dimmed', !isConnected)
        .classed('highlighted', isConnected);
    });

    /* Bridge arcs */
    this.bridgeGroup.selectAll<SVGPathElement, SimLink>('.bridge-arc').each(function (l) {
      const srcId = typeof l.source === 'object' ? (l.source as SimNode).id : (l.source as string);
      const tgtId = typeof l.target === 'object' ? (l.target as SimNode).id : (l.target as string);
      const isConnected = srcId === d.id || tgtId === d.id;
      d3.select(this)
        .classed('dimmed', !isConnected)
        .classed('highlighted', isConnected);
    });
  }

  private clearHighlight(): void {
    this.nodeGroup.selectAll('.node').classed('dimmed', false);
    this.linkGroup.selectAll('.link').classed('dimmed', false).classed('highlighted', false);
    this.bridgeGroup.selectAll('.bridge-arc').classed('dimmed', false).classed('highlighted', false);
  }

  /* ---------------------------------------------------------------- */
  /*  Tooltip                                                          */
  /* ---------------------------------------------------------------- */

  private showTooltip(event: MouseEvent, d: SimNode): void {
    const el = this.tooltip.node();
    if (!el) return;

    el.textContent = '';

    const title = document.createElement('div');
    title.className = 'tt-title';
    title.textContent = shortLabel(d.id);
    el.appendChild(title);

    const typeInfo = document.createElement('div');
    typeInfo.className = 'tt-type';
    typeInfo.textContent = `${d.type} \u00B7 ${d.edge_count} edges`;
    el.appendChild(typeInfo);

    const summary = document.createElement('div');
    summary.className = 'tt-summary';
    summary.textContent = d.summary || d.id;
    el.appendChild(summary);

    this.tooltip.classed('visible', true);
    this.moveTooltip(event);
  }

  private moveTooltip(event: MouseEvent): void {
    const container = document.getElementById('graph-container');
    if (!container) return;
    const rect = container.getBoundingClientRect();
    const x = event.clientX - rect.left + 12;
    const y = event.clientY - rect.top - 8;
    this.tooltip.style('left', `${x}px`).style('top', `${y}px`);
  }

  private hideTooltip(): void {
    this.tooltip.classed('visible', false);
  }

  /* ---------------------------------------------------------------- */
  /*  Simulation tick                                                  */
  /* ---------------------------------------------------------------- */

  private tick(): void {
    /* Links */
    this.linkGroup
      .selectAll<SVGLineElement, SimLink>('.link')
      .attr('x1', (d) => ((d.source as SimNode).x ?? 0))
      .attr('y1', (d) => ((d.source as SimNode).y ?? 0))
      .attr('x2', (d) => ((d.target as SimNode).x ?? 0))
      .attr('y2', (d) => ((d.target as SimNode).y ?? 0));

    /* Bridge arcs — recompute Bezier paths */
    this.bridgeGroup
      .selectAll<SVGPathElement, SimLink>('.bridge-arc')
      .attr('d', (d) => this.bridgeArcPath(d));

    /* Heat links */
    if (this.heatEnabled) {
      const nodeMap = new Map<string, SimNode>();
      this.nodes.forEach((n) => nodeMap.set(n.id, n));

      this.heatGroup
        .selectAll<SVGLineElement, APIHeatPair>('.heat-link')
        .attr('x1', (d) => nodeMap.get(d.a)?.x ?? 0)
        .attr('y1', (d) => nodeMap.get(d.a)?.y ?? 0)
        .attr('x2', (d) => nodeMap.get(d.b)?.x ?? 0)
        .attr('y2', (d) => nodeMap.get(d.b)?.y ?? 0);
    }

    /* Namespace watermark labels */
    this.updateNsLabelPositions();

    /* Folder contour hulls */
    this.renderFolderHulls();

    /* Nodes */
    this.nodeGroup
      .selectAll<SVGGElement, SimNode>('.node')
      .attr('transform', (d) => `translate(${d.x ?? 0},${d.y ?? 0})`);

    /* Minimap */
    this.updateMinimap();
  }

  /* ---------------------------------------------------------------- */
  /*  Minimap & heat glow helpers                                      */
  /* ---------------------------------------------------------------- */

  private updateMinimap(): void {
    if (!this.minimap) return;
    const { width, height } = this.dimensions();
    const minimapNodes = this.nodes
      .filter((n) => this.visibleTypes.has(n.type) && n.x != null && n.y != null)
      .map((n) => ({ x: n.x!, y: n.y!, type: n.type }));
    this.minimap.update(
      minimapNodes,
      { x: this.currentTransform.x, y: this.currentTransform.y, k: this.currentTransform.k },
      width,
      height,
    );
  }

  private applyNodeGlow(): void {
    this.nodeGroup
      .selectAll<SVGGElement, SimNode>('.node')
      .select('circle')
      .style('filter', (d) => this.heatOverlay.getGlowStyle(d.id) || null);
  }

  /* ---------------------------------------------------------------- */
  /*  Visibility helpers                                               */
  /* ---------------------------------------------------------------- */

  private isNodeVisible(d: SimNode): boolean {
    if (!this.visibleTypes.has(d.type)) return false;
    if (this.visibleTags !== null) {
      // If node has no tags, hide it only if there are active tag filters
      if (d.tags.length === 0) return false;
      // Node visible if at least one of its tags is in the active set
      if (!d.tags.some((t) => this.visibleTags!.has(t))) return false;
    }
    return true;
  }

  private applyVisibility(): void {
    this.nodeGroup.selectAll<SVGGElement, SimNode>('.node').attr('display', (d) =>
      this.isNodeVisible(d) ? null : 'none',
    );

    this.linkGroup.selectAll<SVGLineElement, SimLink>('.link').attr('display', (d) => {
      const srcId = typeof d.source === 'object' ? (d.source as SimNode).id : (d.source as string);
      const tgtId = typeof d.target === 'object' ? (d.target as SimNode).id : (d.target as string);
      const srcNode = this.nodes.find((n) => n.id === srcId);
      const tgtNode = this.nodes.find((n) => n.id === tgtId);
      if (!srcNode || !tgtNode) return 'none';
      if (!this.isNodeVisible(srcNode) || !this.isNodeVisible(tgtNode)) return 'none';
      if (this.edgeClassFilter !== 'all' && d.class !== this.edgeClassFilter) return 'none';
      return null;
    });

    /* Bridge arcs visibility */
    this.bridgeGroup.selectAll<SVGPathElement, SimLink>('.bridge-arc').attr('display', (d) => {
      const srcId = typeof d.source === 'object' ? (d.source as SimNode).id : (d.source as string);
      const tgtId = typeof d.target === 'object' ? (d.target as SimNode).id : (d.target as string);
      const srcNode = this.nodes.find((n) => n.id === srcId);
      const tgtNode = this.nodes.find((n) => n.id === tgtId);
      if (!srcNode || !tgtNode) return 'none';
      if (!this.isNodeVisible(srcNode) || !this.isNodeVisible(tgtNode)) return 'none';
      if (this.edgeClassFilter !== 'all' && d.class !== this.edgeClassFilter) return 'none';
      return null;
    });

    this.renderFolderHulls();
  }

  private updateLabelVisibility(): void {
    const showLabels = this.currentTransform.k > 1.0;
    this.nodeGroup
      .selectAll<SVGTextElement, SimNode>('.node text')
      .attr('opacity', showLabels ? 0.9 : 0);
  }

  /* ---------------------------------------------------------------- */
  /*  Layout helpers                                                   */
  /* ---------------------------------------------------------------- */

  private dimensions(): { width: number; height: number } {
    const el = this.svg.node();
    if (!el) return { width: 800, height: 600 };
    return { width: el.clientWidth || 800, height: el.clientHeight || 600 };
  }

  /** Compute centroid positions for a set of group keys arranged in a circle. */
  private groupCentroids(
    keys: string[],
    width: number,
    height: number,
  ): Map<string, { x: number; y: number }> {
    const map = new Map<string, { x: number; y: number }>();
    const cx = width / 2;
    const cy = height / 2;
    const r = Math.min(width, height) * 0.28;

    keys.forEach((key, i) => {
      if (keys.length === 1) {
        map.set(key, { x: cx, y: cy });
      } else {
        const angle = (2 * Math.PI * i) / keys.length - Math.PI / 2;
        map.set(key, {
          x: cx + r * Math.cos(angle),
          y: cy + r * Math.sin(angle),
        });
      }
    });
    return map;
  }

  /** Apply forceX/forceY based on the current groupBy setting. */
  private applyGroupForces(width: number, height: number): void {
    const cx = width / 2;
    const cy = height / 2;

    /* Remove forceCenter when grouping is active — it competes with island
       forces and collapses groups toward the middle. Replace with a gentle
       forceX/forceY toward center for the 'none' case only. */
    if (this.groupBy !== 'none') {
      this.simulation.force('center', null);
    } else {
      this.simulation.force('center', d3.forceCenter(cx, cy).strength(0.5));
    }

    if (this.groupBy === 'none') {
      this.simulation
        .force('x', d3.forceX<SimNode>(cx).strength(0.04))
        .force('y', d3.forceY<SimNode>(cy).strength(0.04));
      return;
    }

    /* Tag-based grouping: nodes may belong to multiple tags.
       Instead of averaging all tag centroids (which dilutes multi-tag nodes
       toward the center), pull toward the "primary" tag — the tag with the
       fewest members, making it the most distinctive affiliation. Untagged
       nodes get a gentle center pull. */
    if (this.groupBy === 'tag') {
      const allTags = [...new Set(this.nodes.flatMap((n) => n.tags))].sort();
      if (allTags.length === 0) {
        this.simulation
          .force('x', d3.forceX<SimNode>(cx).strength(0.04))
          .force('y', d3.forceY<SimNode>(cy).strength(0.04));
        return;
      }

      /* Count members per tag to determine "primary" (rarest) tag per node */
      const tagCount = new Map<string, number>();
      for (const n of this.nodes) {
        for (const t of n.tags) tagCount.set(t, (tagCount.get(t) ?? 0) + 1);
      }

      /* Assign each node a primary tag (the one with the fewest members) */
      const primaryTag = (d: SimNode): string | null => {
        if (d.tags.length === 0) return null;
        let best = d.tags[0];
        let bestCount = tagCount.get(best) ?? Infinity;
        for (let i = 1; i < d.tags.length; i++) {
          const c = tagCount.get(d.tags[i]) ?? Infinity;
          if (c < bestCount) { best = d.tags[i]; bestCount = c; }
        }
        return best;
      };

      const tagCentroids = this.groupCentroids(allTags, width, height);
      const strength = 0.18;

      this.simulation
        .force('x', d3.forceX<SimNode>((d) => {
          const pt = primaryTag(d);
          return pt ? (tagCentroids.get(pt)?.x ?? cx) : cx;
        }).strength((d) => d.tags.length > 0 ? strength : 0.02))
        .force('y', d3.forceY<SimNode>((d) => {
          const pt = primaryTag(d);
          return pt ? (tagCentroids.get(pt)?.y ?? cy) : cy;
        }).strength((d) => d.tags.length > 0 ? strength : 0.02));
      return;
    }

    /* Folder-based grouping: cluster by directory prefix.
       Uses a wider centroid radius than other modes to give the contour
       hulls breathing room between islands. */
    if (this.groupBy === 'folder') {
      const folders = [...new Set(this.nodes.map((n) => this.nodeFolder(n)))].sort();
      const r = Math.min(width, height) * 0.38;
      const centroids = new Map<string, { x: number; y: number }>();
      folders.forEach((key, i) => {
        if (folders.length === 1) {
          centroids.set(key, { x: cx, y: cy });
        } else {
          const angle = (2 * Math.PI * i) / folders.length - Math.PI / 2;
          centroids.set(key, { x: cx + r * Math.cos(angle), y: cy + r * Math.sin(angle) });
        }
      });
      const strength = 0.22;

      this.simulation
        .force('x', d3.forceX<SimNode>((d) => centroids.get(this.nodeFolder(d))?.x ?? cx).strength(strength))
        .force('y', d3.forceY<SimNode>((d) => centroids.get(this.nodeFolder(d))?.y ?? cy).strength(strength));
      return;
    }

    const keyFn = this.groupBy === 'type'
      ? (d: SimNode) => d.type
      : (d: SimNode) => d.namespace;

    const keys = [...new Set(this.nodes.map(keyFn))].sort();
    const centroids = this.groupCentroids(keys, width, height);
    const strength = this.groupBy === 'type' ? 0.15 : 0.08;

    this.simulation
      .force('x', d3.forceX<SimNode>((d) => centroids.get(keyFn(d))?.x ?? cx).strength(strength))
      .force('y', d3.forceY<SimNode>((d) => centroids.get(keyFn(d))?.y ?? cy).strength(strength));
  }

  /** Extract the folder prefix from a node ID (e.g. "packages/node" → "packages"). */
  private nodeFolder(d: SimNode): string {
    const slash = d.id.indexOf('/');
    return slash > 0 ? d.id.substring(0, slash) : '_root';
  }

  /** Render topographic contour hulls around folder groups. */
  private renderFolderHulls(): void {
    if (this.groupBy !== 'folder') {
      this.folderHullGroup.selectAll('*').remove();
      return;
    }

    /* Group visible nodes by folder */
    const folderNodes = new Map<string, SimNode[]>();
    for (const n of this.nodes) {
      if (!this.isNodeVisible(n) || n.x == null || n.y == null) continue;
      const folder = this.nodeFolder(n);
      let arr = folderNodes.get(folder);
      if (!arr) { arr = []; folderNodes.set(folder, arr); }
      arr.push(n);
    }

    /* Palette of warm, muted territory colors (Alpine Cartographic aesthetic) */
    const hullColors = [
      'rgba(212, 168, 83, 0.08)',   /* amber */
      'rgba(107, 158, 135, 0.08)',  /* sage */
      'rgba(196, 93, 75, 0.08)',    /* terracotta */
      'rgba(130, 140, 180, 0.08)',  /* slate blue */
      'rgba(180, 150, 100, 0.08)',  /* sand */
      'rgba(150, 120, 170, 0.08)',  /* lavender */
      'rgba(100, 160, 160, 0.08)',  /* teal */
      'rgba(170, 130, 90, 0.08)',   /* sienna */
    ];
    const borderColors = [
      'rgba(212, 168, 83, 0.25)',
      'rgba(107, 158, 135, 0.25)',
      'rgba(196, 93, 75, 0.25)',
      'rgba(130, 140, 180, 0.25)',
      'rgba(180, 150, 100, 0.25)',
      'rgba(150, 120, 170, 0.25)',
      'rgba(100, 160, 160, 0.25)',
      'rgba(170, 130, 90, 0.25)',
    ];

    const folders = [...folderNodes.keys()].sort();
    const padding = 35; /* Inflate hull outward by this many pixels */

    interface HullDatum {
      folder: string;
      path: string;
      cx: number;
      cy: number;
      fill: string;
      stroke: string;
    }
    const hullData: HullDatum[] = [];

    for (let fi = 0; fi < folders.length; fi++) {
      const folder = folders[fi];
      const nodes = folderNodes.get(folder)!;
      const fill = hullColors[fi % hullColors.length];
      const stroke = borderColors[fi % borderColors.length];

      /* Centroid */
      let cx = 0, cy = 0;
      for (const n of nodes) { cx += n.x!; cy += n.y!; }
      cx /= nodes.length;
      cy /= nodes.length;

      if (nodes.length === 1) {
        /* Single node — draw a circle */
        const r = padding + nodes[0].radius;
        const n = nodes[0];
        // Approximate circle as path for consistent rendering
        hullData.push({
          folder,
          path: `M ${n.x! - r},${n.y!} A ${r},${r} 0 1,0 ${n.x! + r},${n.y!} A ${r},${r} 0 1,0 ${n.x! - r},${n.y!} Z`,
          cx: n.x!, cy: n.y!,
          fill, stroke,
        });
      } else if (nodes.length === 2) {
        /* Two nodes — draw an organic capsule with semicircle end caps */
        const [a, b] = nodes;
        const dx = b.x! - a.x!;
        const dy = b.y! - a.y!;
        const dist = Math.sqrt(dx * dx + dy * dy) || 1;
        const ux = dx / dist;            /* unit vector along a→b */
        const uy = dy / dist;
        const px = -uy * padding;         /* perpendicular */
        const py = ux * padding;
        /* Generate points: side A, semicircle around b, side B, semicircle around a */
        const capsulePts: [number, number][] = [
          [a.x! + px, a.y! + py],
          [b.x! + px, b.y! + py],
          [b.x! + ux * padding * 0.7 + px * 0.5, b.y! + uy * padding * 0.7 + py * 0.5],
          [b.x! + ux * padding, b.y! + uy * padding],
          [b.x! + ux * padding * 0.7 - px * 0.5, b.y! + uy * padding * 0.7 - py * 0.5],
          [b.x! - px, b.y! - py],
          [a.x! - px, a.y! - py],
          [a.x! - ux * padding * 0.7 - px * 0.5, a.y! - uy * padding * 0.7 - py * 0.5],
          [a.x! - ux * padding, a.y! - uy * padding],
          [a.x! - ux * padding * 0.7 + px * 0.5, a.y! - uy * padding * 0.7 + py * 0.5],
        ];
        const lineGen = d3.line().curve(d3.curveCatmullRomClosed.alpha(0.5));
        const pathStr = lineGen(capsulePts);
        if (pathStr) {
          hullData.push({ folder, path: pathStr, cx, cy, fill, stroke });
        }
      } else {
        /* 3+ nodes — convex hull with padding */
        const points: [number, number][] = nodes.map((n) => [n.x!, n.y!]);
        const hull = d3.polygonHull(points);
        if (hull) {
          /* Inflate the hull outward from centroid */
          const inflated = hull.map((p) => {
            const vx = p[0] - cx;
            const vy = p[1] - cy;
            const len = Math.sqrt(vx * vx + vy * vy) || 1;
            return [p[0] + (vx / len) * padding, p[1] + (vy / len) * padding] as [number, number];
          });
          /* Use cardinal closed curve for organic topo feel */
          const lineGen = d3.line().curve(d3.curveCatmullRomClosed.alpha(0.5));
          const pathStr = lineGen(inflated);
          if (pathStr) {
            hullData.push({ folder, path: pathStr, cx, cy, fill, stroke });
          }
        }
      }
    }

    /* ── Render hull paths ── */
    const hullSel = this.folderHullGroup
      .selectAll<SVGPathElement, HullDatum>('.folder-hull')
      .data(hullData, (d) => d.folder);

    hullSel.exit().remove();

    const enterHull = hullSel.enter()
      .append('path')
      .attr('class', 'folder-hull');

    enterHull.merge(hullSel)
      .attr('d', (d) => d.path)
      .attr('fill', (d) => d.fill)
      .attr('stroke', (d) => d.stroke)
      .attr('stroke-width', 1)
      .attr('stroke-dasharray', '4,3');

    /* ── Render folder labels ── */
    const labelSel = this.folderHullGroup
      .selectAll<SVGTextElement, HullDatum>('.folder-label')
      .data(hullData, (d) => d.folder);

    labelSel.exit().remove();

    const enterLabel = labelSel.enter()
      .append('text')
      .attr('class', 'folder-label');

    enterLabel.merge(labelSel)
      .attr('x', (d) => d.cx)
      .attr('y', (d) => d.cy)
      .text((d) => d.folder);
  }

  /** Change the grouping mode and reheat the simulation. */
  setGroupBy(mode: 'none' | 'type' | 'namespace' | 'tag' | 'folder'): void {
    this.groupBy = mode;
    const { width, height } = this.dimensions();
    this.applyGroupForces(width, height);
    this.reheat(0.8);
  }

  private reheat(alpha: number): void {
    this.simulation.alpha(alpha).restart();
  }
}
