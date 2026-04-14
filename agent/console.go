package main

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
)

const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

var consoleTmpl = template.Must(template.New("console").Parse(consoleHTML))

// handleConsolePage serves the noVNC console HTML for a specific VM.
// Requires a valid server-minted HMAC token — see console_auth.go.
// The rendered HTML forwards the same token+exp+nonce on the WebSocket
// URL so handleConsoleWS re-verifies at upgrade time.
func handleConsolePage(w http.ResponseWriter, r *http.Request) {
	vmName := strings.TrimPrefix(r.URL.Path, "/console/")
	if vmName == "" {
		http.Error(w, "vm name required", http.StatusBadRequest)
		return
	}
	if err := verifyConsoleToken(r, vmName); err != nil {
		log.Printf("console: denied page request for %s: %v", vmName, err)
		http.Error(w, "unauthorized: "+err.Error(), http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	q := r.URL.Query()
	data := map[string]any{
		"VMName":   vmName,
		"VMNameJS": template.JS(fmt.Sprintf("%q", vmName)),
		// Forward the token bits to the noVNC template so the WS URL
		// it constructs carries the same auth material.
		"Token": q.Get("token"),
		"Exp":   q.Get("exp"),
		"Nonce": q.Get("nonce"),
	}
	_ = consoleTmpl.Execute(w, data)
}

// handleConsoleWS upgrades to WebSocket and proxies traffic to the VM's VNC port.
func handleConsoleWS(w http.ResponseWriter, r *http.Request) {
	vmName := strings.TrimPrefix(r.URL.Path, "/ws/")
	if vmName == "" {
		http.Error(w, "vm name required", http.StatusBadRequest)
		return
	}
	if err := verifyConsoleToken(r, vmName); err != nil {
		log.Printf("console: denied ws for %s: %v", vmName, err)
		http.Error(w, "unauthorized: "+err.Error(), http.StatusUnauthorized)
		return
	}

	port, err := getVNCPort(vmName)
	if err != nil {
		log.Printf("console: vnc port lookup for %s failed: %v", vmName, err)
		http.Error(w, "VNC not available: "+err.Error(), http.StatusServiceUnavailable)
		return
	}

	conn, rw, err := wsUpgrade(w, r)
	if err != nil {
		log.Printf("console: ws upgrade for %s failed: %v", vmName, err)
		return
	}

	log.Printf("console: session start vm=%s vnc=127.0.0.1:%d", vmName, port)
	proxyWSToVNC(conn, rw, fmt.Sprintf("127.0.0.1:%d", port))
	log.Printf("console: session end vm=%s", vmName)
}

// getVNCPort returns the TCP port of the given VM's VNC display.
// It requires the VM to be running and have a VNC graphics device.
func getVNCPort(vmName string) (int, error) {
	stateOut, err := exec.Command("virsh", "domstate", vmName).Output()
	if err != nil {
		return 0, fmt.Errorf("domstate: %w", err)
	}
	state := strings.TrimSpace(string(stateOut))
	if state != "running" {
		return 0, fmt.Errorf("VM is not running (state=%s)", state)
	}

	out, err := exec.Command("virsh", "vncdisplay", vmName).Output()
	if err != nil {
		return 0, fmt.Errorf("vncdisplay: %w", err)
	}
	display := strings.TrimSpace(string(out))
	if display == "" {
		return 0, fmt.Errorf("no VNC display configured")
	}
	// virsh vncdisplay prints ":N" or "host:N"; the display number follows the last colon.
	colon := strings.LastIndex(display, ":")
	if colon < 0 {
		return 0, fmt.Errorf("unexpected vncdisplay output %q", display)
	}
	n, err := strconv.Atoi(display[colon+1:])
	if err != nil {
		return 0, fmt.Errorf("cannot parse display number from %q", display)
	}
	return 5900 + n, nil
}

// wsUpgrade completes the WebSocket handshake and returns the hijacked connection.
func wsUpgrade(w http.ResponseWriter, r *http.Request) (net.Conn, *bufio.ReadWriter, error) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return nil, nil, fmt.Errorf("not a websocket upgrade")
	}
	key := r.Header.Get("Sec-Websocket-Key")
	if key == "" {
		return nil, nil, fmt.Errorf("missing Sec-Websocket-Key")
	}

	// noVNC requests the "binary" subprotocol; echo it back so the client accepts the upgrade.
	subproto := ""
	for _, p := range strings.Split(r.Header.Get("Sec-Websocket-Protocol"), ",") {
		if strings.TrimSpace(p) == "binary" {
			subproto = "binary"
			break
		}
	}

	h := sha1.New()
	h.Write([]byte(key + wsGUID))
	accept := base64.StdEncoding.EncodeToString(h.Sum(nil))

	hj, ok := w.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("hijacking not supported")
	}
	conn, rw, err := hj.Hijack()
	if err != nil {
		return nil, nil, err
	}

	var sb strings.Builder
	sb.WriteString("HTTP/1.1 101 Switching Protocols\r\n")
	sb.WriteString("Upgrade: websocket\r\n")
	sb.WriteString("Connection: Upgrade\r\n")
	sb.WriteString("Sec-WebSocket-Accept: " + accept + "\r\n")
	if subproto != "" {
		sb.WriteString("Sec-WebSocket-Protocol: " + subproto + "\r\n")
	}
	sb.WriteString("\r\n")

	if _, err := rw.WriteString(sb.String()); err != nil {
		conn.Close()
		return nil, nil, err
	}
	if err := rw.Flush(); err != nil {
		conn.Close()
		return nil, nil, err
	}
	return conn, rw, nil
}

