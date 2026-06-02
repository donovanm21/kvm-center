import { useEffect, useMemo, useRef, useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';

import { apiGet } from '@/api/client';
import { qk } from '@/queryKeys';

// In-app documentation (v5.14.0). Two-pane layout: sidebar nav on
// the left, rendered markdown content on the right. Pages are
// authored under server/docs/*.md and served via /api/docs/*.

interface TreeEntry {
  slug: string;
  title: string;
  section: string;
  order: number;
}
interface PageData {
  slug: string;
  title: string;
  section: string;
  html: string;
  toc?: { id: string; text: string; level: number }[];
}

export function Docs() {
  const { slug } = useParams<{ slug?: string }>();
  const navigate = useNavigate();

  const tree = useQuery({
    queryKey: qk.docsTree(),
    queryFn: () => apiGet<TreeEntry[]>('/api/docs/tree'),
    staleTime: 5 * 60_000,
  });

  const currentSlug = slug || tree.data?.[0]?.slug || 'overview';
  const page = useQuery({
    queryKey: qk.docsPage(currentSlug),
    queryFn: () => apiGet<PageData>('/api/docs/page/' + encodeURIComponent(currentSlug)),
    enabled: !!currentSlug,
  });

  const grouped = useMemo(() => {
    if (!tree.data) return [];
    const bySection = new Map<string, TreeEntry[]>();
    for (const t of tree.data) {
      if (!bySection.has(t.section)) bySection.set(t.section, []);
      bySection.get(t.section)!.push(t);
    }
    return Array.from(bySection.entries());
  }, [tree.data]);

  // Intercept internal /docs/<slug> links so they navigate via React
  // Router instead of doing a full page reload that drops session state.
  const contentRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const el = contentRef.current;
    if (!el) return;
    const onClick = (e: MouseEvent) => {
      const a = (e.target as HTMLElement).closest('a');
      if (!a) return;
      const href = a.getAttribute('href') || '';
      if (href.startsWith('/docs/')) {
        e.preventDefault();
        navigate(href);
      }
    };
    el.addEventListener('click', onClick);
    return () => el.removeEventListener('click', onClick);
  }, [navigate, page.data]);

  // Restart animated WebP clips from frame 0 each time they scroll
  // into view. The browser caches the decoded animation and keeps
  // playing it forever on a loop, so a user who scrolls to a clip
  // halfway through its cycle misses the intro. Re-setting the src
  // (with a short query-string churn to dodge the disk cache when
  // needed) forces a fresh decode context starting at frame 0.
  useEffect(() => {
    const el = contentRef.current;
    if (!el || !page.data) return;
    const imgs = Array.from(el.querySelectorAll<HTMLImageElement>('img[src*="/api/docs/media/"]'));
    if (imgs.length === 0) return;

    // Stash the original src and swap it for a blank pixel until the
    // image enters the viewport. Subsequent entries toggle the src
    // off/on to restart the animation.
    const originals = new Map<HTMLImageElement, string>();
    for (const img of imgs) {
      originals.set(img, img.src);
      img.src = 'data:image/gif;base64,R0lGODlhAQABAAAAACw=';
    }

    const obs = new IntersectionObserver((entries) => {
      for (const entry of entries) {
        const img = entry.target as HTMLImageElement;
        const src = originals.get(img);
        if (!src) continue;
        if (entry.isIntersecting) {
          img.src = src;
        } else {
          img.src = 'data:image/gif;base64,R0lGODlhAQABAAAAACw=';
        }
      }
    }, { threshold: 0.25 });

    imgs.forEach((img) => obs.observe(img));
    return () => obs.disconnect();
  }, [page.data]);

  // Back-to-top button. Show once the reader has scrolled past the
  // point where the sidebar is still useful — roughly the sidebar's
  // own max height. Reset on slug change so navigating to a new
  // doc starts hidden.
  const [showTop, setShowTop] = useState(false);
  useEffect(() => {
    setShowTop(false);
    const onScroll = () => setShowTop(window.scrollY > 480);
    window.addEventListener('scroll', onScroll, { passive: true });
    onScroll();
    return () => window.removeEventListener('scroll', onScroll);
  }, [currentSlug]);

  return (
    <div className="mobile-stack" style={{ display: 'grid', gridTemplateColumns: '260px 1fr', gap: 24, alignItems: 'flex-start', minHeight: '100%' }}>
      <aside style={sidebarStyle}>
        <div style={{ fontSize: 11, color: 'var(--text-faded)', textTransform: 'uppercase', letterSpacing: '0.08em', marginBottom: 12 }}>
          Documentation
        </div>
        {tree.isLoading && <div style={{ color: 'var(--text-muted)', fontSize: 12 }}>Loading…</div>}
        {grouped.map(([section, entries]) => (
          <div key={section} style={{ marginBottom: 18 }}>
            <div style={sectionHeaderStyle}>{section}</div>
            {entries.map((e) => (
              <Link
                key={e.slug}
                to={`/docs/${e.slug}`}
                style={{
                  display: 'block',
                  padding: '6px 10px',
                  marginLeft: -10,
                  fontSize: 13,
                  color: e.slug === currentSlug ? 'var(--text-strong)' : 'var(--text-body)',
                  background: e.slug === currentSlug ? 'var(--accent-subtle)' : 'transparent',
                  borderRadius: 4,
                  textDecoration: 'none',
                  borderLeft: `2px solid ${e.slug === currentSlug ? 'var(--accent)' : 'transparent'}`,
                }}
              >
                {e.title}
              </Link>
            ))}
          </div>
        ))}
      </aside>

      <main ref={contentRef} style={contentStyle}>
        {page.isLoading && <div style={{ color: 'var(--text-muted)' }}>Loading…</div>}
        {page.error && <div className="tone-pill danger">Failed: {(page.error as Error).message}</div>}
        {page.data && (
          <article className="docs-prose" dangerouslySetInnerHTML={{ __html: page.data.html }} />
        )}
      </main>

      {showTop && (
        <button
          type="button"
          onClick={() => window.scrollTo({ top: 0, behavior: 'smooth' })}
          aria-label="Back to top"
          title="Back to top"
          style={backToTopStyle}
        >
          <span style={{ fontSize: 16, lineHeight: 1 }}>↑</span>
          <span style={{ fontSize: 11, fontWeight: 600, letterSpacing: '0.04em' }}>Top</span>
        </button>
      )}
    </div>
  );
}

