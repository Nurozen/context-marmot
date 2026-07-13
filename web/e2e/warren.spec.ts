import { test, expect, Page } from '@playwright/test';

// Warren UI specs (plan U5b/U5c + R2.6 display bits), driven against the
// serve.sh warren fixture: warren "wui" holding the workspace itself as an
// identified project ("self", vault_id e2e-web-vault, never mounted) and a
// mounted foreign project ("other", vault_id other-vault) bridged to it.

function trackErrors(page: Page): string[] {
  const errors: string[] = [];
  page.on('pageerror', (err) => errors.push(`pageerror: ${err.message}`));
  page.on('console', (msg) => {
    if (msg.type() === 'error') errors.push(`console: ${msg.text()}`);
  });
  return errors;
}

/** Selects the warren graph view and waits for its nodes (4 self + 1 other). */
async function openWarrenView(page: Page): Promise<void> {
  await page.locator('#namespace-select').selectOption('_warren/wui');
  await expect(page.locator('#graph-svg g.node')).toHaveCount(5, { timeout: 15_000 });
  // Let the force simulation settle before any node clicks.
  await page.waitForTimeout(1_000);
}

/** Clicks the graph node whose short label matches and waits for the panel. */
async function openNodeDetail(page: Page, label: string): Promise<void> {
  await page
    .locator('#graph-svg g.node')
    .filter({ hasText: label })
    .first()
    .click({ force: true });
  await expect(page.locator('#detail-panel')).not.toHaveClass(/hidden/);
}

test('warren appears in the namespace selector with active and identity counts', async ({
  page,
}) => {
  const errors = trackErrors(page);

  await page.goto('/');
  await expect(page.locator('#graph-svg g.node')).toHaveCount(4, { timeout: 15_000 });

  const option = page.locator('#namespace-select option[value="_warren/wui"]');
  await expect(option).toHaveCount(1);
  const label = await option.textContent();
  expect(label).toContain('Warren wui');
  expect(label).toContain('1 active');
  expect(label).toContain('1 identity');

  expect(errors).toEqual([]);
});

test('warren panel lists projects, doctor report, and bridges', async ({ page }) => {
  const errors = trackErrors(page);

  await page.goto('/');
  await expect(page.locator('#graph-svg g.node')).toHaveCount(4, { timeout: 15_000 });

  const toggle = page.locator('#warren-toggle');
  await expect(toggle).toBeVisible();
  await toggle.click();

  const panel = page.locator('#warren-panel');
  await expect(panel).not.toHaveClass(/hidden/);

  // Identity row: badge, read-only, no mount/unmount affordance.
  const selfRow = panel.locator('tr[data-project="self"]');
  await expect(selfRow).toBeVisible();
  await expect(selfRow.locator('.warren-state')).toHaveText('identity');
  await expect(selfRow.locator('button')).toHaveCount(0);

  // Identity EDITABLE column reads "local" (not a confusing "no") with a
  // tooltip explaining that edits happen normally in this workspace and
  // warren edit/propose applies only to foreign mounted projects.
  const selfEditable = selfRow.locator('td').nth(2);
  await expect(selfEditable).toHaveText('local');
  await expect(selfEditable).toHaveClass(/warren-editable-local/);
  const editableTitle = await selfEditable.getAttribute('title');
  expect(editableTitle).toContain('Edits happen normally in this workspace');
  expect(editableTitle).toContain('foreign mounted projects');
  // The styled tooltip must actually become visible on hover (a bare title
  // attr needs a ~1s native hover delay and reads as "tooltip doesn't show").
  const tooltipData = await selfEditable.getAttribute('data-tooltip');
  expect(tooltipData).toContain('Edits happen normally in this workspace');
  await selfEditable.hover();
  const tooltipVisible = await selfEditable.evaluate((el) => {
    const after = getComputedStyle(el, '::after');
    return after.content !== 'none' && after.content !== '' && after.position === 'absolute';
  });
  expect(tooltipVisible).toBe(true);
  // Foreign mounted rows keep the plain yes/no display.
  await expect(panel.locator('tr[data-project="other"] td').nth(2)).toHaveText('no');

  // Mounted foreign row: state + unmount affordance.
  const otherRow = panel.locator('tr[data-project="other"]');
  await expect(otherRow).toBeVisible();
  await expect(otherRow.locator('.warren-state')).toHaveText('mounted');
  await expect(otherRow.getByRole('button', { name: 'Unmount' })).toBeVisible();

  // Doctor section renders the workspace report (identity is an info code).
  const doctor = panel.locator('.warren-doctor');
  await expect(doctor).toContainText('self_identity');

  // Cross-vault bridges section is wired to GET /api/bridges.
  await expect(panel.locator('.warren-bridges')).toContainText('self');
  await expect(panel.locator('.warren-bridges')).toContainText('other');

  expect(errors).toEqual([]);
});

