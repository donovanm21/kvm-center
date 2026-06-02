package main

import (
	"bufio"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Server-proxied VM console (v5.0.4). Prior to this release the SPA
// opened http://<hvnode>:8081/console/<vm>?token=... directly — the
// HMAC token prevented random LAN access, but a shared URL let anyone
// who received it in chat/Slack/email open the console within the
// 5-minute expiry window.
//
// The proxy moves both the HTML page and the VNC WebSocket behind the
// server's HTTPS origin, auth-gated on every hit. The operator's
// browser session cookie is the required out-of-band credential — no
// cookie, no console. Sharing the URL doesn't share the cookie.
//
// Wire model:
//   Browser  ── HTTPS: GET  /api/console/<host>/<vm>     ──► Server (auth)
//   Server   ── HTTP:  noVNC HTML served inline
//   Browser  ── WSS:   /api/console-ws/<host>/<vm>       ──► Server (auth)
//   Server   ── WS:    ws://<agent>:8081/ws/<vm>?token=… ──► Agent (HMAC)
//   Server   splices bytes between the two websockets.

const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// parseConsolePath pulls the host id and VM name out of /api/console[-ws]/<host>/<vm>.
// Returns empty strings if the path is malformed — handlers then 400.
func parseConsolePath(reqPath, prefix string) (string, string) {
	rest := strings.TrimPrefix(reqPath, prefix)
	rest = strings.TrimPrefix(rest, "/")
	i := strings.Index(rest, "/")
	if i <= 0 || i == len(rest)-1 {
		return "", ""
	}
	host, err := url.PathUnescape(rest[:i])
	if err != nil {
		return "", ""
	}
	vm, err := url.PathUnescape(rest[i+1:])
	if err != nil {
		return "", ""
	}
	return host, vm
}

// handleConsolePage serves the noVNC HTML with its WebSocket URL
// pointed back at the server's /api/console-ws/... proxy, so every
// packet rides the HTTPS session cookie. Auth is enforced by the
// vm.console requirePerm wrapper in main.go — this handler only runs
// for users carrying the permission.
func handleConsolePage(_store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// v5.18.7: RBAC alone isn't enough — if the operator flipped
		// disable_console they still don't want a user with vm.console
		// to deep-link to this page.
		if featureBlocked(w, _store, "console", "VM console access") {
			return
		}
		hostID, vmName := parseConsolePath(r.URL.Path, "/api/console")
		if hostID == "" || vmName == "" {
			http.Error(w, "path must be /api/console/<host>/<vm>", http.StatusBadRequest)
			return
		}
		// Audit on the page open — captures intent. The WebSocket
		// upgrade that follows isn't audited separately because a
		// single Launch Console click produces both, and double-
		// counting clutters the operator's view of "who opened
		// what console".
		audit(_store, r, AuditConsoleOpen, "vm/"+vmName, map[string]string{"host_id": hostID}, true, "")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = serverConsoleTmpl.Execute(w, map[string]any{
			"VMName": vmName,
			"HostID": hostID,
		})
	}
}

// handleConsoleWSProxy is the bidirectional WS splice. Handshake with
// the browser first; only then do we open a connection to the agent
// (cheapest failure mode — bad auth refuses the browser before any
// backend work).
func handleConsoleWSProxy(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// v5.18.7: gate the WebSocket upgrade behind the same flag as
		// the HTML shell so a savvy client can't bypass the UI hiding.
		if featureBlocked(w, store, "console", "VM console access") {
			return
		}
		hostID, vmName := parseConsolePath(r.URL.Path, "/api/console-ws")
		if hostID == "" || vmName == "" {
			http.Error(w, "path must be /api/console-ws/<host>/<vm>", http.StatusBadRequest)
			return
		}
		h := store.GetHost(hostID)
		if h == nil || h.IP == "" {
			http.Error(w, "host not found", http.StatusNotFound)
			return
		}
		port := h.AgentPort
		if port == 0 {
			port = 8081
		}
		token, exp, nonce := store.SignConsoleToken(hostID, vmName)

		// Step 1: upgrade the browser connection. Honour the "binary"
		// subprotocol noVNC asks for; the agent will too when we dial.
		browserConn, browserRW, err := serverWSUpgrade(w, r)
		if err != nil {
			http.Error(w, "ws upgrade failed: "+err.Error(), http.StatusBadRequest)
			return
		}
		defer browserConn.Close()

		// Step 2: dial the agent's /ws/<vm> endpoint as a WS client,
		// carrying the token that proves this call came from the server.
		agentAddr := net.JoinHostPort(h.IP, fmt.Sprintf("%d", port))
		agentPath := fmt.Sprintf("/ws/%s?token=%s&exp=%d&nonce=%s",
			url.PathEscape(vmName), url.QueryEscape(token), exp, url.QueryEscape(nonce))
		agentConn, agentRW, err := dialWSClient(agentAddr, agentPath, "binary")
		if err != nil {
			log.Printf("console-proxy: agent dial failed vm=%s: %v", vmName, err)
			// Politely close the browser side with a close frame.
			closeWithStatus(browserConn, 1011, "agent unreachable: "+err.Error())
			return
		}
		defer agentConn.Close()

		log.Printf("console-proxy: session start vm=%s host=%s", vmName, hostID)
		spliceWS(browserConn, browserRW, agentConn, agentRW)
		log.Printf("console-proxy: session end   vm=%s host=%s", vmName, hostID)
	}
}

