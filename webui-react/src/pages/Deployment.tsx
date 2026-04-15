import { useMemo, useState } from 'react';

type OS = 'rhel' | 'debian' | 'windows';

/**
 * Deployment page — one place that hands out install snippets for the
 * two agents (guest-side kvm-tools and host-side kvm-center-agent), a
 * direct-download manifest, and the verification curls.
 *
 * v5.16: downloads are served by the kvm-center-server itself under
 * /downloads/*. The standalone build portal on :8097 is retired.
 */
export function Deployment() {
  const serverURL    = `${location.protocol}//${location.host}`;
  // Hypervisors + guests pull install scripts before they have any way
  // to trust a self-signed cert, so default the installer URLs to the
  // plaintext :8090 endpoint colocated with the server. The UI still
  // offers a TLS URL for the verification commands.
  const plaintextURL = `http://${location.hostname}:8090`;
  const downloads    = `${plaintextURL}/downloads`;

  return (
    <div style={{ display: 'grid', gap: 14 }}>
      <IntroBanner downloads={downloads} />

      <PackageCard
        icon={<span className="tone-icon-tile accent" style={iconTileStyle}>✚</span>}
        title="KVM Tools (Guest VM Daemon)"
        description={
          <>
            Install inside a guest VM to surface guest OS, hostname, IP addresses, and
            per-mountpoint filesystem usage in the kvm-center UI. Reports to{' '}
            <code style={inlineCode}>{plaintextURL}</code> every 30 seconds. Once installed, the
            VM's pill in the Summary tab flips to{' '}
            <strong style={{ color: '#3FB950' }}>KVM Tools: running</strong>.
          </>
        }
        snippets={{
          rhel: {
            label: 'RHEL / Rocky',
            body:
              `# Run as root on the guest VM (Rocky / RHEL / CentOS / Fedora)
curl -fsSL ${downloads}/kvm-tools-linux-amd64 -o /tmp/kvm-tools-amd64
curl -fsSL ${downloads}/install-kvm-tools.sh -o /tmp/install-kvm-tools.sh
chmod +x /tmp/install-kvm-tools.sh /tmp/kvm-tools-amd64
sudo /tmp/install-kvm-tools.sh --server ${plaintextURL}`,
            footer: 'Installs /opt/kvm-tools/kvm-tools and a systemd unit. Requires root.',
          },
          debian: {
            label: 'Debian / Ubuntu',
            body:
              `# Run as root on the guest VM (Debian / Ubuntu / Mint)
curl -fsSL ${downloads}/kvm-tools-linux-amd64 -o /tmp/kvm-tools-amd64
curl -fsSL ${downloads}/install-kvm-tools.sh -o /tmp/install-kvm-tools.sh
chmod +x /tmp/install-kvm-tools.sh /tmp/kvm-tools-amd64
sudo /tmp/install-kvm-tools.sh --server ${plaintextURL}`,
            footer: 'Installs /opt/kvm-tools/kvm-tools and a systemd unit. Requires root.',
          },
          windows: {
            label: 'Windows (PowerShell)',
            body:
              `# Run from an elevated PowerShell prompt on the guest VM
$server = '${plaintextURL}'
$tmp = Join-Path $env:TEMP 'kvm-tools-install'
New-Item -ItemType Directory -Force -Path $tmp | Out-Null
Invoke-WebRequest -Uri "$server/downloads/kvm-tools-windows-amd64.exe" -OutFile "$tmp\\kvm-tools.exe" -UseBasicParsing
Invoke-WebRequest -Uri "$server/downloads/install-kvm-tools.ps1"       -OutFile "$tmp\\install-kvm-tools.ps1" -UseBasicParsing
Set-Location $tmp
.\\install-kvm-tools.ps1 -Server $server`,
            footer: 'Registers a scheduled task running as SYSTEM on next boot.',
          },
        }}
      />

      <PackageCard
        icon={<span className="tone-icon-tile info" style={iconTileStyle}>↓</span>}
        title="KVM Center Agent (Hypervisor)"
        description={
          <>
            Install on a new KVM hypervisor (Rocky / RHEL / Debian etc.) to add it to the
            kvm-center fleet. Collects host metrics, lists VMs / snapshots / storage pools,
            and serves the per-VM VNC console + power actions. Listens on port{' '}
            <code style={inlineCode}>8081</code> for command routing from the server.
          </>
        }
        snippets={{
          rhel: {
            label: 'RHEL / Rocky',
            body:
              `# Run as root on the new KVM hypervisor (Rocky / RHEL / CentOS)
curl -fsSL ${downloads}/kvm-center-agent-amd64 -o /tmp/kvm-center-agent-amd64
curl -fsSL ${downloads}/install-agent.sh       -o /tmp/install-agent.sh
chmod +x /tmp/install-agent.sh /tmp/kvm-center-agent-amd64
# Open the agent command port (only required if firewalld is active)
sudo firewall-cmd --add-port=8081/tcp --permanent && sudo firewall-cmd --reload
sudo /tmp/install-agent.sh --server ${plaintextURL}`,
            footer:
              'Requires libvirtd running and virsh in PATH. The installer writes /etc/systemd/system/kvm-center-agent.service and enables it at boot.',
          },
          debian: {
            label: 'Debian / Ubuntu',
            body:
              `# Run as root on the new KVM hypervisor (Debian / Ubuntu)
curl -fsSL ${downloads}/kvm-center-agent-amd64 -o /tmp/kvm-center-agent-amd64
curl -fsSL ${downloads}/install-agent.sh       -o /tmp/install-agent.sh
chmod +x /tmp/install-agent.sh /tmp/kvm-center-agent-amd64
# Open the agent command port (only required if ufw is active)
sudo ufw allow 8081/tcp
sudo /tmp/install-agent.sh --server ${plaintextURL}`,
            footer:
              'Requires libvirtd running and virsh in PATH. The installer writes /etc/systemd/system/kvm-center-agent.service and enables it at boot.',
          },
        }}
      />

      <DirectDownloads downloads={downloads} />

      <VerifyCard serverURL={serverURL} />
    </div>
  );
}