test('unmount and mount round-trip through the panel buttons', async ({ page }) => {
  await page.goto('/');
  await expect(page.locator('#graph-svg g.node')).toHaveCount(4, { timeout: 15_000 });

  await page.locator('#warren-toggle').click();
  const panel = page.locator('#warren-panel');
  const otherRow = panel.locator('tr[data-project="other"]');
  await expect(otherRow.locator('.warren-state')).toHaveText('mounted');

  // Unmount: POST /api/warren/wui/unmount, then the panel re-renders from
  // the status endpoint.
  const unmountResponse = page.waitForResponse(
    (r) => r.url().includes('/api/warren/wui/unmount') && r.request().method() === 'POST',
  );
  await otherRow.getByRole('button', { name: 'Unmount' }).click();
  expect((await unmountResponse).status()).toBe(200);
  await expect(panel.locator('tr[data-project="other"] .warren-state')).toHaveText('dormant', {
    timeout: 10_000,
  });

  // C4: the namespace-selector option label must track the unmount instead
  // of keeping the stale "(1 active, ...)" count.
  const warrenOption = page.locator('#namespace-select option[value="_warren/wui"]');
  await expect(warrenOption).toHaveText(/0 active/, { timeout: 10_000 });

  // Mount it back so later specs (and re-runs) see the fixture state.
  const mountResponse = page.waitForResponse(
    (r) => r.url().includes('/api/warren/wui/mount') && r.request().method() === 'POST',
  );
  await panel
    .locator('tr[data-project="other"]')
    .getByRole('button', { name: 'Mount' })
    .click();
  expect((await mountResponse).status()).toBe(200);
  await expect(panel.locator('tr[data-project="other"] .warren-state')).toHaveText('mounted', {
    timeout: 10_000,
  });

  // C4 again after the re-mount.
  await expect(warrenOption).toHaveText(/1 active/, { timeout: 10_000 });

  // No error toast surfaced during the round trip.
  await expect(page.locator('.toast-error')).toHaveCount(0);
});

test('identity node is served live with local_alias provenance and read-only gating', async ({
  page,
}) => {
  const errors = trackErrors(page);

  await page.goto('/');
  await expect(page.locator('#graph-svg g.node')).toHaveCount(4, { timeout: 15_000 });
  await openWarrenView(page);

  // db/users belongs to the identified project: it must render from the
  // LIVE vault (the post-import marker), never the warren snapshot.
  await openNodeDetail(page, 'users');
  const content = page.locator('#detail-content');
  await expect(content).toContainText('Source local_alias');
  await expect(content).toContainText('Vault e2e-web-vault');
  await expect(content.locator('textarea').nth(1)).toHaveValue(/LIVE-MARKER/);

  // Read-only gating: disabled save + the live-vault edit hint.
  const saveBtn = content.getByRole('button', { name: 'Read-only Warren Node' });
  await expect(saveBtn).toBeVisible();
  await expect(saveBtn).toBeDisabled();
  await expect(content.locator('.readonly-hint')).toHaveText(
    'This is your live vault — edit the unqualified node.',
  );

  // C5: the Summary/Context textareas must refuse typing (readOnly), not
  // silently accept edits that can never be saved.
  await expect(content.locator('textarea').nth(0)).toHaveJSProperty('readOnly', true);
  await expect(content.locator('textarea').nth(1)).toHaveJSProperty('readOnly', true);

  expect(errors).toEqual([]);
});

test('foreign warren node shows warren_mount provenance with the edit-enable hint', async ({
  page,
}) => {
  await page.goto('/');
  await expect(page.locator('#graph-svg g.node')).toHaveCount(4, { timeout: 15_000 });
  await openWarrenView(page);

  await openNodeDetail(page, 'beacon');
  const content = page.locator('#detail-content');
  await expect(content).toContainText('Source warren_mount');
  await expect(content).toContainText('Vault other-vault');
  await expect(content.getByRole('button', { name: 'Read-only Warren Node' })).toBeDisabled();
  await expect(content.locator('.readonly-hint')).toHaveText(
    'Enable writes: marmot warren edit other --warren wui',
  );

  // C5: foreign warren-mount nodes also get readOnly textareas.
  await expect(content.locator('textarea').nth(0)).toHaveJSProperty('readOnly', true);
  await expect(content.locator('textarea').nth(1)).toHaveJSProperty('readOnly', true);
});

test('per-warren refresh in the panel shows visible success feedback', async ({ page }) => {
  await page.goto('/');
  await expect(page.locator('#graph-svg g.node')).toHaveCount(4, { timeout: 15_000 });

  await page.locator('#warren-toggle').click();
  await expect(page.locator('#warren-panel')).not.toHaveClass(/hidden/);

  const refreshResponse = page.waitForResponse(
    (r) => r.url().includes('/api/warren/wui/refresh') && r.request().method() === 'POST',
  );
  await page.locator('.warren-block[data-warren="wui"] .warren-refresh').click();
  expect((await refreshResponse).status()).toBe(200);

  // C3: success is no longer silent — a toast confirms the reload.
  const toast = page.locator('.toast-success').last();
  await expect(toast).toBeVisible();
  await expect(toast).toContainText('reloaded');
});

