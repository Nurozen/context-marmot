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

  // No error surfaced in the panel during the round trip.
  await expect(panel.locator('#warren-panel-error')).toBeHidden();
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
