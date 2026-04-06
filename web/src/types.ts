export interface APINode {
  id: string;
  type: NodeType;
  namespace: string;
  status: string;
  valid_from?: string;
  valid_until?: string;
  superseded_by?: string;
  summary: string;
  context: string;
  source?: APISource;
  edges: APIEdge[];
  edge_count: number;
  is_stale: boolean;
}

export interface APISource {
  path: string;
  lines: [number, number];
  hash: string;
}

export interface APIEdge {
  source: string;
  target: string;
  relation: string;
  class: 'structural' | 'behavioral';
}

export interface GraphResponse {
  namespace: string;
  nodes: APINode[];
  edges: APIEdge[];
  node_count: number;
  edge_count: number;
  heat_pairs?: APIHeatPair[];
}

export interface APIHeatPair {
  a: string;
  b: string;
  weight: number;
  hits: number;
  last?: string;
}

export interface NamespaceInfo {
  name: string;
  node_count: number;
  has_summary: boolean;
}

export interface BridgeInfo {
  source: string;
  target: string;
  allowed_relations: string[];
  is_cross_vault: boolean;
}

export interface SearchResult {
  node_id: string;
  score: number;
  summary: string;
  type: string;
  namespace: string;
}

export interface SummaryInfo {
  namespace: string;
  content: string;
  node_count: number;
  generated_at: string;
}

export type NodeType =
  | 'function'
  | 'module'
  | 'class'
  | 'concept'
  | 'decision'
  | 'reference'
  | 'composite'
  | 'interface'
  | 'type'
  | 'method'
  | 'package'
  | 'summary'
  | string;

/** Colorblind-friendly Tableau 10 derivative palette */
export const NODE_COLORS: Record<string, string> = {
  function:  '#4C78A8',
  method:    '#4C78A8',
  module:    '#72B7B2',
  package:   '#72B7B2',
  class:     '#F58518',
  type:      '#F58518',
  interface: '#FF9DA7',
  concept:   '#E45756',
  decision:  '#54A24B',
  reference: '#EECA3B',
  composite: '#B279A2',
  summary:   '#9D755D',
};

export const DEFAULT_NODE_COLOR = '#999999';

export function nodeColor(type: string): string {
  return NODE_COLORS[type] || DEFAULT_NODE_COLOR;
}
