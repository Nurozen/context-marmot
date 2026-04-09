export interface KeyboardCallbacks {
  onSearch: () => void;
  onEscape: () => void;
  onRefresh: () => void;
  onFitView: () => void;
  onCuratorToggle?: () => void;
  onCuratorSlash?: () => void;
}

/**
 * Global keyboard shortcut handler.
 * Skips interception when the user is focused on an input/textarea/select,
 * except for Escape and Cmd+K which always fire.
 */
export function initKeyboard(callbacks: KeyboardCallbacks): void {
  document.addEventListener('keydown', (e: KeyboardEvent) => {
    const tag = (e.target as HTMLElement).tagName;
    const isFormEl = tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT';

    /* Cmd+K / Ctrl+K: toggle curator drawer (always fires) */
    if (e.key === 'k' && (e.metaKey || e.ctrlKey)) {
      e.preventDefault();
      callbacks.onCuratorToggle?.();
      return;
    }

    /* When inside a form element, only Escape is handled beyond Cmd+K */
    if (isFormEl) {
      if (e.key === 'Escape') {
        (e.target as HTMLElement).blur();
        callbacks.onEscape();
      }
      return;
    }

    switch (e.key) {
      case '/':
        e.preventDefault();
        if (callbacks.onCuratorSlash) {
          callbacks.onCuratorSlash();
        } else {
          callbacks.onSearch();
        }
        break;
      case 'Escape':
        callbacks.onEscape();
        break;
      case 'r':
        callbacks.onRefresh();
        break;
      case 'f':
        callbacks.onFitView();
        break;
    }
  });
}
