import { test, expect, Page } from '@playwright/test';

// Regression tests for the manual-test findings (agent-2 issues 1-3):
//   1. mobile/narrow responsive layout was broken at 390x844
//   2. /help was not a recognized curator slash command
//   3. hover card vs detail panel edge-count mismatch

function trackErrors(page: Page): string[] {
  const errors: string[] = [];
  page.on('pageerror', (err) => errors.push(`pageerror: ${err.message}`));
  page.on('console', (msg) => {
    if (msg.type() === 'error') errors.push(`console: ${msg.text()}`);
  });
  return errors;
}

/* ------------------------------------------------------------------ */
/*  Issue 1: responsive layout at phone size (390x844)                 */
/* ------------------------------------------------------------------ */

test.describe('mobile viewport 390x844', () => {
  test.use({ viewport: { width: 390, height: 844 } });

  test('layout stays usable: search, toggles, sidebar drawer, detail close', async ({
    page,
  }) => {
    const errors = trackErrors(page);

    await page.goto('/');
    await expect(page.locator('#graph-svg g.node')).toHaveCount(4, { timeout: 15_000 });

    // Search box gets a usable full-width row instead of collapsing to ~48px.
    const searchBox = await page.locator('#search-input').boundingBox();
    expect(searchBox).not.toBeNull();
    expect(searchBox!.width).toBeGreaterThan(200);
    expect(searchBox!.x + searchBox!.width).toBeLessThanOrEqual(390);

    // Heat / Superseded toggles remain on-screen (they used to overflow right).
    for (const id of ['#heat-toggle', '#superseded-toggle']) {
      const box = await page.locator(id).boundingBox();
      expect(box, `${id} should be visible`).not.toBeNull();
      expect(box!.x).toBeGreaterThanOrEqual(0);
      expect(box!.x + box!.width).toBeLessThanOrEqual(390);
    }

    // Filter sidebar defaults to a collapsed overlay drawer (it used to
    // permanently overlap the canvas).
    const filterPanel = page.locator('#filter-panel');
    await expect(filterPanel).toHaveClass(/collapsed/);

    // The toggle button opens and closes the drawer.
    const filterToggle = page.locator('#filter-toggle');
    await expect(filterToggle).toBeVisible();
    await filterToggle.click();
    await expect(filterPanel).not.toHaveClass(/collapsed/);
    await expect(filterPanel).toBeInViewport(); // retries through the slide-in transition
    const panelBox = await filterPanel.boundingBox();
    expect(panelBox).not.toBeNull();
    expect(panelBox!.width).toBeLessThan(390); // drawer, not full-screen
    await filterToggle.click();
    await expect(filterPanel).toHaveClass(/collapsed/);

    // Node detail panel opens with its close button reachable on-screen.
    await page.waitForTimeout(1_000);
    await page.locator('#graph-svg g.node').first().click({ force: true });
    const detailPanel = page.locator('#detail-panel');
    await expect(detailPanel).not.toHaveClass(/hidden/);
    // Retries through the slide-in transition; ratio 1 = fully on-screen.
    await expect(page.locator('#detail-close')).toBeInViewport({ ratio: 1 });
    const closeBox = await page.locator('#detail-close').boundingBox();
    expect(closeBox).not.toBeNull();
    expect(closeBox!.x).toBeGreaterThanOrEqual(0);
    expect(closeBox!.x + closeBox!.width).toBeLessThanOrEqual(390);
    expect(closeBox!.y).toBeGreaterThanOrEqual(0);
    await page.locator('#detail-close').click();
    await expect(detailPanel).toHaveClass(/hidden/);

    expect(errors).toEqual([]);
  });
});

/* ------------------------------------------------------------------ */
/*  Issue 2: /help slash command                                       */
/* ------------------------------------------------------------------ */

