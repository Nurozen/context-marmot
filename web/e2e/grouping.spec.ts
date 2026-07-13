import { test, expect, Page } from '@playwright/test';

// Grouping + aggregate-view specs, driven against the serve.sh warren
// fixture: workspace vault "e2e-web-vault" (4 nodes: auth, auth/login,
// auth/validate, db/users), warren "wui" (identity project "self" +
// mounted foreign project "other" with vault_id other-vault), and the
// unreachable warren "wgone" (checkout moved after mounting "ghost").

function trackErrors(page: Page): string[] {
  const errors: string[] = [];
  page.on('pageerror', (err) => errors.push(`pageerror: ${err.message}`));
  page.on('console', (msg) => {
    if (msg.type() === 'error') errors.push(`console: ${msg.text()}`);
  });
  return errors;
}

/** Waits for the initial local graph (4 fixture nodes). */
async function waitForLocalGraph(page: Page): Promise<void> {
  await expect(page.locator('#graph-svg g.node')).toHaveCount(4, { timeout: 15_000 });
}

/** Returns the current hull group labels (folder or project grouping). */
async function hullLabels(page: Page): Promise<string[]> {
  return page.locator('#graph-svg .folder-label').allTextContents();
}

test('group-by selector offers Group by project and clusters a warren view by vault', async ({
  page,
}) => {
  const errors = trackErrors(page);

  await page.goto('/');
  await waitForLocalGraph(page);

  // The option sits in the group-by dropdown alongside the existing modes.
  const option = page.locator('#groupby-select option[value="project"]');
  await expect(option).toHaveCount(1);
  await expect(option).toHaveText('Group by project');

  // Warren view: 4 identity nodes (@e2e-web-vault/…) + 1 foreign
  // (@other-vault/svc/beacon).
  await page.locator('#namespace-select').selectOption('_warren/wui');
  await expect(page.locator('#graph-svg g.node')).toHaveCount(5, { timeout: 15_000 });

  await page.locator('#groupby-select').selectOption('project');

  // Two vault islands: the workspace (identity nodes fold into the local
  // group, labeled with the vault_id) and the foreign other-vault.
  await expect(page.locator('#graph-svg .folder-hull')).toHaveCount(2, { timeout: 10_000 });
  const labels = await hullLabels(page);
  expect(labels.sort()).toEqual(['local (e2e-web-vault)', 'other-vault']);

  // The selection sticks like the other modes (no reload resets it here).
  await expect(page.locator('#groupby-select')).toHaveValue('project');

  expect(errors).toEqual([]);
});

test('folder grouping strips vault prefixes and qualifies cross-vault folders', async ({
  page,
}) => {
  await page.goto('/');
  await waitForLocalGraph(page);

  // Local view first: genuine directories, no vault qualification.
  await page.locator('#groupby-select').selectOption('folder');
  await expect(page.locator('#graph-svg .folder-hull')).toHaveCount(3, { timeout: 10_000 });
  expect((await hullLabels(page)).sort()).toEqual(['_root', 'auth', 'db']);

  // Warren view: '@e2e-web-vault' / '@other-vault' must NOT appear as
  // folders; instead each vault's real directories render, qualified as
  // '<vault> › <folder>' so same-named folders never collide across vaults.
  await page.locator('#namespace-select').selectOption('_warren/wui');
  await expect(page.locator('#graph-svg g.node')).toHaveCount(5, { timeout: 15_000 });
  await page.locator('#groupby-select').selectOption('folder');

  await expect(page.locator('#graph-svg .folder-hull')).toHaveCount(4, { timeout: 10_000 });
  const labels = (await hullLabels(page)).sort();
  expect(labels).toEqual([
    'e2e-web-vault › _root',
    'e2e-web-vault › auth',
    'e2e-web-vault › db',
    'other-vault › svc',
  ]);
  for (const label of labels) {
    expect(label.startsWith('@')).toBe(false);
  }
});

test('All warrens option aggregates local + warren graphs and degrades on unreachable warrens', async ({
  page,
}) => {
  await page.goto('/');
  await waitForLocalGraph(page);

  const select = page.locator('#namespace-select');

  // The aggregate option follows the per-warren entries (selector
  // convention: default, Warrens group, then the aggregate).
  const values = await select
    .locator('option')
    .evaluateAll((opts) => opts.map((o) => (o as HTMLOptionElement).value));
  const allIdx = values.indexOf('_warrens');
  expect(allIdx).toBeGreaterThan(values.indexOf('_warren/wui'));
  expect(allIdx).toBeGreaterThan(values.indexOf('_warren/wgone'));
  await expect(select.locator('option[value="_warrens"]')).toHaveText(/All warrens \(\d+ active\)/);

  await select.selectOption('_warrens');

  // The unreachable wgone warren degrades to the existing skip toast —
  // it must not block the aggregate view.
  const toast = page.locator('.toast-error').last();
  await expect(toast).toBeVisible({ timeout: 10_000 });
  await expect(toast).toContainText('wgone');
  await expect(toast).toContainText('ghost');
  await expect(toast).toContainText('unreachable');

  // Aggregate = 4 local nodes + 1 foreign @other-vault node; the wui
  // identity project (@e2e-web-vault/…) folds onto the local nodes
  // instead of duplicating them.
  await expect(page.locator('#graph-svg g.node')).toHaveCount(5, { timeout: 15_000 });

  // The aggregate defaults to project grouping: one local island plus
  // one island per foreign vault.
  await expect(page.locator('#groupby-select')).toHaveValue('project');
  await expect(page.locator('#graph-svg .folder-hull')).toHaveCount(2, { timeout: 10_000 });
  expect((await hullLabels(page)).sort()).toEqual(['local (e2e-web-vault)', 'other-vault']);

  // Selecting a plain namespace recovers cleanly.
  await select.selectOption('default');
  await waitForLocalGraph(page);
});
