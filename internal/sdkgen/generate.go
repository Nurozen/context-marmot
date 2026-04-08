// Package sdkgen generates a self-contained TypeScript SDK file from
// ContextMarmot's MCP tool schemas. The generated file includes all
// interfaces, types, and a MarmotClient class that wraps HTTP calls to
// the existing REST-like API.
package sdkgen

import (
	"fmt"
	"strings"
)

// version is the semantic version of the generated SDK.
const version = "0.1.3"

// Generate returns a complete, self-contained TypeScript file as a string.
// The generated code includes all MCP tool input/output interfaces, domain
// types, and a MarmotClient class. baseURL is baked into the file as the
// default constructor argument.
func Generate(baseURL string) string {
	if baseURL == "" {
		baseURL = "http://localhost:3000"
	}
	// Strip trailing slash for consistency.
	baseURL = strings.TrimRight(baseURL, "/")

	var b strings.Builder

	writeHeader(&b)
	writeDomainTypes(&b)
	writeToolInterfaces(&b)
	writeClient(&b, baseURL)

	return b.String()
}

// ---------------------------------------------------------------------------
// Header
// ---------------------------------------------------------------------------

func writeHeader(b *strings.Builder) {
	fmt.Fprintf(b, `// ----------------------------------------------------------------
// ContextMarmot TypeScript SDK  v%s
// Auto-generated — do not edit by hand.
// ----------------------------------------------------------------

`, version)
}

// ---------------------------------------------------------------------------
// Domain types (MarmotNode, MarmotEdge, GraphData, HeatPair, BridgeInfo)
// ---------------------------------------------------------------------------

func writeDomainTypes(b *strings.Builder) {
	b.WriteString(`// ---------------------------------------------------------------------------
// Domain types
// ---------------------------------------------------------------------------

/** Source-code location metadata attached to a knowledge-graph node. */
export interface NodeSource {
  path: string;
  lines?: [number, number];
  hash?: string;
}

/** A single node in the ContextMarmot knowledge graph. */
export interface MarmotNode {
  id: string;
  type: string;
  namespace: string;
  status: 'active' | 'superseded' | 'archived';
  valid_from?: string;
  valid_until?: string;
  superseded_by?: string;
  summary: string;
  context: string;
  source?: NodeSource;
  edges: MarmotEdge[];
  edge_count: number;
  is_stale: boolean;
  tags: string[];
}

/** A directed, typed edge between two nodes. */
export interface MarmotEdge {
  source: string;
  target: string;
  relation: string;
  class: 'structural' | 'behavioral' | 'bridge';
}

/** Full graph payload returned by the graph endpoints. */
export interface GraphData {
  nodes: MarmotNode[];
  edges: MarmotEdge[];
}

/** A co-access frequency pair from the heat map. */
export interface HeatPair {
  a: string;
  b: string;
  weight: number;
  hits: number;
  last?: string;
}

/** A cross-namespace or cross-vault bridge definition. */
export interface BridgeInfo {
  source: string;
  target: string;
  allowed_relations: string[];
  is_cross_vault: boolean;
}

/** A single semantic-search result. */
export interface SearchResult {
  node_id: string;
  score: number;
  summary: string;
  type: string;
  namespace: string;
}

/** Information about a namespace. */
export interface NamespaceInfo {
  name: string;
  node_count: number;
  has_summary: boolean;
}

`)
}

// ---------------------------------------------------------------------------
// MCP tool input / output interfaces
// ---------------------------------------------------------------------------

