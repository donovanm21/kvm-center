package main

import "html/template"

// noVNC console HTML served by /api/console/<host>/<vm>. Mirrors the
// agent's original page but points its WebSocket URL at the server's
// /api/console-ws proxy so the whole session rides the HTTPS origin
// (and the operator's session cookie). Styling and layout are
// intentionally kept in lockstep with the agent page so the v5.0.3 →
// v5.0.4 upgrade is visually invisible to operators.

var serverConsoleTmpl = template.Must(template.New("server-console").Parse(serverConsoleHTML))

const serverConsoleHTML = `<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<title>{{.VMName}} — console</title>
<style>
  html, body { margin: 0; height: 100%; background: #0B0E13; color: #C9D1D9; font-family: -apple-system, BlinkMacSystemFont, 'Inter', 'Segoe UI', Roboto, sans-serif; font-size: 13px; }
  #screen { width: 100vw; height: 100vh; background: #000; overflow: hidden; position: relative; }
  #overlay { position: fixed; inset: 0; display: flex; align-items: center; justify-content: center; flex-direction: column; background: rgba(11,14,19,0.92); z-index: 10; text-align: center; padding: 24px; }
  #overlay h1 { margin: 0 0 8px 0; font-size: 17px; color: #F0F6FC; font-weight: 600; }
  #overlay p { margin: 0; max-width: 520px; color: #8B949E; line-height: 1.55; }
  #overlay .status { margin-top: 14px; font-size: 11px; color: #6E7681; text-transform: uppercase; letter-spacing: 0.08em; }
  #overlay .err { margin-top: 14px; color: #F85149; font-family: 'SF Mono', monospace; font-size: 11px; white-space: pre-wrap; word-break: break-word; }
  #overlay .spin { display: inline-block; width: 8px; height: 8px; background: #E3571E; border-radius: 50%; margin-right: 8px; animation: p 1s ease-in-out infinite; }
  @keyframes p { 50% { opacity: 0.3; } }
</style>
</head>
<body>
<div id="screen"></div>
<div id="overlay">
  <h1>{{.VMName}}</h1>
  <p>Connecting through the kvm-center server. If sharing this URL doesn't give someone else access, it's working: every request re-validates your session.</p>
  <div class="status"><span class="spin"></span><span id="status-text">connecting…</span></div>
  <div class="err" id="err"></div>
</div>
<script type="module">
  import RFB from 'https://cdn.jsdelivr.net/npm/@novnc/novnc@1.4.0/lib/rfb.js';

  const VMNAME = {{.VMName}};
  const HOST = {{.HostID}};
  const status = document.getElementById('status-text');
  const overlay = document.getElementById('overlay');
  const errBox = document.getElementById('err');
  let rfb = null;
  let attempts = 0;
  const MAX_ATTEMPTS = 5;

  function wsURL() {
    // Same-origin WSS URL — the session cookie rides along automatically.
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    return proto + '//' + location.host +
      '/api/console-ws/' + encodeURIComponent(HOST) + '/' + encodeURIComponent(VMNAME);
  }

  function connect() {
    overlay.style.display = 'flex';
    status.textContent = attempts === 0 ? 'connecting…' : 'reconnecting (' + (attempts+1) + '/' + MAX_ATTEMPTS + ')…';
    errBox.textContent = '';
    try {
      rfb = new RFB(document.getElementById('screen'), wsURL(), { credentials: { password: '' } });
    } catch (err) {
      errBox.textContent = String(err);
      return;
    }
    rfb.viewOnly = false;
    rfb.scaleViewport = true;
    rfb.resizeSession = true;
    rfb.showDotCursor = true;
    rfb.addEventListener('connect', () => {
      attempts = 0;
      overlay.style.display = 'none';
    });
    rfb.addEventListener('disconnect', (ev) => {
      if (attempts < MAX_ATTEMPTS) {
        attempts++;
        setTimeout(connect, 800);
      } else {
        status.textContent = 'disconnected';
        errBox.textContent = ev?.detail?.reason || 'session closed — check that your kvm-center session is still active, then reload.';
      }
    });
    rfb.addEventListener('credentialsrequired', () => {
      rfb.sendCredentials({ password: '' });
    });
  }
  connect();
</script>
</body>
</html>
`
