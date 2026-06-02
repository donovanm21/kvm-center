package main

// gRPC migration-progress streaming flow (v5.27).

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	agentpb "kvm-center/gen/agentpb"
	"kvm-center/internal/models"
)

// authedGRPCContext attaches the v5.37.1 stream-interceptor's required
// `x-kvmc-agent-host` header. Tests using a fresh harness need to
// seed the host first via ApplyTelemetry — the interceptor rejects
// unknown host_ids.
func authedGRPCContext(parent context.Context, hostID string) context.Context {
	return metadata.AppendToOutgoingContext(parent, "x-kvmc-agent-host", hostID)
}

// disableMTLSDefault opts out of v5.47's default-on
// GRPCRequireClientCert for fresh stores so tests that exercise
// the metadata-header path (no TLS, no client cert) keep working.
func disableMTLSDefault(s *Store) {
	f := s.GetFeatures()
	f.GRPCRequireClientCert = false
	s.SetFeatures(f)
}

// TestIntegration_GRPC_MigrationProgress: agent streams a burst of
// progress frames; server updates MigrationRecord in real time;
// a "complete" terminal frame finalises the record.
func TestIntegration_GRPC_MigrationProgress(t *testing.T) {
	h := newIntegrationHarness(t)
	// v5.47.0 flipped GRPCRequireClientCert default ON for fresh
	// installs. These tests exercise the metadata-header fallback
	// path (no TLS, no client cert) so we explicitly opt the
	// feature back off.
	disableMTLSDefault(h.Store)
	// Seed the host the gRPC interceptor will validate against
	// (Tier 2 #9). ApplyTelemetry is enough to make GetHost return
	// non-nil.
	h.Store.ApplyTelemetry(&models.TelemetryPayload{HostID: "hv01"})
	// Seed the record the agent is about to push frames for.
	h.Store.AddMigration(&models.MigrationRecord{
		ID:         "mig-grpc-1",
		VMName:     "test-vm",
		SourceHost: "hv01",
		DestHost:   "hv02",
		Status:     "running",
		StartedAt:  time.Now().Unix(),
	})

	// Start the gRPC server on an ephemeral port, with no TLS (the
	// harness lives entirely in-process).
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	grpcAddr := lis.Addr().String()
	_ = lis.Close()

	stop, err := startGRPCServer(grpcAddr, h.Store, nil)
	if err != nil {
		t.Fatalf("start grpc: %v", err)
	}
	defer stop()

	// Give the server a heartbeat to bind.
	time.Sleep(50 * time.Millisecond)

	// Agent-style client.
	conn, err := grpc.NewClient(grpcAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	client := agentpb.NewAgentServiceClient(conn)

	ctx, cancel := context.WithTimeout(authedGRPCContext(context.Background(), "hv01"), 5*time.Second)
	defer cancel()
	stream, err := client.StreamMigrationProgress(ctx)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}

	// Push progressive frames.
	frames := []*agentpb.MigrationProgress{
		{MigrationId: "mig-grpc-1", Phase: "prepare", ProgressPct: 5, Detail: "Verifying destination pool"},
		{MigrationId: "mig-grpc-1", Phase: "transfer", ProgressPct: 50, Detail: "vol-upload 500 MB"},
		{MigrationId: "mig-grpc-1", Phase: "transfer", ProgressPct: 90, Detail: "vol-upload 900 MB"},
		{MigrationId: "mig-grpc-1", Phase: "complete", ProgressPct: 100, Detail: "migration finished"},
	}
	for _, f := range frames {
		if err := stream.Send(f); err != nil {
			t.Fatalf("send frame: %v", err)
		}
	}
	ack, err := stream.CloseAndRecv()
	if err != nil {
		t.Fatalf("close+recv: %v", err)
	}
	if ack.GetFramesAccepted() != int32(len(frames)) {
		t.Fatalf("frames_accepted: got %d want %d", ack.GetFramesAccepted(), len(frames))
	}

	// Server should have transitioned the record to complete.
	rec := h.Store.GetMigrations()[0]
	if rec.Status != "complete" {
		t.Fatalf("status: got %q, want complete", rec.Status)
	}
}

// TestIntegration_GRPC_MigrationProgress_UnknownID: a frame for a
// migration the server has never heard of is logged + dropped,
// not returned as an error (the agent's local state is ahead; the
// agent's sync HTTP response will still carry the final outcome).
func TestIntegration_GRPC_MigrationProgress_UnknownID(t *testing.T) {
	h := newIntegrationHarness(t)
	disableMTLSDefault(h.Store)
	h.Store.ApplyTelemetry(&models.TelemetryPayload{HostID: "hv01"})

	lis, _ := net.Listen("tcp", "127.0.0.1:0")
	grpcAddr := lis.Addr().String()
	_ = lis.Close()
	stop, err := startGRPCServer(grpcAddr, h.Store, nil)
	if err != nil {
		t.Fatalf("start grpc: %v", err)
	}
	defer stop()
	time.Sleep(50 * time.Millisecond)

	conn, _ := grpc.NewClient(grpcAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	defer conn.Close()
	client := agentpb.NewAgentServiceClient(conn)
	ctx, cancel := context.WithTimeout(authedGRPCContext(context.Background(), "hv01"), 5*time.Second)
	defer cancel()
	stream, _ := client.StreamMigrationProgress(ctx)
	_ = stream.Send(&agentpb.MigrationProgress{
		MigrationId: "non-existent",
		Phase:       "transfer",
		ProgressPct: 42,
	})
	ack, err := stream.CloseAndRecv()
	if err != nil {
		t.Fatalf("close+recv: %v", err)
	}
	if ack.GetFramesAccepted() != 1 {
		t.Fatalf("unknown-id frame should still be counted as accepted-but-dropped: got %d", ack.GetFramesAccepted())
	}
}
