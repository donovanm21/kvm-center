import { useId, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { apiGet, apiPut } from '@/api/client';
import { useAuth } from '@/hooks/useAuth';
import { qk } from '@/queryKeys';
import type { VM } from '@/types';

export function VMTags({ uuid, tags = [] }: { uuid: string; tags?: string[] }) {
  const qc = useQueryClient();
  const { hasPerm } = useAuth();
  const inputId = useId();
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState('');
  const [expected, setExpected] = useState<string[]>([]);
  const [message, setMessage] = useState('');
  const catalog = useQuery({
    queryKey: qk.tags(), enabled: editing,
    queryFn: () => apiGet<{ tags: { tag: string; count: number }[] }>('/api/tags'),
  });
  const save = useMutation({
    mutationFn: () => apiPut<{ tags: string[] }>(`/api/vms/${encodeURIComponent(uuid)}/tags`, {
      tags: draft.split(',').map(t => t.trim()).filter(Boolean), expected_tags: expected,
    }),
    onSuccess: (result) => {
      qc.setQueryData<VM[]>(qk.vms(), old => old?.map(vm => vm.uuid === uuid ? { ...vm, tags: result.tags } : vm));
      void qc.invalidateQueries({ queryKey: qk.vms() });
      void qc.invalidateQueries({ queryKey: qk.tags() });
      setEditing(false); setMessage('Tags saved');
    },
  });
  const reload = useMutation({
    mutationFn: () => apiGet<{ tags: string[] }>(`/api/vms/${encodeURIComponent(uuid)}/tags`),
    onSuccess: result => { setExpected(result.tags); setDraft(result.tags.join(', ')); save.reset(); },
  });
  const busy = save.isPending || reload.isPending;
  const begin = () => {
    setExpected([...tags]); setDraft(tags.join(', ')); setMessage(''); save.reset(); reload.reset(); setEditing(true);
  };
  return (
    <section aria-label="VM tags" data-testid="vm-tags" style={{ display: 'grid', gap: 10 }}>
      <h2 style={{ margin: 0 }}>Tags</h2>
      {!editing ? <>
        <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
          {tags.length === 0 && <span>No tags</span>}
          {tags.map(tag => <span key={tag} className="tone-pill muted" style={{ textTransform: 'none' }}>{tag}</span>)}
        </div>
        {hasPerm('vm.edit') && <button type="button" className="btn-secondary-sm" onClick={begin}>{tags.length ? 'Edit tags' : 'Add tags'}</button>}
      </> : <>
        <label htmlFor={inputId}>Tags, separated by commas</label>
        <input id={inputId} value={draft} disabled={busy} autoFocus onChange={e => setDraft(e.target.value)} placeholder="database, production" />
        <p style={{ margin: 0, fontSize: 12 }}>Create a tag by assigning it here, or enter an existing tag. Use up to 24 tags, each 1–32 letters or digits with internal dots, underscores or hyphens. Tags are saved in lowercase. Clear the field to remove all assignments.</p>
        {catalog.data && catalog.data.tags.length > 0 && <p style={{ margin: 0, fontSize: 12 }}>Existing tags: {catalog.data.tags.map(t => t.tag).join(', ')}</p>}
        {catalog.isError && <p role="status">Existing tags could not be loaded. You can still enter tags.</p>}
        {(save.error || reload.error) && <p role="alert">{(reload.error || save.error)?.message}</p>}
        <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
          <button type="button" className="btn-primary-sm" disabled={busy} onClick={() => save.mutate()}>{save.isPending ? 'Saving tags…' : 'Save tags'}</button>
          <button type="button" className="btn-secondary-sm" disabled={busy} onClick={() => { setEditing(false); setMessage(''); }}>Cancel</button>
          {save.isError && <button type="button" className="btn-secondary-sm" disabled={busy} onClick={() => reload.mutate()}>Reload tags</button>}
        </div>
      </>}
      {message && <p role="status">{message}</p>}
    </section>
  );
}