// serverWSUpgrade accepts an inbound WebSocket from the browser. It's
// structurally identical to the agent's wsUpgrade — duplicated here
// so the server has no runtime dep on the agent package and can be
// cross-compiled independently.
func serverWSUpgrade(w http.ResponseWriter, r *http.Request) (net.Conn, *bufio.ReadWriter, error) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return nil, nil, fmt.Errorf("not a websocket upgrade")
	}
	key := r.Header.Get("Sec-Websocket-Key")
	if key == "" {
		return nil, nil, fmt.Errorf("missing Sec-Websocket-Key")
	}

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
	sb.WriteString("Upgrade: websocket\r\nConnection: Upgrade\r\n")
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

// dialWSClient opens a TCP connection to the agent and performs the
// WebSocket client handshake. No TLS — agent is plain-HTTP on the LAN.
// Returns the raw connection + a buffered reader positioned right
// after the 101 response so the first frame can be read without
// re-wrapping.
func dialWSClient(addr, path, subproto string) (net.Conn, *bufio.ReadWriter, error) {
	d := &net.Dialer{Timeout: 5 * time.Second}
	conn, err := d.Dial("tcp", addr)
	if err != nil {
		return nil, nil, err
	}
	// 16 random bytes, base64-encoded per RFC 6455.
	kb := make([]byte, 16)
	if _, err := rand.Read(kb); err != nil {
		conn.Close()
		return nil, nil, err
	}
	key := base64.StdEncoding.EncodeToString(kb)

	var req strings.Builder
	req.WriteString("GET " + path + " HTTP/1.1\r\n")
	req.WriteString("Host: " + addr + "\r\n")
	req.WriteString("Upgrade: websocket\r\nConnection: Upgrade\r\n")
	req.WriteString("Sec-WebSocket-Version: 13\r\n")
	req.WriteString("Sec-WebSocket-Key: " + key + "\r\n")
	if subproto != "" {
		req.WriteString("Sec-WebSocket-Protocol: " + subproto + "\r\n")
	}
	req.WriteString("\r\n")
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.WriteString(conn, req.String()); err != nil {
		conn.Close()
		return nil, nil, err
	}

	r := bufio.NewReader(conn)
	statusLine, err := r.ReadString('\n')
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	if !strings.Contains(statusLine, " 101 ") {
		// Drain the rest of the headers so the error message is useful.
		body, _ := io.ReadAll(r)
		conn.Close()
		return nil, nil, fmt.Errorf("agent refused upgrade: %s body=%s",
			strings.TrimSpace(statusLine), strings.TrimSpace(string(body)))
	}
	// Consume headers up to the blank line.
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			conn.Close()
			return nil, nil, err
		}
		if line == "\r\n" || line == "\n" {
			break
		}
	}
	// Clear deadline — session is live, subject to its own timeouts.
	_ = conn.SetDeadline(time.Time{})
	return conn, bufio.NewReadWriter(r, bufio.NewWriter(conn)), nil
}

// spliceWS blindly copies bytes between the two connections. Both
// sides have already completed their WebSocket handshakes; any frame
// (including ping/pong/close) from the browser is a valid frame for
// the agent and vice versa. Terminates when either side closes.
func spliceWS(browser net.Conn, browserRW *bufio.ReadWriter, agent net.Conn, agentRW *bufio.ReadWriter) {
	done := make(chan struct{}, 2)
	go func() {
		// Forward what's already buffered from the handshake, then
		// swap to raw io.Copy on the underlying conn for zero-copy.
		_, _ = io.Copy(agent, browserRW.Reader)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(browser, agentRW.Reader)
		done <- struct{}{}
	}()
	<-done
	// One side closed — tear the other down so the goroutine unblocks.
	_ = browser.Close()
	_ = agent.Close()
	<-done
}

// closeWithStatus writes a WebSocket close frame with the given RFC
// 6455 status code + reason and closes the conn. Best-effort only.
func closeWithStatus(conn net.Conn, code uint16, reason string) {
	payload := make([]byte, 2+len(reason))
	binary.BigEndian.PutUint16(payload[:2], code)
	copy(payload[2:], reason)
	_ = writeCloseFrame(conn, payload)
	_ = conn.Close()
}

func writeCloseFrame(conn net.Conn, payload []byte) error {
	frame := make([]byte, 0, 4+len(payload))
	// FIN + close opcode
	frame = append(frame, 0x88)
	if len(payload) < 126 {
		frame = append(frame, byte(len(payload)))
	} else {
		// Close frames are small; clamp to fit the 1-byte length path.
		payload = payload[:125]
		frame = append(frame, byte(len(payload)))
	}
	frame = append(frame, payload...)
	_, err := conn.Write(frame)
	return err
}
