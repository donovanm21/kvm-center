import { useCallback, useEffect, useState } from 'react';

type Theme = 'dark' | 'light';
const STORAGE_KEY = 'kvm-center-theme';

function readPersisted(): Theme {
  try {
    const v = localStorage.getItem(STORAGE_KEY);
    if (v === 'light' || v === 'dark') return v;
  } catch {
    /* localStorage may be blocked in private mode */
  }
  return 'dark';
}

/**
 * Syncs a `light-mode` class on document.body so the CSS custom-property
 * overrides kick in. Persists the choice in localStorage; no server
 * round-trip needed — v3 behaved the same way.
 */
export function useTheme(): [Theme, () => void] {
  const [theme, setTheme] = useState<Theme>(() => readPersisted());

  useEffect(() => {
    document.body.classList.toggle('light-mode', theme === 'light');
    try {
      localStorage.setItem(STORAGE_KEY, theme);
    } catch {
      /* ignore */
    }
  }, [theme]);

  const toggle = useCallback(() => setTheme((t) => (t === 'dark' ? 'light' : 'dark')), []);
  return [theme, toggle];
}
