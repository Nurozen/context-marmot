/* ──────────────────────────────────────────────────────────────────
   Toast notifications — the single user-visible surfacing mechanism
   for async operation outcomes (graph-load failures, warren state
   errors, refresh confirmations). Fixes the old pattern of
   console-only errors that left the UI silently stale.
   ────────────────────────────────────────────────────────────────── */

export type ToastKind = 'success' | 'error' | 'info';

const DEFAULT_DURATION_MS = 5000;

/**
 * Shows a transient toast in the top-right corner. Errors get a longer
 * default duration and an alert role so screen readers announce them.
 */
export function showToast(
  message: string,
  kind: ToastKind = 'info',
  durationMs?: number,
): HTMLElement {
  let container = document.getElementById('toast-container');
  if (!container) {
    container = document.createElement('div');
    container.id = 'toast-container';
    document.body.appendChild(container);
  }

  const toast = document.createElement('div');
  toast.className = `toast toast-${kind}`;
  toast.setAttribute('role', kind === 'error' ? 'alert' : 'status');
  toast.textContent = message;
  container.appendChild(toast);

  const ttl = durationMs ?? (kind === 'error' ? 8000 : DEFAULT_DURATION_MS);
  const dismiss = (): void => {
    toast.classList.add('toast-hide');
    window.setTimeout(() => toast.remove(), 250);
  };
  const timer = window.setTimeout(dismiss, ttl);
  // Click dismisses immediately.
  toast.addEventListener('click', () => {
    window.clearTimeout(timer);
    dismiss();
  });
  return toast;
}
