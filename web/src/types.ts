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
  tags?: string[];
  provenance?: Provenance;
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
  class: 'structural' | 'behavioral' | 'bridge';
}

export interface GraphResponse {
  namespace: string;
  nodes: APINode[];
  edges: APIEdge[];
  node_count: number;
  edge_count: number;
  heat_pairs?: APIHeatPair[];
  namespaces?: string[];
  /** Warren graph view only: project IDs skipped from the view. */
  skipped?: string[];
  /** Warren graph view only: why each skipped project was skipped. */
  skipped_reasons?: Record<string, string>;
}

export interface Provenance {
  source?: 'local' | 'warren_mount' | 'warren_materialized' | 'local_alias' | string;
  warren_id?: string;
  project_id?: string;
  vault_id?: string;
  marmot_dir?: string;
  qualified_id?: string;
  editable?: boolean;
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

export interface WorkspaceWarren {
  path: string;
  active_projects?: string[];
  editable_projects?: string[];
  materialized_projects?: string[];
  /** Computed: projects whose vault_id matches this workspace (served live; never mounted). */
  identified_projects?: string[];
  /** Computed: whether the registered checkout directory still exists (CLI REACHABLE). */
  reachable?: boolean;
}

export interface WarrensResponse {
  warrens: Record<string, WorkspaceWarren>;
  /** This workspace's configured vault_id (absent when the vault has none). */
  local_vault_id?: string;
}

/** One warren project's workspace state (GET /api/warren/{id}/status). */
export interface ProjectStatus {
  warren_id: string;
  warren_path?: string;
  project_id: string;
  path: string;
  vault_id?: string;
  registered: boolean;
  active: boolean;
  editable: boolean;
  materialized: boolean;
  available: boolean;
  /** Identity: this project IS the workspace vault (served live, read-only, never routed). */
  self_alias?: boolean;
}

export interface WarrenStatusResponse {
  warren_id: string;
  path: string;
  projects: ProjectStatus[];
}

/** Response of POST /api/warren/{id}/mount and /unmount. */
export interface WarrenMountResponse {
  warren_id: string;
  action: 'mounted' | 'unmounted';
  projects: string[];
  status: string;
}

/** One workspace doctor finding (GET /api/doctor/workspace). */
export interface DoctorIssue {
  severity: 'error' | 'warning' | 'info' | string;
  code: string;
  message: string;
  project_id?: string;
  path?: string;
}

export interface DoctorReport {
  warren_id?: string;
  issues?: DoctorIssue[];
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
  provenance?: Provenance;
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
