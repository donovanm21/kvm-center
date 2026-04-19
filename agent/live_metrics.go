package main

// live_metrics.go (v5.28.0): streams per-running-VM CPU/mem samples
// to the control plane over gRPC every few seconds. Complements the
// 15 s HTTP telemetry tick — the HTTP path remains authoritative for
// inventory/state changes; gRPC just keeps the numeric gauges fresh
// without hammering telemetry cadence (which also updates hosts,
// storage, hardware, etc).
//
// Connection policy: one persistent client stream, re-opened on
// failure. If the server is unreachable we back off and keep the
// 15 s telemetry path covering state.

import (
	"context"
	"crypto/tls"
	"log"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	agentpb "kvm-center/gen/agentpb"
	"kvm-center/internal/models"
)

const liveMetricsInterval = 3 * time.Second

// startLiveMetricsStreamer opens a gRPC client stream to serverURL and
// pushes per-running-VM CPU/mem samples every liveMetricsInterval.
// Runs until ctx is cancelled. Non-fatal stream errors trigger a
// reconnect with backoff.
func startLiveMetricsStreamer(ctx context.Context, serverURL string) {
	grpcAddr := deriveGRPCAddr(serverURL)
	if grpcAddr == "" {
		log.Printf("live-metrics: no grpc address derivable from %q — streamer disabled", serverURL)
		return
	}
	hostID := hostname()
	backoff := 2 * time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		if err := runOneLiveMetricsStream(ctx, grpcAddr, hostID); err != nil {
			log.Printf("live-metrics: stream ended (%v) — reconnecting in %s", err, backoff)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = 2 * time.Second
	}
}

func runOneLiveMetricsStream(ctx context.Context, grpcAddr, hostID string) error {
	// Server multiplexes gRPC on its TLS port (:8190) — same self-signed
	// cert that makes the HTTP client run with InsecureSkipVerify. Match
	// that trust model here; operator upgrades to a trusted cert via
	// Settings → TLS when they're ready.
	creds := credentials.NewTLS(&tls.Config{InsecureSkipVerify: true}) //nolint:gosec // homelab trust model, see agent/main.go
	conn, err := grpc.NewClient(grpcAddr, grpc.WithTransportCredentials(creds))
	if err != nil {
		return err
	}
	defer conn.Close()
	client := agentpb.NewAgentServiceClient(conn)
	stream, err := client.StreamVMMetrics(ctx)
	if err != nil {
		return err
	}
	ticker := time.NewTicker(liveMetricsInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			_, _ = stream.CloseAndRecv()
			return nil
		case <-ticker.C:
			if err := sendLiveMetricsFrame(stream, hostID); err != nil {
				return err
			}
		}
	}
}

func sendLiveMetricsFrame(stream agentpb.AgentService_StreamVMMetricsClient, hostID string) error {
	vms := listVMs()
	now := time.Now().Unix()
	for _, vm := range vms {
		if vm.State != models.VMRunning || vm.UUID == "" {
			continue
		}
		sample := &agentpb.VMMetricSample{
			VmUuid:     vm.UUID,
			HostId:     hostID,
			CpuUsage:   vm.CPUUsage,
			MemUsageMb: int64(vm.MemUsageMB),
			ReportedAt: now,
		}
		if err := stream.Send(sample); err != nil {
			return err
		}
	}
	return nil
}

// deriveGRPCAddr picks the host:port the agent should dial for gRPC.
// Explicit override via KVM_CENTER_GRPC_ADDR wins. Otherwise: the
// server multiplexes gRPC on its TLS port (:8190) regardless of
// whether the agent's HTTP telemetry goes to :8090 (plain) or :8190
// (TLS). We strip scheme+path from serverURL and rewrite the port
// to the multiplex port.
func deriveGRPCAddr(serverURL string) string {
	if v := os.Getenv("KVM_CENTER_GRPC_ADDR"); v != "" {
		return v
	}
	if serverURL == "" {
		return ""
	}
	u, err := url.Parse(serverURL)
	host := ""
	if err == nil && u.Host != "" {
		host = u.Host
	} else {
		s := strings.TrimPrefix(serverURL, "https://")
		s = strings.TrimPrefix(s, "http://")
		host = strings.TrimRight(s, "/")
	}
	// Strip whatever port the HTTP telemetry URL carries and pin to the
	// multiplex port. host:port → host; bare host stays bare.
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return host + ":8190"
}