test('/help lists the available slash commands', async ({ page }) => {
  const errors = trackErrors(page);

  await page.goto('/');
  await expect(page.locator('#graph-svg g.node')).toHaveCount(4, { timeout: 15_000 });

  const input = page.locator('#chat-input');
  await input.fill('/help');
  await input.press('Enter');

  const result = page.locator('.chat-message-command').last();
  await expect(result).toHaveClass(/success/);
  await expect(result).toContainText('Available commands');
  for (const cmd of ['/help', '/tag', '/verify', '/merge']) {
    await expect(result).toContainText(cmd);
  }

  // Unknown commands now point at /help.
  await input.fill('/bogus');
  await input.press('Enter');
  const unknown = page.locator('.chat-message-command').last();
  await expect(unknown).toContainText('Unknown command: /bogus');
  await expect(unknown).toContainText('/help');

  expect(errors).toEqual([]);
});

/* ------------------------------------------------------------------ */
/*  Issue 3: hover card vs detail panel edge counts                    */
/* ------------------------------------------------------------------ */

test('hover card and detail panel agree on edge counts', async ({ page }) => {
  const errors = trackErrors(page);

  await page.goto('/');
  const nodes = page.locator('#graph-svg g.node');
  await expect(nodes).toHaveCount(4, { timeout: 15_000 });
  await page.waitForTimeout(1_500); // let the simulation settle

  // auth/login has 1 outgoing edge (reads db/users) and 2 incoming
  // (auth contains it, auth/validate references it) in the fixture vault.
  const login = nodes.filter({ hasText: 'login' }).first();
  await login.hover({ force: true });

  const tooltipType = page.locator('.graph-tooltip .tt-type');
  await expect(tooltipType).toBeVisible();
  const ttText = (await tooltipType.textContent()) ?? '';
  const ttMatch = ttText.match(/(\d+) out \/ (\d+) in/);
  expect(ttMatch, `tooltip should show "N out / M in", got "${ttText}"`).not.toBeNull();

  await login.click({ force: true });
  await expect(page.locator('#detail-panel')).not.toHaveClass(/hidden/);
  const edgesHeader = page
    .locator('#detail-content .detail-section h4')
    .filter({ hasText: 'Edges' });
  await expect(edgesHeader).toHaveCount(1);
  const headerText = (await edgesHeader.textContent()) ?? '';
  const panelMatch = headerText.match(/Edges \((\d+) out \/ (\d+) in\)/);
  expect(panelMatch, `panel should show "Edges (N out / M in)", got "${headerText}"`).not.toBeNull();

  // The two surfaces must report identical counts.
  expect(panelMatch![1]).toBe(ttMatch![1]);
  expect(panelMatch![2]).toBe(ttMatch![2]);

  // And they should match the fixture's known degree (1 out, 2 in).
  expect(ttMatch![1]).toBe('1');
  expect(ttMatch![2]).toBe('2');

  expect(errors).toEqual([]);
});

/* ------------------------------------------------------------------ */
/*  /verify must not hit dead endpoints or log console errors          */
/* ------------------------------------------------------------------ */

test('/verify refreshes the Issues tab without dead API calls', async ({ page }) => {
  const errors = trackErrors(page);
  const deadRequests: string[] = [];
  page.on('request', (req) => {
    if (new URL(req.url()).pathname.startsWith('/api/verify/')) {
      deadRequests.push(req.url());
    }
  });

  await page.goto('/');
  await expect(page.locator('#graph-svg g.node')).toHaveCount(4, { timeout: 15_000 });

  const input = page.locator('#chat-input');
  await input.fill('/verify');
  await input.press('Enter');

  const result = page.locator('.chat-message-command').last();
  await expect(result).toContainText('Health check complete');

  // The Issues tab is activated and rendered by the Issues panel
  // (via /api/curator/suggestions).
  await expect(page.locator('#curator-issues')).toHaveClass(/active/);

  // Legacy /api/verify/:ns never existed server-side; the curator must not
  // call it (it now 404s as JSON, which showed up as a console error).
  expect(deadRequests).toEqual([]);
  expect(errors).toEqual([]);
});
