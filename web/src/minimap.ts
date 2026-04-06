import { nodeColor } from './types';

/* ------------------------------------------------------------------ */
/*  Minimap — small overview of the entire graph with viewport rect   */
/* ------------------------------------------------------------------ */

interface MinimapNode {
  x: number;
  y: number;
  type: string;
}

interface MinimapTransform {
  x: number;
  y: number;
  k: number;
}

export class Minimap {
  private canvas: HTMLCanvasElement;
  private ctx: CanvasRenderingContext2D;
  private width: number;
  private height: number;

  constructor(container: HTMLElement) {
    this.width = container.clientWidth || 160;
    this.height = container.clientHeight || 100;

    this.canvas = document.createElement('canvas');
    this.canvas.width = this.width;
    this.canvas.height = this.height;
    this.canvas.style.width = '100%';
    this.canvas.style.height = '100%';
    this.canvas.style.display = 'block';

    container.appendChild(this.canvas);

    const ctx = this.canvas.getContext('2d');
    if (!ctx) throw new Error('Minimap: failed to get 2d context');
    this.ctx = ctx;
  }

  update(
    nodes: MinimapNode[],
    transform: MinimapTransform,
    viewportWidth: number,
    viewportHeight: number,
  ): void {
    const ctx = this.ctx;
    const w = this.width;
    const h = this.height;

    /* Clear */
    ctx.clearRect(0, 0, w, h);

    if (nodes.length === 0) return;

    /* Bounding box of all nodes */
    let minX = Infinity;
    let minY = Infinity;
    let maxX = -Infinity;
    let maxY = -Infinity;

    for (const n of nodes) {
      if (n.x < minX) minX = n.x;
      if (n.y < minY) minY = n.y;
      if (n.x > maxX) maxX = n.x;
      if (n.y > maxY) maxY = n.y;
    }

    const padding = 20;
    const graphW = maxX - minX || 1;
    const graphH = maxY - minY || 1;

    const scaleX = (w - padding * 2) / graphW;
    const scaleY = (h - padding * 2) / graphH;
    const scale = Math.min(scaleX, scaleY);

    /* Center offset so the graph is centered in the minimap */
    const offsetX = (w - graphW * scale) / 2;
    const offsetY = (h - graphH * scale) / 2;

    const toMiniX = (x: number) => (x - minX) * scale + offsetX;
    const toMiniY = (y: number) => (y - minY) * scale + offsetY;

    /* Draw nodes as tiny dots */
    for (const n of nodes) {
      const mx = toMiniX(n.x);
      const my = toMiniY(n.y);

      ctx.beginPath();
      ctx.arc(mx, my, 2, 0, Math.PI * 2);
      ctx.fillStyle = nodeColor(n.type);
      ctx.globalAlpha = 0.85;
      ctx.fill();
    }

    /* Draw viewport rectangle */
    ctx.globalAlpha = 1;

    /* The viewport in graph-space: the inverse of the current transform */
    const vpLeft = -transform.x / transform.k;
    const vpTop = -transform.y / transform.k;
    const vpW = viewportWidth / transform.k;
    const vpH = viewportHeight / transform.k;

    const rx = toMiniX(vpLeft);
    const ry = toMiniY(vpTop);
    const rw = vpW * scale;
    const rh = vpH * scale;

    ctx.strokeStyle = 'rgba(76, 120, 168, 0.8)';
    ctx.lineWidth = 1.5;
    ctx.fillStyle = 'rgba(76, 120, 168, 0.1)';
    ctx.fillRect(rx, ry, rw, rh);
    ctx.strokeRect(rx, ry, rw, rh);
  }
}
