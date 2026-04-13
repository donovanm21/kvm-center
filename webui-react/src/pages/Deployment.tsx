import { useEffect, useState } from 'react';

/**
 * Deployment view mirrors v3's scripts landing page at a high level —
 * it points admins at the build portal (which hosts the agent /
 * kvm-tools binaries and the full installer instructions) and calls
 * out the one-liner curl snippets for quick reference.
 *
 * Keeping it short on purpose: the portal page is the canonical
 * source of truth for install commands, and we don't want two copies
 * of snippets drifting out of sync.
 */
export function Deployment() {
  const portalURL = `${location.protocol}//${location.hostname}:8097`;
  const serverURL = `${location.protocol}//${location.host}`;
  const [reachable, setReachable] = useState<'checking' | 'ok' | 'down'>('checking');

  useEffect(() => {
    const ctrl = new AbortController();
    fetch(portalURL, { mode: 'no-cors', signal: ctrl.signal })
      .then(() => setReachable('ok'))
      .catch(() => setReachable('down'));
    return () => ctrl.abort();
  }, [portalURL]);

  return (
    <div style={{ display: 'grid', gap: 12 }}>
      <div className="panel" style={{ borderLeft: '3px solid var(--accent)' }}>
        <h2>Build portal</h2>
        <p style={{ margin: 0, color: 'var(--text-body)', fontSize: 12, lineHeight: 1.55 }}>
          The build container publishes the hypervisor agent, guest daemon, and install
          scripts at the address below. Open it in a new tab for browsable downloads and
          the full install instructions.
        </p>
        <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginTop: 12 }}>
          <a
            href={portalURL}
            target="_blank"
            rel="noopener noreferrer"
            className="btn-primary-sm"
            style={{ textDecoration: 'none' }}
          >
            Open portal ↗
          </a>
          <code style={codeStyle}>{portalURL}</code>
          <PortalStatus state={reachable} />
        </div>
      </div>

      <div className="panel">
        <h2>Quick-install snippets</h2>
        <p style={{ margin: 0, color: 'var(--text-muted)', fontSize: 12 }}>
          Run on a freshly-provisioned hypervisor (replace <code style={chipStyle}>SERVER</code> with this
          kvm-center's URL; the build portal URL is baked in below).
        </p>

        <SnippetBlock
          title="Hypervisor agent"
          body={`curl -fsSL ${portalURL}/kvm-center-agent-amd64 -o /tmp/kvm-center-agent-amd64
curl -fsSL ${portalURL}/install-agent.sh     -o /tmp/install-agent.sh
chmod +x /tmp/install-agent.sh /tmp/kvm-center-agent-amd64
sudo /tmp/install-agent.sh --server ${serverURL}`}
        />

        <SnippetBlock
          title="Guest tools (Linux)"
          body={`curl -fsSL ${portalURL}/kvm-tools-linux-amd64 -o /tmp/kvm-tools-amd64
curl -fsSL ${portalURL}/install-kvm-tools.sh  -o /tmp/install-kvm-tools.sh
chmod +x /tmp/install-kvm-tools.sh /tmp/kvm-tools-amd64
sudo /tmp/install-kvm-tools.sh --server ${serverURL}`}
        />
      </div>

      <div className="panel">
        <h2>Verify an install</h2>
        <SnippetBlock
          title=""
          body={`# 1. Server liveness
curl -s ${serverURL}/health

# 2. Hosts the server knows about
curl -s ${serverURL}/api/hosts | python3 -m json.tool

# 3. All VMs (look for guest_agent: true after kvm-tools installs)
curl -s ${serverURL}/api/vms | python3 -m json.tool`}
        />
        <p style={{ margin: '8px 0 0 0', fontSize: 11, color: 'var(--text-faded)' }}>
          The kvm-tools daemon waits for the next 30 s tick before its first telemetry
          burst, so VM detail takes up to a minute to flip to "guest tools: kvm-tools".
        </p>
      </div>
    </div>
  );
}

function PortalStatus({ state }: { state: 'checking' | 'ok' | 'down' }) {
  if (state === 'checking') {
    return <span style={{ fontSize: 11, color: 'var(--text-muted)' }}>Probing…</span>;
  }
  return (
    <span
      style={{
        display: 'inline-flex',
        alignItems: 'center',
        gap: 6,
        fontSize: 11,
        padding: '2px 8px',
        borderRadius: 10,
        background: state === 'ok' ? '#1E3D23' : '#3D1E1E',
        color: state === 'ok' ? '#3FB950' : '#F85149',
        fontWeight: 600,
      }}
    >
      {state === 'ok' ? '● online' : '● unreachable'}
    </span>
  );
}

function SnippetBlock({ title, body }: { title: string; body: string }) {
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(body);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      /* user rejected clipboard permission */
    }
  };
  return (
    <div style={{ marginTop: title ? 14 : 10 }}>
      {title && <div style={{ fontSize: 10, textTransform: 'uppercase', letterSpacing: '0.08em', color: 'var(--text-faded)', marginBottom: 6 }}>{title}</div>}
      <div style={{ position: 'relative' }}>
        <pre style={preStyle}>{body}</pre>
        <button type="button" onClick={copy} style={copyBtnStyle}>
          {copied ? 'Copied!' : 'Copy'}
        </button>
      </div>
    </div>
  );
}

const codeStyle: React.CSSProperties = {
  fontFamily: "'SF Mono', monospace",
  fontSize: 11,
  padding: '3px 8px',
  borderRadius: 3,
  background: 'var(--bg-input)',
  border: '1px solid var(--border)',
  color: 'var(--text-body)',
};
const chipStyle: React.CSSProperties = {
  fontFamily: "'SF Mono', monospace",
  background: 'var(--bg-input)',
  padding: '1px 6px',
  borderRadius: 3,
  color: 'var(--accent-hover)',
};
const preStyle: React.CSSProperties = {
  margin: 0,
  padding: '12px 14px',
  background: 'var(--bg-input)',
  border: '1px solid var(--border)',
  borderRadius: 4,
  fontFamily: "'SF Mono', monospace",
  fontSize: 11,
  color: 'var(--text-strong)',
  whiteSpace: 'pre',
  overflowX: 'auto',
  lineHeight: 1.55,
};
const copyBtnStyle: React.CSSProperties = {
  position: 'absolute',
  top: 6,
  right: 6,
  height: 24,
  padding: '0 10px',
  fontSize: 11,
  background: 'var(--surface-2)',
  color: 'var(--text-body)',
  border: '1px solid var(--border)',
  borderRadius: 3,
  cursor: 'pointer',
};
