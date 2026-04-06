export interface KeyboardCallbacks {
  onSearch: () => void;
  onEscape: () => void;
  onRefresh: () => void;
  onFitView: () => void;
}

/**
 * Global keyboard shortcut handler.
 * Skips interception when the user is focused on an input/textarea/select,
 * except for Escape which always fires.
 */
export function initKeyboard(callbacks: KeyboardCallbacks): void {
  document.addEventListener('keydown', (e: KeyboardEvent) => {
    const tag = (e.target as HTMLElement).tagName;

    // When inside a form element, only Escape is handled
    if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') {
      if (e.key === 'Escape') {
        (e.target as HTMLElement).blur();
        callbacks.onEscape();
      }
      return;
    }

    switch (e.key) {
      case '/':
        e.preventDefault();
        callbacks.onSearch();
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
