import { useRef, useState } from 'react';

import { Modal } from './Modal';
import { useAuth } from '@/hooks/useAuth';
import { useFeatures } from '@/hooks/useInventory';
import { useDeleteFile, useStorageBrowse, useUploadFile } from '@/hooks/useStorageBrowse';

import { useConfirm } from '@/hooks/useConfirm';
interface Props {
  hostID: string;
  initialPath: string;
  open: boolean;
  onClose: () => void;
}

/**
 * Remote file browser for a libvirt storage pool path. Walks directories
 * (agent-side), uploads new files, deletes existing ones — each gated by
 * the storage.upload / storage.delete permission and the matching global
 * feature toggle.
 */
export function FileBrowserModal({ hostID, initialPath, open, onClose }: Props) {
  const [path, setPath] = useState(initialPath);
  const { hasPerm } = useAuth();
  const confirmFn = useConfirm();
  const features = useFeatures();
  const browse = useStorageBrowse(hostID, path);
  const upload = useUploadFile(hostID, path);
  const del = useDeleteFile(hostID, path);
  const fileInput = useRef<HTMLInputElement>(null);

  const canUpload = hasPerm('storage.upload') && !features.data?.disable_storage_upload;
  const canDelete = hasPerm('storage.delete') && !features.data?.disable_storage_delete;

  const goUp = () => {
    if (path === '/' || path === initialPath) return;
    const up = path.replace(/\/+$/, '').split('/').slice(0, -1).join('/') || '/';
    setPath(up);
  };

  const crumbs = splitCrumbs(path, initialPath);

  const triggerUpload = () => {
    if (!canUpload) return;
    fileInput.current?.click();
  };

  return (
    <Modal
      open={open}
      onClose={onClose}
      width={720}
      title={`${hostID} — ${initialPath}`}
      footer={
        <>
          <button className="btn-secondary-sm" onClick={goUp} disabled={path === initialPath}>↑ Up</button>
          <button
            className="btn-primary-sm"
            onClick={triggerUpload}
            disabled={!canUpload || upload.isPending}
            title={!canUpload ? 'Storage uploads disabled or missing permission' : ''}
          >
            {upload.isPending ? 'Uploading…' : 'Upload File'}
          </button>
          <button className="btn-secondary-sm" onClick={() => browse.refetch()}>↻ Refresh</button>
          <button className="btn-secondary-sm" onClick={onClose}>Close</button>
        </>
      }
    >
      <input
        ref={fileInput}
        type="file"
        style={{ display: 'none' }}
        onChange={(e) => {
          const f = e.target.files?.[0];
          if (f) upload.mutate(f);
          e.target.value = '';
        }}
      />

      <div style={crumbRowStyle}>
        <span style={{ color: 'var(--text-muted)' }}>Path:</span>
        {crumbs.map((c, i) => (
          <span key={i}>
            <span
              style={{
                color: 'var(--text-body)',
                cursor: c.path !== path ? 'pointer' : 'default',
                textDecoration: c.path !== path ? 'underline dotted' : 'none',
              }}
              onClick={() => c.path !== path && setPath(c.path)}
            >
              {c.label}
            </span>
            {i < crumbs.length - 1 && <span style={{ color: 'var(--text-disabled)' }}> / </span>}
          </span>
        ))}
      </div>

      <div style={{ border: '1px solid var(--border)', borderRadius: 4, overflow: 'hidden' }}>
        {browse.isLoading && <div style={emptyCell}>Loading…</div>}
        {browse.error && (
          <div style={{ ...emptyCell, color: '#F85149' }}>
            Failed to load: {(browse.error as Error).message}
          </div>
        )}
        {browse.data && (() => {
          // Defensive: agents on pre-v5.60.12 returned `null` for empty
          // dirs (Go's nil slice serialises as null), and the prior
          // truthiness check fell through to `.length` on null →
          // "null is not an object" SPA crash. Always coalesce to [].
          const entries = Array.isArray(browse.data.entries) ? browse.data.entries : [];
          if (entries.length === 0) {
            return <div style={emptyCell}>Empty directory.</div>;
          }
          return (
          <table className="data-table">
            <thead>
              <tr>
                <th>Name</th>
                <th style={{ width: 120, textAlign: 'right' }}>Size</th>
                <th style={{ width: 180 }}>Modified</th>
                <th style={{ width: 80 }} />
              </tr>
            </thead>
            <tbody>
              {[...entries]
                .sort((a, b) => Number(b.is_dir) - Number(a.is_dir) || a.name.localeCompare(b.name))
                .map((e) => {
                  const fullPath = (path.replace(/\/+$/, '') + '/' + e.name).replace(/^\/+/, '/');
                  return (
                    <tr key={e.name}>
                      <td>
                        <span
                          style={{
                            cursor: e.is_dir ? 'pointer' : 'default',
                            color: e.is_dir ? 'var(--text-strong)' : 'var(--text-body)',
                            fontWeight: e.is_dir ? 500 : 400,
                          }}
                          onClick={() => e.is_dir && setPath(fullPath)}
                        >
                          {e.is_dir ? '📁' : fileIcon(e.name)} {e.name}
                        </span>
                      </td>
                      <td style={{ textAlign: 'right', color: 'var(--text-muted)' }}>
                        {e.is_dir ? '—' : fmtSize(e.size)}
                      </td>
                      <td style={{ color: 'var(--text-faded)', fontSize: 11 }}>{e.modified || '—'}</td>
                      <td>
                        {!e.is_dir && (
                          <button
                            type="button"
                            className="btn-danger-sm"
                            disabled={!canDelete}
                            title={!canDelete ? 'Storage delete disabled or missing permission' : ''}
                            onClick={async () => {
                              if (await confirmFn({ title: 'Delete file?', body: <>Delete <code>{e.name}</code>?</> })) del.mutate(fullPath);
                            }}
                          >
                            Delete
                          </button>
                        )}
                      </td>
                    </tr>
                  );
                })}
            </tbody>
          </table>
          );
        })()}
      </div>
    </Modal>
  );
}