// proxyWSToVNC splices the WebSocket and VNC TCP connection bidirectionally.
// Browser frames are masked binary; VNC bytes are forwarded as unmasked binary frames.
func proxyWSToVNC(wsConn net.Conn, wsRW *bufio.ReadWriter, vncAddr string) {
	defer wsConn.Close()

	vncConn, err := net.Dial("tcp", vncAddr)
	if err != nil {
		payload := make([]byte, 2+len("VNC dial failed"))
		binary.BigEndian.PutUint16(payload[:2], 1011)
		copy(payload[2:], "VNC dial failed")
		_ = writeWSFrame(wsConn, 0x8, payload)
		return
	}
	defer vncConn.Close()

	done := make(chan struct{}, 2)

	// Browser → VNC
	go func() {
		defer func() { done <- struct{}{} }()
		for {
			payload, opcode, err := readWSFrame(wsRW.Reader)
			if err != nil {
				return
			}
			switch opcode {
			case 0x8:
				return
			case 0x9, 0xA:
				continue
			}
			if _, err := vncConn.Write(payload); err != nil {
				return
			}
		}
	}()

	// VNC → Browser
	go func() {
		defer func() { done <- struct{}{} }()
		buf := make([]byte, 32*1024)
		for {
			n, rerr := vncConn.Read(buf)
			if n > 0 {
				if werr := writeWSFrame(wsConn, 0x2, buf[:n]); werr != nil {
					return
				}
			}
			if rerr != nil {
				return
			}
		}
	}()

	<-done
}

// readWSFrame reads one WebSocket frame and returns the (unmasked) payload and opcode.
func readWSFrame(r *bufio.Reader) ([]byte, byte, error) {
	b0, err := r.ReadByte()
	if err != nil {
		return nil, 0, err
	}
	opcode := b0 & 0x0f

	b1, err := r.ReadByte()
	if err != nil {
		return nil, 0, err
	}
	masked := (b1 & 0x80) != 0
	payloadLen := int64(b1 & 0x7f)

	switch payloadLen {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return nil, 0, err
		}
		payloadLen = int64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return nil, 0, err
		}
		payloadLen = int64(binary.BigEndian.Uint64(ext[:]))
	}

	var maskKey [4]byte
	if masked {
		if _, err := io.ReadFull(r, maskKey[:]); err != nil {
			return nil, 0, err
		}
	}

	payload := make([]byte, payloadLen)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, 0, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= maskKey[i%4]
		}
	}
	return payload, opcode, nil
}

// writeWSFrame writes one unmasked WebSocket frame (server→client direction).
func writeWSFrame(w io.Writer, opcode byte, payload []byte) error {
	b0 := byte(0x80) | (opcode & 0x0f) // FIN=1
	plen := len(payload)
	var header []byte
	switch {
	case plen < 126:
		header = []byte{b0, byte(plen)}
	case plen <= 0xFFFF:
		header = make([]byte, 4)
		header[0] = b0
		header[1] = 126
		binary.BigEndian.PutUint16(header[2:], uint16(plen))
	default:
		header = make([]byte, 10)
		header[0] = b0
		header[1] = 127
		binary.BigEndian.PutUint64(header[2:], uint64(plen))
	}
	if _, err := w.Write(header); err != nil {
		return err
	}
	if plen > 0 {
		if _, err := w.Write(payload); err != nil {
			return err
		}
	}
	return nil
}

