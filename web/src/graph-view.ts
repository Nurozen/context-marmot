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
  class: 'structural' | 'behavioral';
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

  private linkGroup!: d3.Selection<SVGGElement, unknown, HTMLElement, unknown>;
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
  private edgeClassFilter: 'all' | 'structural' | 'behavioral' = 'all';
  private heatEnabled = false;
  private heatPairs: APIHeatPair[] = [];
  private groupBy: 'none' | 'type' | 'namespace' | 'tag' = 'type';

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
    this.linkGroup = this.container.append('g').attr('class', 'links');
    this.heatGroup = this.container.append('g').attr('class', 'heat-links');
    this.nodeGroup = this.container.append('g').attr('class', 'nodes');

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

    /* Build SimNodes */
    this.nodes = data.nodes.map((n) => ({
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
    }));

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

    /* Populate visible types */
    this.visibleTypes = new Set(data.nodes.map((n) => n.type));

    /* Heat pairs */
    this.heatPairs = data.heat_pairs ?? [];

    /* Dynamic charge based on node count */
    const charge = this.nodes.length > 200 ? -150 : this.nodes.length > 80 ? -200 : -300;

    /* Reconfigure simulation */
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
      .force('center', d3.forceCenter(width / 2, height / 2).strength(0.5))
      .force('collide', d3.forceCollide<SimNode>().radius((d) => d.radius + 2));

    this.applyGroupForces(width, height);

    this.simulation.alpha(1).restart();

    this.renderLinks();
    this.renderHeatLinks();
    this.renderNodes();
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

  filterByEdgeClass(cls: 'all' | 'structural' | 'behavioral'): void {
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
      .data(this.links, (d) => `${(d.source as SimNode).id ?? d.source}-${(d.target as SimNode).id ?? d.target}-${d.relation}`);

    linkSel.exit().remove();

    const enter = linkSel
      .enter()
      .append('line')
      .attr('class', (d) => `link ${d.class}`)
      .attr('stroke', '#555')
      .attr('stroke-opacity', (d) => (d.class === 'behavioral' ? 0.4 : 0.6))
      .attr('stroke-dasharray', (d) => (d.class === 'behavioral' ? '6,4' : null))
      .attr('marker-end', 'url(#arrow)');

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

    const heatData = this.heatPairs.filter(
      (p) => nodeMap.has(p.a) && nodeMap.has(p.b),
    );

    const maxW = d3.max(heatData, (d) => d.weight) ?? 1;

    const heatSel = this.heatGroup
      .selectAll<SVGLineElement, APIHeatPair>('.heat-link')
      .data(heatData, (d) => `${d.a}-${d.b}`);

    heatSel.exit().remove();

    const enter = heatSel
      .enter()
      .append('line')
      .attr('class', 'heat-link visible')
      .attr('stroke-width', (d) => 1.5 + (d.weight / maxW) * 4);

    enter.merge(heatSel).classed('visible', true);
  }

  private renderNodes(): void {
    const nodeSel = this.nodeGroup
      .selectAll<SVGGElement, SimNode>('.node')
      .data(this.nodes, (d) => d.id);

    nodeSel.exit().transition().duration(200).attr('opacity', 0).remove();

    const enter = nodeSel.enter().append('g').attr('class', (d) => {
      let cls = 'node';
      if (d.is_stale) cls += ' stale';
      if (d.superseded_by) cls += ' superseded';
      return cls;
    });

    /* Circle */
    enter
      .append('circle')
      .attr('r', 0)
      .attr('fill', (d) => nodeColor(d.type))
      .attr('opacity', (d) => (d.superseded_by ? 0.4 : 1))
      .transition()
      .duration(400)
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

    /* Click */
    enter.on('click', (event: MouseEvent, d: SimNode) => {
      event.stopPropagation();
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
  }

  private clearHighlight(): void {
    this.nodeGroup.selectAll('.node').classed('dimmed', false);
    this.linkGroup.selectAll('.link').classed('dimmed', false).classed('highlighted', false);
  }

  /* ---------------------------------------------------------------- */
  /*  Tooltip                                                          */
  /* ---------------------------------------------------------------- */

  private showTooltip(event: MouseEvent, d: SimNode): void {
    this.tooltip.html(
      `<div class="tt-title">${shortLabel(d.id)}</div>` +
        `<div class="tt-type">${d.type} &middot; ${d.edge_count} edges</div>` +
        `<div class="tt-summary">${d.summary || d.id}</div>`,
    );
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

    if (this.groupBy === 'none') {
      this.simulation
        .force('x', d3.forceX<SimNode>(cx).strength(0.04))
        .force('y', d3.forceY<SimNode>(cy).strength(0.04));
      return;
    }

    /* Tag-based grouping: nodes may belong to multiple tags */
    if (this.groupBy === 'tag') {
      const allTags = [...new Set(this.nodes.flatMap((n) => n.tags))].sort();
      if (allTags.length === 0) {
        /* No tags at all — fall back to center pull */
        this.simulation
          .force('x', d3.forceX<SimNode>(cx).strength(0.04))
          .force('y', d3.forceY<SimNode>(cy).strength(0.04));
        return;
      }
      const tagCentroids = this.groupCentroids(allTags, width, height);
      const strength = 0.15;

      this.simulation
        .force('x', d3.forceX<SimNode>((d) => {
          if (d.tags.length === 0) return cx;
          let sx = 0;
          for (const t of d.tags) sx += tagCentroids.get(t)?.x ?? cx;
          return sx / d.tags.length;
        }).strength(strength))
        .force('y', d3.forceY<SimNode>((d) => {
          if (d.tags.length === 0) return cy;
          let sy = 0;
          for (const t of d.tags) sy += tagCentroids.get(t)?.y ?? cy;
          return sy / d.tags.length;
        }).strength(strength));
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

  /** Change the grouping mode and reheat the simulation. */
  setGroupBy(mode: 'none' | 'type' | 'namespace' | 'tag'): void {
    this.groupBy = mode;
    const { width, height } = this.dimensions();
    this.applyGroupForces(width, height);
    this.reheat(0.8);
  }

  private reheat(alpha: number): void {
    this.simulation.alpha(alpha).restart();
  }
}
