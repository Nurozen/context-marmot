import type { APIHeatPair } from './types';

/* ------------------------------------------------------------------ */
/*  HeatOverlay — per-node heat scoring and glow effects              */
/* ------------------------------------------------------------------ */

export class HeatOverlay {
  private enabled = false;
  private nodeHeat: Map<string, number> = new Map();

  setData(pairs: APIHeatPair[]): void {
    this.nodeHeat.clear();

    /* Accumulate per-node heat by summing weights of all pairs */
    for (const pair of pairs) {
      this.nodeHeat.set(pair.a, (this.nodeHeat.get(pair.a) || 0) + pair.weight);
      this.nodeHeat.set(pair.b, (this.nodeHeat.get(pair.b) || 0) + pair.weight);
    }

    /* Normalize to 0-1 */
    const max = Math.max(...this.nodeHeat.values(), 0.001);
    for (const [id, val] of this.nodeHeat) {
      this.nodeHeat.set(id, val / max);
    }
  }

  enable(): void {
    this.enabled = true;
  }

  disable(): void {
    this.enabled = false;
  }

  isEnabled(): boolean {
    return this.enabled;
  }

  getHeat(nodeId: string): number {
    if (!this.enabled) return 0;
    return this.nodeHeat.get(nodeId) || 0;
  }

  /** Returns a CSS filter string for glow effect: higher heat = stronger glow */
  getGlowStyle(nodeId: string): string {
    const heat = this.getHeat(nodeId);
    if (heat <= 0) return '';
    const blur = 2 + heat * 8;
    return `drop-shadow(0 0 ${blur}px rgba(255, 140, 0, ${heat * 0.8}))`;
  }
}
