import { useEffect, useMemo, useRef } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';

import { apiGet } from '@/api/client';

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
    queryKey: ['docs-tree'],
    queryFn: () => apiGet<TreeEntry[]>('/api/docs/tree'),
    staleTime: 5 * 60_000,
  });

  const currentSlug = slug || tree.data?.[0]?.slug || 'overview';
  const page = useQuery({
    queryKey: ['docs-page', currentSlug],
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

  return (
    <div style={{ display: 'grid', gridTemplateColumns: '260px 1fr', gap: 24, alignItems: 'flex-start', minHeight: '100%' }}>
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
    </div>
  );
}

const sidebarStyle: React.CSSProperties = {
  position: 'sticky',
  top: 20,
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