// consoleHTML is the noVNC console page served per-VM. noVNC is loaded from jsDelivr CDN.
// The page connects WebSocket to /ws/{vmname} on the same origin (the agent port)
// and invokes POST /command for lifecycle actions (existing commander endpoint).
const consoleHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Console — {{.VMName}} · KVM Center</title>
<style>
/* KVM Center console — matches the main web UI's productized theme
   (see docs/STYLE_GUIDE.md). Single-theme dark: a full-screen remote
   console is one of the few surfaces where light-mode makes no sense. */
*, *::before, *::after { box-sizing: border-box; margin: 0; padding: 0; }
html, body { height: 100%; overflow: hidden; }
body {
  font-family: -apple-system, BlinkMacSystemFont, 'Inter', 'Segoe UI', Roboto, sans-serif;
  background: #0B0E13;
  color: #C9D1D9;
  display: flex;
  flex-direction: column;
  font-size: 13px;
  -webkit-font-smoothing: antialiased;
}
::selection { background: #E3571E44; color: #F0F6FC; }

#toolbar {
  height: 48px;
  background: #0D1117;
  border-bottom: 1px solid #30363D;
  display: flex;
  align-items: center;
  padding: 0 16px;
  gap: 14px;
  flex-shrink: 0;
}
.brand {
  display: flex; align-items: center; gap: 8px;
  font-size: 14px; font-weight: 700; letter-spacing: -0.01em;
}
.brand svg { flex-shrink: 0; filter: drop-shadow(0 0 6px rgba(227,87,30,0.25)); }
.brand .b-primary { color: #F0F6FC; }
.brand .b-accent  { color: #E3571E; }
.sep { color: #30363D; font-size: 14px; }
#vm-name {
  font-weight: 600;
  color: #F0F6FC;
  font-size: 13px;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  max-width: 340px;
}
#vm-state {
  display: inline-flex; align-items: center; gap: 6px;
  font-size: 10px;
  padding: 3px 10px;
  border-radius: 10px;
  background: #21262D;
  color: #8B949E;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.5px;
}
#vm-state::before {
  content: ''; width: 6px; height: 6px; border-radius: 50%; background: currentColor;
}
#vm-state.running { background: #1E3D23; color: #3FB950; }
#vm-state.running::before { box-shadow: 0 0 5px #3FB95066; }
#vm-state.stopped { background: #21262D; color: #8B949E; }
#vm-state.error   { background: #3D1E1E; color: #F85149; }
.spacer { flex: 1; }