test('failed warren graph load surfaces a visible error toast, then recovers', async ({
  page,
}) => {
  await page.goto('/');
  await expect(page.locator('#graph-svg g.node')).toHaveCount(4, { timeout: 15_000 });

  // The UI has no URL routing, so a warren vanishing between refreshes is
  // simulated by forcing the selector to a nonexistent warren namespace.
  await page.evaluate(() => {
    const select = document.getElementById('namespace-select') as HTMLSelectElement;
    const opt = document.createElement('option');
    opt.value = '_warren/nope-does-not-exist';
    opt.textContent = 'Warren nope-does-not-exist';
    select.appendChild(opt);
    select.value = opt.value;
    select.dispatchEvent(new Event('change'));
  });

  // C2: the 404 graph load must be user-visible, not console-only.
  const toast = page.locator('.toast-error').last();
  await expect(toast).toBeVisible({ timeout: 10_000 });
  await expect(toast).toContainText('Failed to load graph');
  await expect(toast).toContainText('nope-does-not-exist');

  // Selecting a valid namespace recovers cleanly.
  await page.locator('#namespace-select').selectOption('default');
  await expect(page.locator('#graph-svg g.node')).toHaveCount(4, { timeout: 15_000 });
});

test('unreachable warren graph surfaces skipped projects instead of a silent empty graph', async ({
  page,
}) => {
  await page.goto('/');
  await expect(page.locator('#graph-svg g.node')).toHaveCount(4, { timeout: 15_000 });

  // The wgone fixture warren's checkout was moved away on disk after its
  // "ghost" project was mounted (the real-world C2 case: dir vanished
  // between refreshes — a 200 empty graph, not a 404).
  await page.locator('#namespace-select').selectOption('_warren/wgone');

  // C2: the empty load must be user-visible, not console-only.
  const toast = page.locator('.toast-error').last();
  await expect(toast).toBeVisible({ timeout: 10_000 });
  await expect(toast).toContainText('wgone');
  await expect(toast).toContainText('ghost');
  await expect(toast).toContainText('unreachable');

  // The panel row carries the skip-reason tooltip fed by the graph load.
  await page.locator('#warren-toggle').click();
  const ghostRow = page.locator('#warren-panel tr[data-project="ghost"]');
  await expect(ghostRow).toBeVisible();
  await expect(ghostRow).toHaveClass(/warren-row-skipped/);
  const title = await ghostRow.getAttribute('title');
  expect(title).toContain('unreachable');

  // Selecting a valid namespace recovers cleanly.
  await page.locator('#namespace-select').selectOption('default');
  await expect(page.locator('#graph-svg g.node')).toHaveCount(4, { timeout: 15_000 });
});

test.describe('mobile 390x844', () => {
  test.use({ viewport: { width: 390, height: 844 } });

  test('warren and refresh controls stay reachable and the panel opens', async ({ page }) => {
    const errors = trackErrors(page);

    await page.goto('/');
    await expect(page.locator('#graph-svg g.node')).toHaveCount(4, { timeout: 15_000 });

    // C1: neither control may be clipped by the 390px viewport (body has
    // overflow-x hidden, so anything past x=390 is unreachable).
    const toggle = page.locator('#warren-toggle');
    await expect(toggle).toBeVisible();
    const toggleBox = await toggle.boundingBox();
    expect(toggleBox).not.toBeNull();
    expect(toggleBox!.x).toBeGreaterThanOrEqual(0);
    expect(toggleBox!.x + toggleBox!.width).toBeLessThanOrEqual(390);

    const refreshBox = await page.locator('#refresh-btn').boundingBox();
    expect(refreshBox).not.toBeNull();
    expect(refreshBox!.x).toBeGreaterThanOrEqual(0);
    expect(refreshBox!.x + refreshBox!.width).toBeLessThanOrEqual(390);

    // The panel actually opens from a real tap at phone width.
    await toggle.click();
    const panel = page.locator('#warren-panel');
    await expect(panel).not.toHaveClass(/hidden/);
    await expect(panel.locator('tr[data-project="other"]')).toBeVisible();
    await expect(panel.locator('tr[data-project="self"]')).toBeVisible();

    expect(errors).toEqual([]);
  });
});

test('refresh button POSTs the warren refresh endpoint in a warren view', async ({ page }) => {
  await page.goto('/');
  await expect(page.locator('#graph-svg g.node')).toHaveCount(4, { timeout: 15_000 });
  await openWarrenView(page);

  const refreshResponse = page.waitForResponse(
    (r) => r.url().includes('/api/warren/wui/refresh') && r.request().method() === 'POST',
  );
  await page.locator('#refresh-btn').click();
  expect((await refreshResponse).status()).toBe(200);

  // The graph re-fetch after the refresh keeps the warren view alive.
  await expect(page.locator('#graph-svg g.node')).toHaveCount(5, { timeout: 15_000 });
});
