import {
  fetchBridges,
  fetchDoctorWorkspace,
  fetchWarrens,
  fetchWarrenStatus,
  mountWarrenProjects,
  refreshWarrenState,
  unmountWarrenProjects,
} from './api';
import type { BridgeInfo, DoctorReport, ProjectStatus } from './types';

/**
 * Warren management panel (U5b): lists every registered warren's projects
 * with their STATE / EDITABLE / AVAILABLE columns and an identity badge,
 * offers mount/unmount for non-identity rows, shows the workspace doctor
 * report (badge = error/warning counts), and surfaces refresh/mount
 * failures inline instead of silently swallowing them.
 *
 * The panel is a slide-over anchored under the toolbar; the toolbar button
 * (#warren-toggle) is hidden until at least one warren is registered.
 */
export class WarrenPanel {
  private panel: HTMLElement;
  private body: HTMLElement;
  private errorEl: HTMLElement;
  private toggleBtn: HTMLButtonElement;
  private badge: HTMLElement;
  private onStateChange: () => Promise<void>;

  private warrenIds: string[] = [];
  /** warrenId -> projectId -> skip reason, from the latest warren graph fetch. */
  private skippedReasons = new Map<string, Record<string, string>>();

  constructor(onStateChange: () => Promise<void>) {
    this.panel = document.getElementById('warren-panel') as HTMLElement;
    this.body = document.getElementById('warren-panel-body') as HTMLElement;
    this.errorEl = document.getElementById('warren-panel-error') as HTMLElement;
    this.toggleBtn = document.getElementById('warren-toggle') as HTMLButtonElement;
    this.badge = document.getElementById('warren-badge') as HTMLElement;
    this.onStateChange = onStateChange;

    this.toggleBtn?.addEventListener('click', () => this.toggle());
    document.getElementById('warren-panel-close')?.addEventListener('click', () => this.hide());
  }

  /** Discovers registered warrens; shows the toolbar button when any exist. */
  async init(): Promise<void> {
    try {
      const data = await fetchWarrens();
      this.warrenIds = Object.keys(data.warrens ?? {}).sort((a, b) => a.localeCompare(b));
    } catch {
      this.warrenIds = [];
    }
    if (this.warrenIds.length === 0) return;
    this.toggleBtn?.classList.remove('hidden');
    void this.updateDoctorBadge();
  }

  isVisible(): boolean {
    return !this.panel.classList.contains('hidden');
  }

  toggle(): void {
    if (this.isVisible()) {
      this.hide();
    } else {
      this.show();
    }
  }

  show(): void {
    this.panel.classList.remove('hidden');
    void this.render();
  }

  hide(): void {
    this.panel.classList.add('hidden');
  }

  /**
   * Surfaces a warren operation failure in the panel (opening it if needed)
   * — the replacement for the old best-effort silent swallow.
   */
  reportError(message: string): void {
    this.panel.classList.remove('hidden');
    this.errorEl.textContent = message;
    this.errorEl.hidden = false;
  }

  private clearError(): void {
    this.errorEl.textContent = '';
    this.errorEl.hidden = true;
  }

  /** Called by main.ts after a warren graph load so rows can carry skip tooltips. */
  setSkippedReasons(warrenId: string, reasons: Record<string, string>): void {
    this.skippedReasons.set(warrenId, reasons);
    if (this.isVisible()) void this.render();
  }

  private async updateDoctorBadge(): Promise<DoctorReport | null> {
    try {
      const report = await fetchDoctorWorkspace();
      let errors = 0;
      let warnings = 0;
      for (const issue of report.issues ?? []) {
        if (issue.severity === 'error') errors++;
        else if (issue.severity === 'warning') warnings++;
      }
      if (errors + warnings > 0) {
        this.badge.textContent = `${errors + warnings}`;
        this.badge.classList.toggle('warren-badge-error', errors > 0);
        this.badge.hidden = false;
      } else {
        this.badge.hidden = true;
      }
      return report;
    } catch {
      this.badge.hidden = true;
      return null;
    }
  }