func writeToolInterfaces(b *strings.Builder) {
	b.WriteString(`// ---------------------------------------------------------------------------
// MCP tool input / output interfaces
// ---------------------------------------------------------------------------

// -- context_query --------------------------------------------------------

/** Input for the context_query tool. Retrieves relevant context from the knowledge graph. */
export interface QueryInput {
  /** The natural-language query to search for. */
  query: string;
  /** Maximum traversal depth (default: 2). */
  depth?: number;
  /** Token budget for the returned context (default: 4096). */
  budget?: number;
  /** Traversal mode: adjacency (fast, shallow) or deep (thorough). */
  mode?: 'adjacency' | 'deep';
  /** Whether to include superseded nodes in results. */
  include_superseded?: boolean;
}

/** Output of the context_query tool. */
export interface QueryResult {
  /** The assembled XML context string. */
  context: string;
}

// -- context_write --------------------------------------------------------

/** An edge descriptor used when writing a node. */
export interface WriteEdge {
  /** Target node ID. */
  target: string;
  /** Relation type between source and target. */
  relation:
    | 'contains'
    | 'imports'
    | 'extends'
    | 'implements'
    | 'calls'
    | 'reads'
    | 'writes'
    | 'references'
    | 'cross_project'
    | 'associated';
}

/** Source-code location for a node being written. */
export interface WriteSource {
  path?: string;
  lines?: [number, number];
  hash?: string;
}

/** Input for the context_write tool. Creates or updates a knowledge-graph node. */
export interface WriteInput {
  /** Unique node identifier. */
  id: string;
  /** The semantic type of the node. */
  type:
    | 'function'
    | 'module'
    | 'class'
    | 'concept'
    | 'decision'
    | 'reference'
    | 'composite';
  /** Namespace to place the node in. */
  namespace?: string;
  /** One-line summary of the node. */
  summary?: string;
  /** Full context / body content. */
  context?: string;
  /** Tags for categorisation and filtering. */
  tags?: string[];
  /** Outbound edges from this node. */
  edges?: WriteEdge[];
  /** Source-code location metadata. */
  source?: WriteSource;
}

/** Output of the context_write tool. */
export interface WriteResult {
  /** The canonical node ID (may include namespace prefix). */
  node_id: string;
  /** Content hash of the persisted node. */
  hash: string;
  /** Whether the node was newly created or updated. */
  status: 'created' | 'updated';
}

// -- context_verify -------------------------------------------------------

/** Input for the context_verify tool. Checks node staleness and integrity. */
export interface VerifyInput {
  /** Specific node IDs to verify. Omit to verify all nodes. */
  node_ids?: string[];
  /** The type of check to perform. */
  check?: 'staleness' | 'integrity' | 'all';
}

/** A single verification issue found by context_verify. */
export interface VerifyIssue {
  /** The node ID that has the issue. */
  node_id: string;
  /** Machine-readable issue type (e.g. "stale_source", "missing_hash"). */
  type: string;
  /** Human-readable description of the issue. */
  message: string;
  /** Severity level. */
  severity: 'error' | 'warning';
}

/** Output of the context_verify tool. */
export interface VerifyResult {
  /** List of issues found. */
  issues: VerifyIssue[];
  /** Total number of issues. */
  total: number;
}

// -- context_delete -------------------------------------------------------

/** Input for the context_delete tool. Removes or supersedes a node. */
export interface DeleteInput {
  /** The node ID to delete. */
  id: string;
  /** If provided, marks the node as superseded by this ID instead of deleting. */
  superseded_by?: string;
}

/** Output of the context_delete tool. */
export interface DeleteResult {
  /** The deleted/superseded node ID. */
  node_id: string;
  /** Result status (e.g. "deleted", "superseded"). */
  status: string;
  /** The node that supersedes this one, if applicable. */
  superseded_by?: string;
}

// -- context_tag ----------------------------------------------------------

/** Input for the context_tag tool. Bulk-tags nodes matching a query. */
export interface TagInput {
  /** Search query to find nodes to tag. */
  query: string;
  /** The tag to apply. */
  tag: string;
  /** Optional namespace filter. */
  namespace?: string;
  /** Maximum number of nodes to tag. */
  limit?: number;
}

/** Output of the context_tag tool. */
export interface TagResult {
  /** The tag that was applied. */
  tag: string;
  /** IDs of nodes that were tagged. */
  tagged_ids: string[];
  /** Number of nodes tagged. */
  count: number;
}

`)
}

// ---------------------------------------------------------------------------
// MarmotClient class
// ---------------------------------------------------------------------------

