import { Component, type ErrorInfo, type ReactNode } from 'react';

// Top-level ErrorBoundary. Pre-v5.50 a render-time exception in any
// lazy-loaded page (Settings, VMDetail, Docs) white-screened the whole
// SPA with no recovery and no message. The operator only knew
// something was wrong because the viewport went blank.
//
// This boundary catches the throw, renders a minimal recovery panel
// with the error message and a "Reset to dashboard" button, and
// reports the error to /api/client-error so server-side log shipping
// picks it up alongside operator-driven audit events.

interface State {
  err?: Error;
  info?: ErrorInfo;
}

export class ErrorBoundary extends Component<{ children: ReactNode }, State> {
  state: State = {};

  static getDerivedStateFromError(err: Error): State {
    return { err };
  }

  componentDidCatch(err: Error, info: ErrorInfo) {
    this.setState({ err, info });
    // Best-effort report. If the API is itself broken, the network
    // failure is logged to the console and we don't loop.
    void fetch('/api/client-error', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        message: err.message,
        stack: err.stack,
        component_stack: info.componentStack,
        url: window.location.href,
        user_agent: navigator.userAgent,
      }),
    }).catch(() => {
      // Swallow — the visible recovery panel is the user-facing signal.
    });
  }

  reset = () => {
    this.setState({ err: undefined, info: undefined });
    // Hard navigation back to root: even if the broken render-tree
    // survives setState (it shouldn't), a top-of-stack navigation
    // forces a full reload of the SPA bundle.
    window.location.href = '/';
  };

  render() {
    if (!this.state.err) return this.props.children;
    return (
      <div role="alert" style={panelStyle}>
        <div style={{ maxWidth: 540, padding: '0 24px' }}>
          <h1 style={{ margin: 0, fontSize: 20, color: 'var(--text-strong)' }}>
            Something went wrong on this page.
          </h1>
          <p style={{ marginTop: 12, color: 'var(--text-muted)' }}>
            kvm-center hit an unexpected error and stopped rendering. Your data is unaffected — only this view is disrupted. The error has been reported to the server log; you can reset to the dashboard or reload the tab.
          </p>
          <pre style={preStyle}>{this.state.err.message}</pre>
          <div style={{ display: 'flex', gap: 10, marginTop: 16 }}>
            <button onClick={this.reset} style={btnPrimary}>
              Reset to dashboard
            </button>
            <button onClick={() => window.location.reload()} style={btnGhost}>
              Reload tab
            </button>
          </div>
        </div>
      </div>
    );
  }
}

const panelStyle: React.CSSProperties = {
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  minHeight: '100vh',
  background: 'var(--bg-app)',
  fontFamily: 'inherit',
};

const preStyle: React.CSSProperties = {
  marginTop: 16,
  padding: 12,
  borderRadius: 6,
  background: 'var(--surface-2)',
  border: '1px solid var(--border)',
  color: 'var(--text-body)',
  fontSize: 12,
  whiteSpace: 'pre-wrap',
  wordBreak: 'break-word',
  maxHeight: 200,
  overflow: 'auto',
};

const btnPrimary: React.CSSProperties = {
  height: 32,
  padding: '0 16px',
  background: 'var(--accent)',
  color: '#fff',
  border: 'none',
  borderRadius: 5,
  fontWeight: 500,
  cursor: 'pointer',
};

const btnGhost: React.CSSProperties = {
  height: 32,
  padding: '0 16px',
  background: 'transparent',
  color: 'var(--text-body)',
  border: '1px solid var(--border)',
  borderRadius: 5,
  fontWeight: 500,
  cursor: 'pointer',
};