  /** Re-fetches everything the panel shows and rebuilds its DOM. */
  async render(): Promise<void> {
    this.body.innerHTML = '<p class="warren-loading">Loading warren status…</p>';
    let doctor: DoctorReport | null = null;
    let bridges: BridgeInfo[] = [];
    const statuses = new Map<string, ProjectStatus[]>();
    const loadErrors: string[] = [];

    const results = await Promise.allSettled([
      this.updateDoctorBadge(),
      fetchBridges(),
      ...this.warrenIds.map((id) => fetchWarrenStatus(id)),
    ]);
    if (results[0].status === 'fulfilled') {
      doctor = results[0].value as DoctorReport | null;
    }
    if (results[1].status === 'fulfilled') {
      bridges = results[1].value as BridgeInfo[];
    }
    this.warrenIds.forEach((id, i) => {
      const result = results[2 + i];
      if (result.status === 'fulfilled') {
        statuses.set(id, (result.value as { projects: ProjectStatus[] }).projects ?? []);
      } else {
        loadErrors.push(`warren ${id}: ${(result.reason as Error).message}`);
      }
    });

    this.body.innerHTML = '';
    if (loadErrors.length > 0) {
      this.reportError(loadErrors.join('\n'));
    }

    for (const warrenId of this.warrenIds) {
      this.body.appendChild(this.renderWarrenBlock(warrenId, statuses.get(warrenId) ?? []));
    }
    this.body.appendChild(this.renderDoctorSection(doctor));
    const crossVault = bridges.filter((b) => b.is_cross_vault);
    if (crossVault.length > 0) {
      this.body.appendChild(this.renderBridgesSection(crossVault));
    }
  }

  private renderWarrenBlock(warrenId: string, projects: ProjectStatus[]): HTMLElement {
    const block = document.createElement('div');
    block.className = 'warren-block';
    block.dataset.warren = warrenId;

    const title = document.createElement('div');
    title.className = 'warren-block-title';
    const name = document.createElement('span');
    name.className = 'warren-name';
    name.textContent = warrenId;
    title.appendChild(name);

    const refreshBtn = document.createElement('button');
    refreshBtn.className = 'warren-refresh';
    refreshBtn.title = 'Reload warren state from disk';
    refreshBtn.textContent = '↻';
    refreshBtn.addEventListener('click', () => {
      void this.run(refreshBtn, () => refreshWarrenState(warrenId));
    });
    title.appendChild(refreshBtn);
    block.appendChild(title);

    const table = document.createElement('table');
    table.className = 'warren-projects';
    const thead = document.createElement('thead');
    const headRow = document.createElement('tr');
    for (const col of ['Project', 'State', 'Editable', 'Available', '']) {
      const th = document.createElement('th');
      th.textContent = col;
      headRow.appendChild(th);
    }
    thead.appendChild(headRow);
    table.appendChild(thead);

    const tbody = document.createElement('tbody');
    const reasons = this.skippedReasons.get(warrenId) ?? {};
    for (const project of projects) {
      tbody.appendChild(this.renderProjectRow(warrenId, project, reasons[project.project_id]));
    }
    if (projects.length === 0) {
      const tr = document.createElement('tr');
      const td = document.createElement('td');
      td.colSpan = 5;
      td.className = 'warren-empty';
      td.textContent = 'No projects in this warren.';
      tr.appendChild(td);
      tbody.appendChild(tr);
    }
    table.appendChild(tbody);
    block.appendChild(table);
    return block;
  }