.btn {
  background: #161B22;
  border: 1px solid #30363D;
  color: #C9D1D9;
  padding: 6px 13px;
  border-radius: 4px;
  cursor: pointer;
  font-size: 12px;
  font-weight: 500;
  font-family: inherit;
  display: inline-flex;
  align-items: center;
  gap: 6px;
  transition: background 0.15s, border-color 0.15s, color 0.15s;
  white-space: nowrap;
}
.btn:hover:not(:disabled) {
  background: #1C2128;
  border-color: #484F58;
  color: #F0F6FC;
}
.btn:disabled { opacity: 0.35; cursor: not-allowed; }
.btn.primary {
  background: linear-gradient(180deg, #F26419 0%, #E3571E 100%);
  border-color: #C04512; color: #fff;
}
.btn.primary:hover:not(:disabled) {
  background: linear-gradient(180deg, #FF7B3C 0%, #F26419 100%);
}
.btn.warn { background: #3d2a00; border-color: #7a5200; color: #ffd080; }
.btn.warn:hover:not(:disabled) { background: #7a5200; color: #fff; }
.btn.danger { background: #7A1C1C; border-color: #A02929; color: #FFB3B3; }
.btn.danger:hover:not(:disabled) { background: #A02929; color: #fff; }

.spinner {
  display: none;
  width: 11px; height: 11px;
  border: 2px solid #ffffff30;
  border-top-color: #fff;
  border-radius: 50%;
  animation: spin 0.6s linear infinite;
}
.btn.loading .spinner { display: inline-block; }
@keyframes spin { to { transform: rotate(360deg); } }

#console-wrap {
  flex: 1;
  position: relative;
  background: #000;
  overflow: hidden;
}
#screen { width: 100%; height: 100%; }
#screen canvas { display: block; }

#overlay {
  display: none;
  position: absolute;
  inset: 0;
  background: rgba(11, 14, 19, 0.94);
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 14px;
  text-align: center;
  padding: 32px;
  z-index: 100;
}
#overlay.visible { display: flex; }
#overlay .icon { font-size: 44px; color: #E3571E; }
#overlay .title { font-size: 18px; font-weight: 600; color: #F0F6FC; letter-spacing: -0.01em; }
#overlay .msg { font-size: 13px; color: #8B949E; max-width: 460px; line-height: 1.6; }

#banner {
  display: none;
  position: fixed;
  top: 60px; left: 50%;
  transform: translateX(-50%);
  padding: 7px 18px;
  border-radius: 4px;
  font-size: 12px;
  z-index: 200;
  box-shadow: 0 4px 16px rgba(0,0,0,0.6);
  white-space: nowrap;
  border-left: 3px solid #E3571E;
  background: #161B22;
  color: #C9D1D9;
}
#banner.warn { border-color: #ff9800; color: #ffd080; background: #2a1d0a; }

#toast-container {
  position: fixed;
  bottom: 16px; right: 16px;
  display: flex;
  flex-direction: column;
  gap: 8px;
  z-index: 300;
}
.toast {
  padding: 10px 16px;
  border-radius: 4px;
  font-size: 12px;
  min-width: 220px;
  border-left: 4px solid;
  animation: slideIn 0.2s ease;
  background: #161B22;
  color: #C9D1D9;
}
.toast.success { background: #1E3D23; border-color: #3FB950; color: #7EE787; }
.toast.error   { background: #3D1E1E; border-color: #F85149; color: #FFB3B3; }
.toast.info    { background: #161B22; border-color: #E3571E; color: #FF8A3C; }
@keyframes slideIn { from { transform: translateX(120%); } to { transform: translateX(0); } }
</style>
</head>
<body>

<div id="toolbar">
  <div class="brand">
    <svg width="22" height="22" viewBox="0 0 32 32" fill="none" aria-label="KVM Center">
      <defs>
        <linearGradient id="cons-accent" x1="0" y1="0" x2="1" y2="1">
          <stop offset="0%" stop-color="#FF8A3C"/>
          <stop offset="100%" stop-color="#E3571E"/>
        </linearGradient>
      </defs>
      <path d="M16 3 L27 10 L27 22 L16 29 L5 22 L5 10 Z"
            fill="none" stroke="url(#cons-accent)" stroke-width="2" stroke-linejoin="round"/>
      <rect x="10" y="11" width="12" height="3" rx="0.5" fill="url(#cons-accent)"/>
      <rect x="10" y="15" width="9"  height="3" rx="0.5" fill="#E3571E" opacity="0.7"/>
      <rect x="10" y="19" width="6"  height="3" rx="0.5" fill="#E3571E" opacity="0.45"/>
    </svg>
    <span><span class="b-primary">kvm</span><span class="b-accent">center</span></span>
  </div>
  <span class="sep">|</span>
  <span id="vm-name">{{.VMName}}</span>
  <span id="vm-state">connecting…</span>

  <div class="spacer"></div>

  <button class="btn primary" id="btn-START"     onclick="doAction('START')"><span class="spinner"></span>▶ Start</button>
  <button class="btn warn"    id="btn-STOP"      onclick="doAction('STOP')"><span class="spinner"></span>■ Shutdown</button>
  <button class="btn"         id="btn-RESTART"   onclick="doAction('RESTART')"><span class="spinner"></span>↺ Restart</button>
  <button class="btn danger"  id="btn-FORCESTOP" onclick="doAction('FORCESTOP')"><span class="spinner"></span>⚡ Force Off</button>
</div>

<div id="console-wrap">
  <div id="screen"></div>
  <div id="overlay">
    <div class="icon">⚠</div>
    <div class="title" id="overlay-title">Console Unavailable</div>
    <div class="msg" id="overlay-msg">Unable to connect to VM console.</div>
    <button class="btn primary" onclick="retryConnect()">Retry</button>
  </div>
</div>

<div id="banner"></div>
<div id="toast-container"></div>

<script type="module">
let RFB;
try {
  const mod = await import('https://cdn.jsdelivr.net/npm/@novnc/novnc@1.4.0/core/rfb.js');
  RFB = mod.default;
} catch (err) {
  showOverlay('Console library failed to load',
    'Could not load noVNC from CDN (' + err.message + '). Check browser network access to cdn.jsdelivr.net.');
  throw err;
}

const VMNAME = {{.VMNameJS}};
// Forward the server-minted auth material from the /console/ URL onto
// the WS URL so the agent re-verifies the token at upgrade time.
const AUTH_QS = 'token={{.Token}}&exp={{.Exp}}&nonce={{.Nonce}}';
let rfb = null;
let attempts = 0;
const MAX_ATTEMPTS = 5;

function wsURL() {
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
  return proto + '//' + location.host + '/ws/' + encodeURIComponent(VMNAME) + '?' + AUTH_QS;
}

function connect() {
  hideOverlay();
  try {
    rfb = new RFB(document.getElementById('screen'), wsURL(), {
      credentials: { password: '' },
    });
  } catch (err) {
    showOverlay('Cannot start console', err.message);
    return;
  }
  rfb.scaleViewport = true;
  rfb.resizeSession = false;

  rfb.addEventListener('connect', () => {
    attempts = 0;
    hideBanner();
    setState('running');
  });

  rfb.addEventListener('disconnect', (ev) => {
    const clean = ev.detail && ev.detail.clean;
    if (clean) {
      showOverlay('Console Disconnected', 'The VM session ended.');
      setState('stopped');
      return;
    }
    if (attempts < MAX_ATTEMPTS) {
      attempts++;
      const delay = Math.min(1000 * Math.pow(2, attempts - 1), 15000);
      showBanner('Console disconnected — reconnecting (' + attempts + '/' + MAX_ATTEMPTS + ')…');
      setTimeout(connect, delay);
    } else {
      showOverlay('Console Disconnected',
        'Could not reconnect after ' + MAX_ATTEMPTS + ' attempts. The VM may be powered off or VNC unreachable.');
      setState('error');
    }
  });

  rfb.addEventListener('credentialsrequired', () => {
    const pw = window.prompt('VNC password:');
    if (pw !== null) rfb.sendCredentials({ password: pw });
  });

  rfb.addEventListener('securityfailure', (ev) => {
    showOverlay('VNC Authentication Failed', (ev.detail && ev.detail.reason) || 'Security negotiation failed.');
  });
}

function retryConnect() {
  attempts = 0;
  connect();
}

function showOverlay(title, msg) {
  document.getElementById('overlay-title').textContent = title;
  document.getElementById('overlay-msg').textContent = msg;
  document.getElementById('overlay').classList.add('visible');
}
function hideOverlay() {
  document.getElementById('overlay').classList.remove('visible');
}
function showBanner(msg, cls) {
  const b = document.getElementById('banner');
  b.textContent = msg;
  b.className = cls || '';
  b.style.display = 'block';
}
function hideBanner() {
  document.getElementById('banner').style.display = 'none';
}
function setState(s) {
  const el = document.getElementById('vm-state');
  el.textContent = s || '—';
  el.className = '';
  if (s === 'running') el.classList.add('running');
  else if (s === 'stopped') el.classList.add('stopped');
  else if (s === 'error') el.classList.add('error');
}
function toast(msg, type) {
  const c = document.getElementById('toast-container');
  const el = document.createElement('div');
  el.className = 'toast ' + (type || 'info');
  el.textContent = msg;
  c.appendChild(el);
  setTimeout(() => el.remove(), 4500);
}

const btns = {};
['START','STOP','RESTART','FORCESTOP'].forEach(a => {
  btns[a] = document.getElementById('btn-' + a);
});
function setButtonsDisabled(dis) {
  Object.values(btns).forEach(b => { b.disabled = dis; });
}

window.doAction = async function(action) {
  const btn = btns[action];
  if (!btn) return;
  btn.classList.add('loading');
  setButtonsDisabled(true);
  try {
    const resp = await fetch('/command', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ action: action, vm_name: VMNAME, params: {} }),
    });
    const data = await resp.json();
    if (data.success) {
      toast(action + ' sent: ' + (data.message || 'OK'), 'success');
      if (action === 'STOP' || action === 'FORCESTOP') {
        setTimeout(() => { if (rfb) rfb.disconnect(); }, 1500);
      } else if (action === 'START') {
        setTimeout(retryConnect, 2500);
      } else if (action === 'RESTART') {
        setTimeout(retryConnect, 3500);
      }
    } else {
      toast(action + ' failed: ' + (data.message || resp.statusText), 'error');
    }
  } catch (err) {
    toast('Network error: ' + err.message, 'error');
  } finally {
    btn.classList.remove('loading');
    setButtonsDisabled(false);
  }
};

window.retryConnect = retryConnect;
connect();
</script>
</body>
</html>
`
