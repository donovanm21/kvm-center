package main

// gRPC agent service (v5.27). Foundation cut of the hybrid-transport
// roadmap item: HTTP REST stays authoritative for telemetry + setup
// + SPA + kvmctl; gRPC is used only where streaming delivers real
// value the 30 s telemetry tick can't match.
//
// Wired in v5.27:
//   - StreamMigrationProgress: agent pushes progress frames during
//     `virsh migrate`; server updates MigrationRecord in real time
//     instead of waiting for the synchronous HTTP response.
//
// Stubbed for later (return Unimplemented):
//   - StreamVMMetrics (live per-VM CPU/mem while detail page open)
//   - OpenConsoleSession (HMAC token exchange + WS URL)
//
// The gRPC listener binds to KVM_CENTER_GRPC_ADDR (default :8095)
// behind the same TLS cert as the HTTPS endpoint. Port is separate
// from HTTP/2 multiplex for simpler ops + easier firewall rules.

import (
	"io"
	"log"
	"net"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"

	agentpb "kvm-center/gen/agentpb"
	"kvm-center/internal/models"
)

// agentGRPCServer implements agentpb.AgentServiceServer. Holds a
// reference to the Store so inbound stream frames can update
// MigrationRecord state in real time.
type agentGRPCServer struct {
	agentpb.UnimplementedAgentServiceServer
	store *Store
}

// StreamMigrationProgress: agent opens one stream per migration and
// pushes frames as `virsh migrate` reports progress. Server persists
// each frame into the MigrationRecord. The stream ends when the
// agent closes it (on completion, failure, or cancellation); server
// replies with a summary Ack.
//
// Auth note: this RPC is currently open (mTLS-only — the agent is
// already dialing with the cluster CA cert in prod). A future patch
// will validate the caller's cert against the issued hypervisor cert
// list so a stolen cert can't impersonate a different host.
func (s *agentGRPCServer) StreamMigrationProgress(stream agentpb.AgentService_StreamMigrationProgressServer) error {
	var frames int32
	var lastErr string
	for {
		frame, err := stream.Recv()
		if err == io.EOF {
			return stream.SendAndClose(&agentpb.Ack{
				FramesAccepted: frames,
				LastError:      lastErr,
			})
		}
		if err != nil {
			return err
		}
		frames++
		if err := s.applyProgressFrame(frame); err != nil {
			lastErr = err.Error()
			log.Printf("grpc: migration progress frame apply failed: %v", err)
		}
	}
}

// applyProgressFrame updates the matching MigrationRecord with the
// phase + progress the agent is reporting. If a record is missing
// (e.g. the server was restarted after the migration started and
// hasn't rehydrated yet), the frame is logged and dropped — the
// agent's subsequent sync HTTP response + the boot reconciler
// cover that recovery path.
func (s *agentGRPCServer) applyProgressFrame(f *agentpb.MigrationProgress) error {
	if f.GetMigrationId() == "" {
		return status.Error(codes.InvalidArgument, "migration_id required")
	}
	// Find the record by ID. MigrationRecord store is small (usually
	// <100 entries), linear scan is fine.
	var rec *models.MigrationRecord
	for _, m := range s.store.GetMigrations() {
		if m != nil && m.ID == f.GetMigrationId() {
			rec = m
			break
		}
	}
	if rec == nil {
		log.Printf("grpc: migration progress frame for unknown id=%q — agent ahead of server state, dropping",
			f.GetMigrationId())
		return nil
	}
	// Terminal phases update the final status; intermediate phases
	// only bump progress/phase fields.
	phase := strings.ToLower(strings.TrimSpace(f.GetPhase()))
	switch phase {
	case "complete":
		finalizeMigration(s.store, rec, "complete", f.GetDetail())
	case "failed":
		detail := f.GetError()
		if detail == "" {
			detail = f.GetDetail()
		}
		finalizeMigration(s.store, rec, "failed", detail)
	default:
		rec.Phase = phase
		if p := f.GetProgressPct(); p > 0 {
			rec.Progress = int(p)
		}
		if d := f.GetDetail(); d != "" {
			rec.Detail = d
		}
		s.store.AddMigration(rec)
	}
	return nil
}

// startGRPCServer spins up the agent-facing gRPC listener. Returns a
// closer for graceful shutdown. TLS creds are optional — when nil,
// the server listens in plaintext (only safe on the management VLAN;
// prod wires the same TLS cert the HTTPS endpoint uses).
func startGRPCServer(addr string, store *Store, tlsCreds credentials.TransportCredentials) (stop func(), err error) {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	var opts []grpc.ServerOption
	if tlsCreds != nil {
		opts = append(opts, grpc.Creds(tlsCreds))
	}
	srv := grpc.NewServer(opts...)
	agentpb.RegisterAgentServiceServer(srv, &agentGRPCServer{store: store})

	go func() {
		log.Printf("grpc agent service listening on %s (tls=%v)", addr, tlsCreds != nil)
		if err := srv.Serve(lis); err != nil {
			log.Printf("grpc: serve exited: %v", err)
		}
	}()

	stop = func() {
		// GracefulStop drains in-flight RPCs with a timeout.
		done := make(chan struct{})
		go func() { srv.GracefulStop(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			srv.Stop()
		}
		_ = lis.Close()
	}
	return stop, nil
}