  private renderProjectRow(
    warrenId: string,
    project: ProjectStatus,
    skipReason: string | undefined,
  ): HTMLElement {
    const tr = document.createElement('tr');
    tr.dataset.project = project.project_id;
    if (skipReason) {
      tr.title = `Skipped from graph: ${skipReason}`;
      tr.classList.add('warren-row-skipped');
    }

    const nameTd = document.createElement('td');
    nameTd.className = 'warren-project-id';
    nameTd.textContent = project.project_id;
    tr.appendChild(nameTd);

    const stateTd = document.createElement('td');
    const state = document.createElement('span');
    if (project.self_alias) {
      // Identity: served from the live workspace vault; mounting is a no-op
      // (identity is derived from vault_id) so no buttons are offered.
      state.className = 'warren-state warren-state-identity';
      state.textContent = 'identity';
      state.title = 'This project IS this workspace (vault_id match) — served from the live vault';
    } else if (project.active) {
      state.className = 'warren-state warren-state-mounted';
      state.textContent = project.materialized ? 'mounted (burrow)' : 'mounted';
    } else {
      state.className = 'warren-state warren-state-dormant';
      state.textContent = 'dormant';
    }
    stateTd.appendChild(state);
    tr.appendChild(stateTd);

    const editableTd = document.createElement('td');
    editableTd.textContent = project.editable ? 'yes' : 'no';
    tr.appendChild(editableTd);

    const availableTd = document.createElement('td');
    availableTd.textContent = project.available ? 'yes' : 'no';
    if (!project.available) availableTd.className = 'warren-unavailable';
    tr.appendChild(availableTd);

    const actionTd = document.createElement('td');
    actionTd.className = 'warren-actions';
    if (!project.self_alias) {
      const btn = document.createElement('button');
      btn.className = 'warren-action-btn';
      if (project.active) {
        btn.textContent = 'Unmount';
        btn.addEventListener('click', () => {
          void this.run(btn, () => unmountWarrenProjects(warrenId, [project.project_id]));
        });
      } else {
        btn.textContent = 'Mount';
        btn.addEventListener('click', () => {
          void this.run(btn, () => mountWarrenProjects(warrenId, [project.project_id]));
        });
      }
      actionTd.appendChild(btn);
    }
    tr.appendChild(actionTd);
    return tr;
  }

  private renderDoctorSection(report: DoctorReport | null): HTMLElement {
    const section = document.createElement('div');
    section.className = 'warren-doctor';
    const h4 = document.createElement('h4');
    h4.textContent = 'Workspace doctor';
    section.appendChild(h4);

    const issues = report?.issues ?? [];
    if (report === null) {
      const p = document.createElement('p');
      p.className = 'warren-empty';
      p.textContent = 'Doctor report unavailable.';
      section.appendChild(p);
    } else if (issues.length === 0) {
      const p = document.createElement('p');
      p.className = 'warren-doctor-ok';
      p.textContent = 'No findings.';
      section.appendChild(p);
    } else {
      const list = document.createElement('ul');
      list.className = 'warren-doctor-list';
      for (const issue of issues) {
        const li = document.createElement('li');
        li.className = `warren-doctor-issue warren-doctor-${issue.severity}`;
        const sev = document.createElement('span');
        sev.className = 'warren-doctor-sev';
        sev.textContent = issue.severity;
        li.appendChild(sev);
        const code = document.createElement('span');
        code.className = 'warren-doctor-code';
        code.textContent = issue.code;
        li.appendChild(code);
        const msg = document.createElement('span');
        msg.textContent = issue.message;
        li.appendChild(msg);
        list.appendChild(li);
      }
      section.appendChild(list);
    }
    return section;
  }

  private renderBridgesSection(bridges: BridgeInfo[]): HTMLElement {
    const section = document.createElement('div');
    section.className = 'warren-bridges';
    const h4 = document.createElement('h4');
    h4.textContent = 'Cross-vault bridges';
    section.appendChild(h4);
    const list = document.createElement('ul');
    list.className = 'warren-bridge-list';
    for (const bridge of bridges) {
      const li = document.createElement('li');
      li.textContent = `${bridge.source} ↔ ${bridge.target} (${(bridge.allowed_relations ?? []).join(', ') || 'any'})`;
      list.appendChild(li);
    }
    section.appendChild(list);
    return section;
  }

  /**
   * Runs a warren mutation: disables the button, calls the API, surfaces
   * failures in the panel, then re-renders the panel and reloads the graph.
   */
  private async run(btn: HTMLButtonElement, op: () => Promise<unknown>): Promise<void> {
    this.clearError();
    btn.disabled = true;
    try {
      await op();
    } catch (err) {
      this.reportError(err instanceof Error ? err.message : String(err));
      btn.disabled = false;
      return;
    }
    await this.render();
    await this.onStateChange();
  }
}
