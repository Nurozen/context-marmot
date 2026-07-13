import { test, expect, Page } from '@playwright/test';

// E2E coverage for the ui_validation2 chat findings:
//   Issue 1 — /verify integrity issues must be viewable in the Issues tab
//             (they were counted in chat but rendered nowhere).
//   Issue 2 — the slash-command autocomplete menu omitted /verify even
//             though /help listed it (the menu capped at 8 of 9 commands).
//
// These tests are deliberately non-mutating: the fixture vault is shared
// with the other vault-project specs running in parallel workers.

function trackErrors(page: Page): string[] {
  const errors: string[] = [];
  page.on('pageerror', (err) => errors.push(`pageerror: ${err.message}`));
  page.on('console', (msg) => {
    if (msg.type() === 'error') errors.push(`console: ${msg.text()}`);
  });
  return errors;
}

async function waitForGraph(page: Page): Promise<void> {
  await page.goto('/');
  await expect(page.locator('#graph-svg g.node').first()).toBeVisible({
    timeout: 15_000,
  });
}

/* ------------------------------------------------------------------ */
/*  Issue 2: autocomplete registry must include /verify                */
/* ------------------------------------------------------------------ */

test('slash autocomplete lists every command including /verify', async ({ page }) => {
  const errors = trackErrors(page);
  await waitForGraph(page);

  const input = page.locator('#chat-input');
  await input.click();
  await input.fill('/');

  const menu = page.locator('.curator-autocomplete');
  await expect(menu).toBeVisible();

  // All 9 registered commands must be offered on a bare "/" — the old cap
  // of 8 silently dropped /verify, the last registry entry.
  const labels = await menu.locator('.ac-cmd').allTextContents();
  for (const cmd of [
    '/help',
    '/tag',
    '/untag',
    '/type',
    '/merge',
    '/delete',
    '/link',
    '/unlink',
    '/verify',
  ]) {
    expect(labels, `autocomplete should offer ${cmd}`).toContain(cmd);
  }

  // Narrowing by prefix still works.
  await input.fill('/ve');
  await expect(menu.locator('.ac-cmd')).toHaveText(['/verify']);

  await input.press('Escape');
  expect(errors).toEqual([]);
});

/* ------------------------------------------------------------------ */
/*  Issue 1: integrity issues flow into the Issues tab                 */
/* ------------------------------------------------------------------ */

test('suggestions API exposes the integrity issues /verify counts', async ({ page }) => {
  const res = await page.request.get('/api/curator/suggestions?limit=50');
  expect(res.status()).toBe(200);
  const body = await res.json();

  // The field is always an array (never null/absent) with the verify scope.
  expect(Array.isArray(body.integrity_issues)).toBe(true);
  expect(typeof body.integrity_node_count).toBe('number');
  expect(body.integrity_node_count).toBeGreaterThan(0);

  // Each issue carries the shape the Issues panel renders.
  for (const issue of body.integrity_issues) {
    expect(typeof issue.node_id).toBe('string');
    expect(typeof issue.message).toBe('string');
    expect(['error', 'warning', 'info']).toContain(issue.severity);
    expect(typeof issue.type).toBe('string');
  }
});

test('/verify chat count matches the Issues tab integrity section and badge', async ({
  page,
}) => {
  const errors = trackErrors(page);
  await waitForGraph(page);

  // Ground truth from the API the panel loads.
  const apiRes = await page.request.get('/api/curator/suggestions?limit=50');
  const apiBody = await apiRes.json();
  const integrityCount: number = apiBody.integrity_issues.length;
  const suggestionCount: number = (apiBody.suggestions ?? []).length;

  // Run /verify from the chat; it activates and refreshes the Issues tab.
  const input = page.locator('#chat-input');
  await input.fill('/verify');
  await input.press('Enter');

  const result = page.locator('.chat-message-command').last();
  await expect(result).toContainText('Health check complete');
  await expect(result).toContainText('See Issues tab');
  const chatText = (await result.textContent()) ?? '';
  if (integrityCount === 0) {
    expect(chatText).toContain('no issues');
  } else {
    expect(chatText).toContain(`found ${integrityCount} issue(s)`);
  }

  await expect(page.locator('#curator-issues')).toHaveClass(/active/);

  // The integrity section renders the same count the chat reported —
  // including the explicit clean state, so "8 issues in chat, 2 in the
  // tab" can never silently recur.
  const integrityHeader = page.locator('.issues-section-integrity');
  await expect(integrityHeader).toBeVisible();
  await expect(integrityHeader).toContainText(`Integrity issues (${integrityCount})`);

  const integrityCards = page.locator('#issues-list .integrity-card');
  await expect(integrityCards).toHaveCount(integrityCount);
  if (integrityCount === 0) {
    await expect(page.locator('.issues-section-empty').last()).toContainText(
      'No integrity issues',
    );
  }

  // Curator suggestions render as their own section alongside.
  const suggestionsHeader = page.locator('.issues-section-suggestions');
  await expect(suggestionsHeader).toBeVisible();
  await expect(suggestionsHeader).toContainText(
    `Curator suggestions (${suggestionCount})`,
  );

  // The tab badge sums both sets, with the breakdown on hover.
  const badge = page.locator('#issues-badge');
  const total = suggestionCount + integrityCount;
  if (total === 0) {
    await expect(badge).toBeHidden();
  } else {
    await expect(badge).toHaveText(String(total));
  }
  await expect(badge).toHaveAttribute(
    'title',
    new RegExp(`${suggestionCount} curator suggestion.*\\+ ${integrityCount} integrity issue`),
  );

  expect(errors).toEqual([]);
});
