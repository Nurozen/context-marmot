import { test, expect, Page } from '@playwright/test';

// Collect console/page errors so any UI regression fails the test even when
// the DOM assertions still pass.
function trackErrors(page: Page): string[] {
  const errors: string[] = [];
  page.on('pageerror', (err) => errors.push(`pageerror: ${err.message}`));
  page.on('console', (msg) => {
    if (msg.type() === 'error') errors.push(`console: ${msg.text()}`);
  });
  return errors;
}

test('graph explorer renders the fixture graph', async ({ page }) => {
  const errors = trackErrors(page);

  await page.goto('/');
  await expect(page).toHaveTitle(/ContextMarmot/);

  // The force-directed graph renders all 4 fixture nodes.
  await expect(page.locator('#graph-svg g.node')).toHaveCount(4, { timeout: 15_000 });

  // Namespace selector is populated with the default namespace.
  await expect(page.locator('#namespace-select option')).not.toHaveCount(0);
  const values = await page
    .locator('#namespace-select option')
    .evaluateAll((opts) => opts.map((o) => (o as HTMLOptionElement).value));
  expect(values).toContain('default');

  // Filter panel picked up node types from the graph payload.
  await expect(page.locator('#type-filters input')).not.toHaveCount(0);

  expect(errors).toEqual([]);
});

test('clicking a node opens the detail panel', async ({ page }) => {
  const errors = trackErrors(page);

  await page.goto('/');
  const nodes = page.locator('#graph-svg g.node');
  await expect(nodes).toHaveCount(4, { timeout: 15_000 });

  // Let the force simulation settle briefly, then click a node.
  await page.waitForTimeout(1_000);
  await nodes.first().click({ force: true });

  const panel = page.locator('#detail-panel');
  await expect(panel).not.toHaveClass(/hidden/);
  await expect(page.locator('#detail-content')).not.toBeEmpty();

  // Close it again.
  await page.locator('#detail-close').click();
  await expect(panel).toHaveClass(/hidden/);

  expect(errors).toEqual([]);
});

test('search input filters without errors', async ({ page }) => {
  const errors = trackErrors(page);

  await page.goto('/');
  await expect(page.locator('#graph-svg g.node')).toHaveCount(4, { timeout: 15_000 });

  const search = page.locator('#search-input');
  await search.fill('login');
  await search.press('Enter');

  // The graph must still be alive after searching (no crash, nodes intact).
  await expect(page.locator('#graph-svg g.node')).not.toHaveCount(0);
  expect(errors).toEqual([]);
});