func writeClient(b *strings.Builder, baseURL string) {
	fmt.Fprintf(b, `// ---------------------------------------------------------------------------
// MarmotClient
// ---------------------------------------------------------------------------

/** Error thrown when the ContextMarmot API returns a non-OK response. */
export class MarmotError extends Error {
  constructor(
    public readonly status: number,
    public readonly body: string,
  ) {
    super(` + "`" + `MarmotClient request failed (${status}): ${body}` + "`" + `);
    this.name = 'MarmotError';
  }
}

/**
 * A typed HTTP client for the ContextMarmot REST API.
 *
 * Tool methods (query, write, verify, delete, tag) POST to ` + "`" + `/api/sdk/{tool}` + "`" + `.
 * Graph read methods use the existing GET endpoints.
 */
export class MarmotClient {
  private readonly baseURL: string;

  constructor(baseURL: string = '%s') {
    this.baseURL = baseURL.replace(/\/$/,  '');
  }

  // -----------------------------------------------------------------------
  // Internal helpers
  // -----------------------------------------------------------------------

  private async request<T>(method: string, path: string, body?: unknown): Promise<T> {
    const url = this.baseURL + path;
    const init: RequestInit = {
      method,
      headers: { 'Content-Type': 'application/json' },
    };
    if (body !== undefined) {
      init.body = JSON.stringify(body);
    }
    const res = await fetch(url, init);
    if (!res.ok) {
      const text = await res.text();
      throw new MarmotError(res.status, text);
    }
    return (await res.json()) as T;
  }

  // -----------------------------------------------------------------------
  // MCP tool methods — POST /api/sdk/{tool}
  // -----------------------------------------------------------------------

  /**
   * Query the knowledge graph for relevant context.
   *
   * Performs a semantic search and graph traversal, returning assembled XML
   * context that fits within the specified token budget.
   */
  async query(input: QueryInput): Promise<QueryResult> {
    return this.request<QueryResult>('POST', '/api/sdk/context_query', input);
  }

  /**
   * Create or update a knowledge-graph node.
   *
   * If a node with the given ID already exists it is updated; otherwise a
   * new node is created. Returns the canonical node ID, content hash, and
   * whether the operation was a create or update.
   */
  async write(input: WriteInput): Promise<WriteResult> {
    return this.request<WriteResult>('POST', '/api/sdk/context_write', input);
  }

  /**
   * Verify node staleness and integrity.
   *
   * Checks whether source files have changed since the node was last
   * updated (staleness) and whether node hashes are consistent (integrity).
   */
  async verify(input: VerifyInput): Promise<VerifyResult> {
    return this.request<VerifyResult>('POST', '/api/sdk/context_verify', input);
  }

  /**
   * Delete or supersede a knowledge-graph node.
   *
   * If ` + "`" + `superseded_by` + "`" + ` is provided the node is marked as superseded rather
   * than being permanently removed.
   */
  async delete(input: DeleteInput): Promise<DeleteResult> {
    return this.request<DeleteResult>('POST', '/api/sdk/context_delete', input);
  }

  /**
   * Bulk-tag nodes that match a search query.
   *
   * Finds nodes matching the query and applies the specified tag to each.
   */
  async tag(input: TagInput): Promise<TagResult> {
    return this.request<TagResult>('POST', '/api/sdk/context_tag', input);
  }

  // -----------------------------------------------------------------------
  // Graph read methods — existing GET endpoints
  // -----------------------------------------------------------------------

  /**
   * Retrieve the full graph for a namespace, or the entire graph across
   * all namespaces when called without arguments.
   *
   * @param namespace - A specific namespace, or omit / pass ` + "`" + `undefined` + "`" + ` for all.
   */
  async getGraph(namespace?: string): Promise<GraphData> {
    const path = namespace ? ` + "`" + `/api/graph/${encodeURIComponent(namespace)}` + "`" + ` : '/api/graph/_all';
    const raw = await this.request<{ nodes: MarmotNode[]; edges: MarmotEdge[] }>('GET', path);
    return { nodes: raw.nodes, edges: raw.edges };
  }

  /**
   * Fetch a single node by namespace and ID.
   *
   * @param namespace - The node's namespace.
   * @param id        - The node's ID (without namespace prefix).
   */
  async getNode(namespace: string, id: string): Promise<MarmotNode> {
    // id may contain slashes (e.g. "auth/login") which must remain literal
    // path segments — only encode each segment individually.
    const encodedId = id.split('/').map(encodeURIComponent).join('/');
    return this.request<MarmotNode>(
      'GET',
      ` + "`" + `/api/node/${encodeURIComponent(namespace)}/${encodedId}` + "`" + `,
    );
  }

  /**
   * Perform a semantic search across the knowledge graph.
   *
   * @param query - The natural-language search query.
   */
  async search(query: string): Promise<SearchResult[]> {
    const raw = await this.request<{ results: SearchResult[] }>(
      'GET',
      ` + "`" + `/api/search?q=${encodeURIComponent(query)}` + "`" + `,
    );
    return raw.results;
  }

  /**
   * List all known namespaces.
   *
   * @returns An array of namespace name strings.
   */
  async getNamespaces(): Promise<string[]> {
    const raw = await this.request<{ namespaces: NamespaceInfo[] }>('GET', '/api/namespaces');
    return raw.namespaces.map((ns) => ns.name);
  }

  /**
   * Retrieve heat-map co-access pairs for a namespace.
   *
   * @param namespace - The namespace to get heat data for.
   */
  async getHeat(namespace: string): Promise<HeatPair[]> {
    const raw = await this.request<{ pairs: HeatPair[] }>(
      'GET',
      ` + "`" + `/api/heat/${encodeURIComponent(namespace)}` + "`" + `,
    );
    return raw.pairs;
  }

  /**
   * List all cross-namespace and cross-vault bridge definitions.
   */
  async getBridges(): Promise<BridgeInfo[]> {
    const raw = await this.request<{ bridges: BridgeInfo[] }>('GET', '/api/bridges');
    return raw.bridges;
  }
}
`, baseURL)
}
