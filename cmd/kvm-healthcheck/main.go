// kvm-healthcheck performs one bounded, loopback-only container liveness probe.
// TLS runs in-process: no shell or TLS helper subprocess is created.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"
)

func probe(ctx context.Context, endpoint string, timeout time.Duration) error {
	u, err := url.Parse(endpoint)
	if err != nil {
		return errors.New("invalid health endpoint")
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "https" || ip == nil || !ip.IsLoopback() || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "/health" {
		return errors.New("health endpoint must be an HTTPS literal-loopback /health URL without credentials, query or fragment")
	}
	if timeout <= 0 || timeout > 4*time.Second {
		return errors.New("timeout must be greater than zero and at most 4s")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	transport := &http.Transport{
		// This is local liveness only, matching the former self-signed probe.
		// The literal loopback target, disabled proxies and redirects keep this
		// exception out of application traffic and remote trust decisions.
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true},
		DisableKeepAlives: true,
		Proxy:             nil,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health returned HTTP %d", resp.StatusCode)
	}
	// Bound both response size and time; do not leave a streaming response open.
	n, err := io.Copy(io.Discard, io.LimitReader(resp.Body, 4097))
	if err != nil {
		return err
	}
	if n > 4096 {
		return errors.New("health response exceeds 4096 bytes")
	}
	return nil
}

func main() {
	endpoint := flag.String("url", "https://127.0.0.1:8190/health", "literal-loopback HTTPS health URL")
	timeout := flag.Duration("timeout", 3*time.Second, "overall timeout (maximum 4s)")
	flag.Parse()
	if err := probe(context.Background(), *endpoint, *timeout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
