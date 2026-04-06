import type { GraphResponse, NamespaceInfo, SearchResult, SummaryInfo } from './types';

export async function fetchGraph(
  namespace: string,
  includeSuperseded = false,
): Promise<GraphResponse> {
  const params = new URLSearchParams();
  if (includeSuperseded) params.set('include_superseded', 'true');
  const url = `/api/graph/${encodeURIComponent(namespace)}?${params}`;
  const res = await fetch(url);
  if (!res.ok) throw new Error(`Failed to fetch graph: ${res.statusText}`);
  return res.json();
}

export async function fetchNamespaces(): Promise<NamespaceInfo[]> {
  const res = await fetch('/api/namespaces');
  if (!res.ok) throw new Error(`Failed to fetch namespaces: ${res.statusText}`);
  const data = await res.json();
  return data.namespaces;
}

export async function searchNodes(
  query: string,
  namespace?: string,
  limit = 20,
): Promise<SearchResult[]> {
  const params = new URLSearchParams({ q: query, limit: String(limit) });
  if (namespace) params.set('ns', namespace);
  const res = await fetch(`/api/search?${params}`);
  if (!res.ok) throw new Error(`Search failed: ${res.statusText}`);
  const data = await res.json();
  return data.results;
}

export async function fetchSummary(namespace: string): Promise<SummaryInfo> {
  const res = await fetch(`/api/summary/${encodeURIComponent(namespace)}`);
  if (!res.ok) throw new Error(`Failed to fetch summary: ${res.statusText}`);
  return res.json();
}

export async function updateNode(
  id: string,
  data: { summary?: string; context?: string },
): Promise<void> {
  const res = await fetch(`/api/node/${encodeURIComponent(id)}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(data),
  });
  if (!res.ok) throw new Error(`Failed to update node: ${res.statusText}`);
}