const backToTopStyle: React.CSSProperties = {
  position: 'fixed',
  right: 24,
  bottom: 24,
  display: 'flex',
  flexDirection: 'column',
  alignItems: 'center',
  gap: 2,
  padding: '10px 12px',
  minWidth: 44,
  border: '1px solid var(--border)',
  borderRadius: 999,
  background: 'var(--surface)',
  color: 'var(--text-strong)',
  cursor: 'pointer',
  boxShadow: '0 4px 14px rgba(0,0,0,0.35)',
  zIndex: 50,
};

const sidebarStyle: React.CSSProperties = {
  position: 'sticky',
  // v5.45.1: clear the 64 px global topbar + a small breathing
  // gap. Pre-fix the docs sidebar tucked under the topbar after
  // the global layout switched to window-level scrolling.
  top: 84,
  padding: '16px 12px',
  borderRight: '1px solid var(--border)',
  maxHeight: 'calc(100vh - 100px)',
  overflowY: 'auto',
};

const sectionHeaderStyle: React.CSSProperties = {
  fontSize: 10,
  fontWeight: 700,
  textTransform: 'uppercase',
  letterSpacing: '0.12em',
  color: 'var(--text-faded)',
  marginBottom: 6,
};

const contentStyle: React.CSSProperties = {
  padding: '0 8px',
  maxWidth: 840,
  lineHeight: 1.6,
  color: 'var(--text-body)',
  fontSize: 14,
};