/* ── Intro ───────────────────────────────────────────────── */
function IntroBanner({ downloads }: { downloads: string }) {
  return (
    <div style={introBannerStyle}>
      <div>
        <strong style={{ color: 'var(--text-strong)' }}>One-shot installs.</strong>{' '}
        Copy a snippet, paste it on the target host, done. Binaries and install
        scripts are served by this server at{' '}
        <a href={downloads} target="_blank" rel="noopener noreferrer" style={introLinkStyle}>
          {downloads}
        </a>{' '}
        — cross-compiled from the commit the current server image was built from.
      </div>
    </div>
  );
}

/* ── Per-package card ────────────────────────────────────── */
interface SnippetDef { label: string; body: string; footer?: string }
interface PackageCardProps {
  icon: React.ReactNode;
  title: string;
  description: React.ReactNode;
  snippets: Partial<Record<OS, SnippetDef>>;
}
function PackageCard({ icon, title, description, snippets }: PackageCardProps) {
  const available = useMemo(
    () => (Object.keys(snippets) as OS[]).filter((k) => snippets[k]),
    [snippets],
  );
  const [active, setActive] = useState<OS>(available[0]);
  const current = snippets[active];

  return (
    <div className="panel" style={{ padding: 22 }}>
      <div style={{ display: 'flex', alignItems: 'flex-start', gap: 12 }}>
        {icon}
        <div style={{ flex: 1, minWidth: 0 }}>
          <h2 style={{ margin: 0, fontSize: 16, fontWeight: 600, color: 'var(--text-strong)' }}>{title}</h2>
          <p style={{ margin: '6px 0 0 0', fontSize: 12, color: 'var(--text-body)', lineHeight: 1.55 }}>
            {description}
          </p>
        </div>
      </div>

      {available.length > 1 && (
        <div style={tabsStyle}>
          {available.map((k) => (
            <div
              key={k}
              role="tab"
              aria-selected={active === k}
              onClick={() => setActive(k)}
              style={{
                cursor: 'pointer',
                padding: '6px 14px',
                fontSize: 12,
                borderBottom: `2px solid ${active === k ? 'var(--accent)' : 'transparent'}`,
                color: active === k ? 'var(--text-strong)' : 'var(--text-muted)',
              }}
            >
              {snippets[k]!.label}
            </div>
          ))}
        </div>
      )}

      {current && <CopyBlock body={current.body} />}
      {current?.footer && (
        <div style={{ marginTop: 8, fontSize: 11, color: 'var(--text-faded)' }}>{current.footer}</div>
      )}
    </div>
  );
}