function splitCrumbs(path: string, root: string): { label: string; path: string }[] {
  // Always anchor the breadcrumb at the pool root so the user can't
  // navigate above the pool. The root segment is the pool label.
  if (!path.startsWith(root)) return [{ label: path, path }];
  const rel = path.slice(root.length).replace(/^\/+/, '');
  const parts = rel ? rel.split('/') : [];
  const out: { label: string; path: string }[] = [{ label: root, path: root }];
  let acc = root.replace(/\/+$/, '');
  for (const p of parts) {
    acc = acc + '/' + p;
    out.push({ label: p, path: acc });
  }
  return out;
}

function fmtSize(bytes: number): string {
  if (!bytes) return '0 B';
  if (bytes > 1e12) return `${(bytes / 1e12).toFixed(2)} TB`;
  if (bytes > 1e9) return `${(bytes / 1e9).toFixed(2)} GB`;
  if (bytes > 1e6) return `${(bytes / 1e6).toFixed(1)} MB`;
  if (bytes > 1e3) return `${(bytes / 1e3).toFixed(1)} kB`;
  return `${bytes} B`;
}

function fileIcon(name: string): string {
  const ext = name.toLowerCase().split('.').pop() ?? '';
  if (['qcow2', 'raw', 'vmdk', 'vhd', 'vdi'].includes(ext)) return '💾';
  if (['iso', 'img'].includes(ext)) return '💿';
  if (['xml', 'json', 'yaml', 'yml', 'toml'].includes(ext)) return '📄';
  return '📎';
}

const crumbRowStyle: React.CSSProperties = {
  display: 'flex',
  gap: 6,
  alignItems: 'center',
  padding: '6px 10px',
  marginBottom: 10,
  fontSize: 11,
  fontFamily: "'SF Mono', monospace",
  background: 'var(--bg-input)',
  border: '1px solid var(--border)',
  borderRadius: 4,
  flexWrap: 'wrap',
};
const emptyCell: React.CSSProperties = {
  padding: 20,
  fontSize: 12,
  color: 'var(--text-muted)',
  textAlign: 'center',
};
