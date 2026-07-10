import type {
  BridgeInfo,
  DoctorReport,
  GraphResponse,
  NamespaceInfo,
  SearchResult,
  SummaryInfo,
  WarrenMountResponse,
  WarrenStatusResponse,
  WarrensResponse,
} from './types';

/**
 * Throws an Error carrying the server's JSON `{error}` message when the
 * response body has one, so warren refusals (collisions, not-mounted, …)
 * surface with the same message quality as the CLI.
 */
async function throwApiError(res: Response, fallback: string): Promise<never> {
  let msg = `${fallback}: ${res.status} ${res.statusText}`;
  try {
    const data = (await res.json()) as { error?: string };
    if (data && typeof data.error === 'string' && data.error !== '') msg = data.error;
  } catch {
    // Body was not JSON — keep the fallback message.
  }
  throw new Error(msg);
}

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

export async function fetchGraphAll(includeSuperseded = false): Promise<GraphResponse> {
  const params = new URLSearchParams();
  if (includeSuperseded) params.set('include_superseded', 'true');
  const qs = params.toString();
  const url = `/api/graph/_all${qs ? '?' + qs : ''}`;
  const res = await fetch(url);
  if (!res.ok) throw new Error(`fetchGraphAll: ${res.status}`);
  return res.json();
}

export async function fetchBridges(): Promise<BridgeInfo[]> {
  const res = await fetch('/api/bridges');
  if (!res.ok) throw new Error(`fetchBridges: ${res.status}`);
  const data = await res.json();
  return data.bridges ?? [];
}

export async function fetchWarrens(): Promise<WarrensResponse> {
  const res = await fetch('/api/warrens');
  if (!res.ok) throw new Error(`fetchWarrens: ${res.status}`);
  return res.json();
}

export async function fetchWarrenGraph(warrenId: string): Promise<GraphResponse> {
  const res = await fetch(`/api/warren/${encodeURIComponent(warrenId)}/graph`);
  if (!res.ok) throw new Error(`fetchWarrenGraph: ${res.status}`);
  return res.json();
}

export async function fetchWarrenStatus(warrenId: string): Promise<WarrenStatusResponse> {
  const res = await fetch(`/api/warren/${encodeURIComponent(warrenId)}/status`);
  if (!res.ok) return throwApiError(res, `fetchWarrenStatus ${warrenId}`);
  return res.json();
}

export async function fetchDoctorWorkspace(): Promise<DoctorReport> {
  const res = await fetch('/api/doctor/workspace');
  if (!res.ok) return throwApiError(res, 'fetchDoctorWorkspace');
  return res.json();
}

/**
 * Asks the server to reload its warren state (mounts, routes, runtime
 * bridges) from disk. Shared by the toolbar refresh button and the warren
 * panel; throws with the server's message so callers can surface failures.
 */
export async function refreshWarrenState(warrenId: string): Promise<void> {
  const res = await fetch(`/api/warren/${encodeURIComponent(warrenId)}/refresh`, {
    method: 'POST',
  });
  if (!res.ok) return throwApiError(res, `refreshWarrenState ${warrenId}`);
}

async function postWarrenMountChange(
  warrenId: string,
  verb: 'mount' | 'unmount',
  projects: string[],
): Promise<WarrenMountResponse> {
  const res = await fetch(`/api/warren/${encodeURIComponent(warrenId)}/${verb}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ projects }),
  });
  if (!res.ok) return throwApiError(res, `${verb} ${warrenId}`);
  return res.json();
}

export async function mountWarrenProjects(
  warrenId: string,
  projects: string[],
): Promise<WarrenMountResponse> {
  return postWarrenMountChange(warrenId, 'mount', projects);
}

export async function unmountWarrenProjects(
  warrenId: string,
  projects: string[],
): Promise<WarrenMountResponse> {
  return postWarrenMountChange(warrenId, 'unmount', projects);
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