/* ── Direct downloads ────────────────────────────────────── */
function DirectDownloads({ downloads }: { downloads: string }) {
  const files: { name: string; desc: string }[] = [
    { name: 'kvm-tools-linux-amd64',        desc: 'KVM Tools — Linux x86_64' },
    { name: 'kvm-tools-linux-arm64',        desc: 'KVM Tools — Linux ARM64' },
    { name: 'kvm-tools-windows-amd64.exe',  desc: 'KVM Tools — Windows x86_64' },
    { name: 'install-kvm-tools.sh',         desc: 'KVM Tools installer (Linux)' },
    { name: 'install-kvm-tools.ps1',        desc: 'KVM Tools installer (Windows)' },
    { name: 'kvm-center-agent-amd64',       desc: 'Hypervisor agent — Linux x86_64' },
    { name: 'kvm-center-agent-arm64',       desc: 'Hypervisor agent — Linux ARM64' },
    { name: 'install-agent.sh',             desc: 'Hypervisor agent installer' },
    { name: 'kvmctl-linux-amd64',           desc: 'kvmctl CLI — Linux x86_64' },
    { name: 'kvmctl-linux-arm64',           desc: 'kvmctl CLI — Linux ARM64' },
    { name: 'kvmctl-darwin-amd64',          desc: 'kvmctl CLI — macOS Intel' },
    { name: 'kvmctl-darwin-arm64',          desc: 'kvmctl CLI — macOS Apple Silicon' },
    { name: 'kvmctl-windows-amd64.exe',     desc: 'kvmctl CLI — Windows x86_64' },
    { name: 'install-kvmctl.sh',            desc: 'kvmctl installer (Linux / macOS)' },
    { name: 'install-kvmctl.ps1',           desc: 'kvmctl installer (Windows)' },
  ];
  return (
    <div className="panel" style={{ padding: 22 }}>
      <h2 style={{ margin: 0, fontSize: 15, fontWeight: 600, color: 'var(--text-strong)', display: 'flex', alignItems: 'center', gap: 8 }}>
        <span style={{ color: '#3FB950' }}>↓</span> Direct Downloads
      </h2>
      <p style={{ margin: '4px 0 14px 0', fontSize: 12, color: 'var(--text-muted)' }}>
        Cross-compiled into the server image at build time. Served at{' '}
        <a href={downloads} target="_blank" rel="noopener noreferrer" style={introLinkStyle}>{downloads}</a>.
      </p>
      <table className="data-table">
        <thead>
          <tr><th>File</th><th>Description</th></tr>
        </thead>
        <tbody>
          {files.map((f) => (
            <tr key={f.name}>
              <td>
                <a
                  href={`${downloads}/${f.name}`}
                  target="_blank"
                  rel="noopener noreferrer"
                  style={{ color: 'var(--accent-hover)', fontFamily: "'SF Mono', monospace", fontSize: 12 }}
                >
                  {f.name}
                </a>
              </td>
              <td style={{ color: 'var(--text-muted)' }}>{f.desc}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

/* ── Verification ────────────────────────────────────────── */
function VerifyCard({ serverURL }: { serverURL: string }) {
  const snippet = `# 1. Server liveness
curl -sk ${serverURL}/health

# 2. List all hosts the server knows about
curl -sk ${serverURL}/api/hosts | python3 -m json.tool

# 3. List all VMs (look for guest_agent: true after kvm-tools install)
curl -sk ${serverURL}/api/vms | python3 -m json.tool`;
  return (
    <div className="panel" style={{ padding: 22 }}>
      <h2 style={{ margin: 0, fontSize: 15, fontWeight: 600, color: 'var(--text-strong)', display: 'flex', alignItems: 'center', gap: 8 }}>
        <span style={{ color: '#FFB020' }}>✓</span> Verifying a Successful Install
      </h2>
      <p style={{ margin: '4px 0 14px 0', fontSize: 12, color: 'var(--text-muted)' }}>
        After running the installer the new agent or kvm-tools should appear in the kvm-center inventory
        within 30 seconds. Use these checks to confirm.
      </p>
      <CopyBlock body={snippet} />
      <p style={{ margin: '10px 0 0 0', fontSize: 11, color: 'var(--text-faded)' }}>
        The kvm-tools daemon waits for the next 30 s tick before its first telemetry burst, so VM detail
        takes up to a minute to flip to "guest tools: kvm-tools".
      </p>
    </div>
  );
}

/* ── Copy block ──────────────────────────────────────────── */
function CopyBlock({ body }: { body: string }) {
  const [state, setState] = useState<'idle' | 'copied' | 'failed'>('idle');
  const flash = (s: 'copied' | 'failed') => {
    setState(s);
    setTimeout(() => setState('idle'), 1800);
  };
  const copy = async () => {
    if (window.isSecureContext && navigator.clipboard?.writeText) {
      try {
        await navigator.clipboard.writeText(body);
        flash('copied');
        return;
      } catch {
        // permission denied (rare) — fall through to the textarea path
      }
    }
    try {
      const ta = document.createElement('textarea');
      ta.value = body;
      ta.setAttribute('readonly', '');
      ta.style.position = 'fixed';
      ta.style.top = '0';
      ta.style.left = '0';
      ta.style.opacity = '0';
      document.body.appendChild(ta);
      ta.focus();
      ta.select();
      const ok = document.execCommand('copy');
      document.body.removeChild(ta);
      flash(ok ? 'copied' : 'failed');
    } catch {
      flash('failed');
    }
  };
  return (
    <div style={{ position: 'relative', marginTop: 10 }}>
      <pre style={preStyle}>{body}</pre>
      <button
        type="button"
        onClick={copy}
        style={copyBtnStyle}
        title={state === 'failed' ? 'Browser blocked the copy — select the text and copy manually.' : ''}
      >
        {state === 'copied' ? 'Copied!' : state === 'failed' ? 'Copy failed' : 'Copy'}
      </button>
    </div>
  );
}

/* ── Styles ──────────────────────────────────────────────── */
const introBannerStyle: React.CSSProperties = {
  background: 'var(--surface)',
  border: '1px solid var(--border)',
  borderLeft: '3px solid var(--accent)',
  borderRadius: 6,
  padding: '12px 16px',
  fontSize: 12,
  color: 'var(--text-body)',
  lineHeight: 1.6,
};
const introLinkStyle: React.CSSProperties = {
  color: 'var(--accent-hover)',
  fontFamily: "'SF Mono', monospace",
  background: 'var(--bg-input)',
  padding: '1px 6px',
  borderRadius: 3,
  textDecoration: 'none',
};
const inlineCode: React.CSSProperties = {
  background: 'var(--bg-input)',
  padding: '1px 6px',
  borderRadius: 3,
  color: 'var(--accent-hover)',
  fontFamily: "'SF Mono', monospace",
  fontSize: 11,
};
const iconTileStyle: React.CSSProperties = {
  display: 'inline-flex',
  alignItems: 'center',
  justifyContent: 'center',
  width: 28,
  height: 28,
  borderRadius: 14,
  fontSize: 14,
  fontWeight: 700,
  flexShrink: 0,
};
const tabsStyle: React.CSSProperties = {
  display: 'flex',
  gap: 4,
  marginTop: 14,
  borderBottom: '1px solid var(--border)',
};
const preStyle: React.CSSProperties = {
  margin: 0,
  padding: '14px 16px',
  background: 'var(--bg-input)',
  border: '1px solid var(--border)',
  borderRadius: 5,
  fontFamily: "'SF Mono', monospace",
  fontSize: 12,
  color: 'var(--text-strong)',
  whiteSpace: 'pre',
  overflowX: 'auto',
  lineHeight: 1.55,
};
const copyBtnStyle: React.CSSProperties = {
  position: 'absolute',
  top: 8,
  right: 8,
  height: 26,
  padding: '0 12px',
  fontSize: 11,
  background: 'var(--surface-2)',
  color: 'var(--accent)',
  border: '1px solid var(--accent-dim, var(--accent))',
  borderRadius: 3,
  cursor: 'pointer',
  fontWeight: 600,
  letterSpacing: '0.04em',
};
