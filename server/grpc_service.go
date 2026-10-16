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
	"context"
	"io"
	"log"
	"net"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
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
// Auth note: this RPC runs behind agentAuthStreamInterceptor. When the
// agent presents a cluster-CA-verified client cert, the cert CN (the
// host_id, pinned at enrollment) is validated against the known-host
// list — a stolen cert for host A can't impersonate host B. When no
// client cert is presented, identity falls back to the self-asserted
// `x-kvmc-agent-host` metadata header (validated against the host list
// but not cryptographically bound). Operators can require client certs
// fleet-wide via the GRPCRequireClientCert feature, which rejects
// header-only callers outright.
func (s *agentGRPCServer) StreamMigrationProgress(stream agentpb.AgentService_StreamMigrationProgressServer) error {
	var frames int32
	var lastErr string
	callerHostID, _ := stream.Context().Value(agentHostKey{}).(string)
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
		if err := s.applyProgressFrame(frame, callerHostID); err != nil {
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
func (s *agentGRPCServer) applyProgressFrame(f *agentpb.MigrationProgress, callerHostID string) error {
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
	// Ownership check: the streaming agent must be one of the two
	// hosts party to this migration. Stops a spoofed/compromised
	// peer from poisoning another host's migration record.
	if callerHostID != "" && callerHostID != rec.SourceHost && callerHostID != rec.DestHost {
		log.Printf("grpc: refusing migration progress frame: caller host=%q is not source=%q or dest=%q for migration=%q",
			callerHostID, rec.SourceHost, rec.DestHost, rec.ID)
		return status.Error(codes.PermissionDenied, "caller is not a party to this migration")
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
		// v5.28: fan out via SSE so open browser sessions see the
		// progress bar move without waiting for their next poll.
		if s.store.hub != nil {
			s.store.hub.Publish(EventMigration, map[string]any{
				"id":       rec.ID,
				"phase":    rec.Phase,
				"progress": rec.Progress,
				"status":   rec.Status,
			})
		}
		return nil
	}
	// Terminal finalize path already publishes via AddMigration's SSE hook.
	return nil
}

// StreamVMMetrics (v5.28.0): agent streams per-VM CPU + memory samples
// at a faster cadence than the 15 s telemetry tick. Server updates
// VM.CPUUsage / VM.MemUsageMB and fans out an `inventory` SSE event
// so the SPA's query cache invalidates and shows live numbers.
// Stream is unidirectional client→server; the agent never waits on
// an ack other than the final Ack on Close.
func (s *agentGRPCServer) StreamVMMetrics(stream agentpb.AgentService_StreamVMMetricsServer) error {
	var frames int32
	var lastErr string
	var lastPublish time.Time
	const publishDebounce = 2 * time.Second
	for {
		sample, err := stream.Recv()
		if err == io.EOF {
			return stream.SendAndClose(&agentpb.Ack{FramesAccepted: frames, LastError: lastErr})
		}
		if err != nil {
			return err
		}
		frames++
		if sample.GetVmUuid() == "" {
			continue
		}
		s.store.ApplyVMMetricSample(sample.GetHostId(), sample.GetVmUuid(),
			sample.GetCpuUsage(), sample.GetMemUsageMb())
		// Debounce SSE fanout — one inventory event per ~2 s is
		// plenty for the SPA's live sparklines; flooding hundreds
		// of ['vms'] invalidations per minute would thrash React
		// Query.
		if s.store.hub != nil && time.Since(lastPublish) >= publishDebounce {
			s.store.hub.Publish(EventInventory, map[string]any{"host_id": sample.GetHostId()})
			lastPublish = time.Now()
		}
	}
}

// buildGRPCServer constructs the *grpc.Server with AgentService
// registered. Keeps the registration in one place so both the
// multiplexed-on-8190 path and the optional dedicated-port path
// expose the same handlers.
//
// v5.37.1 (Tier 2 #9): every stream goes through agentAuthInterceptor,
// which requires an `x-kvmc-agent-host` metadata key naming a host
// the server already knows. The full mTLS-client-cert story still
// has to land — this is the cheapest layer that closes the "any TCP
// peer can stream frames" hole.
func buildGRPCServer(store *Store) *grpc.Server {
	srv := grpc.NewServer(
		grpc.StreamInterceptor(agentAuthStreamInterceptor(store)),
		grpc.UnaryInterceptor(agentAuthUnaryInterceptor(store)),
	)
	agentpb.RegisterAgentServiceServer(srv, &agentGRPCServer{store: store})
	return srv
}

// agentAuthUnaryInterceptor mirrors the stream-side interceptor's
// host_id binding for unary RPCs (currently OpenConsoleSession).
// Same precedence: verified client cert > metadata header. The
// GRPCRequireClientCert feature flag also applies here.
func agentAuthUnaryInterceptor(store *Store) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if !store.work.begin() {
			return nil, status.Error(codes.Unavailable, "server draining")
		}
		defer store.work.end()
		if store.shutdown != nil && store.shutdown.draining.Load() {
			return nil, status.Error(codes.Unavailable, "server draining")
		}
		if certHostID, ok := agentHostFromClientCert(ctx); ok {
			if store.GetHost(certHostID) == nil {
				log.Printf("grpc: refusing %s — client cert CN %q is not a known host", info.FullMethod, certHostID)
				return nil, status.Error(codes.PermissionDenied, "unknown host")
			}
			ctx = context.WithValue(ctx, agentHostKey{}, certHostID)
			return handler(ctx, req)
		}
		if store.GetFeatures().GRPCRequireClientCert {
			log.Printf("grpc: refusing %s — GRPCRequireClientCert on but no verified client cert", info.FullMethod)
			return nil, status.Error(codes.Unauthenticated, "client cert required")
		}
		md, ok := metadata.FromIncomingContext(ctx)
		if !ok {
			return nil, status.Error(codes.Unauthenticated, "missing metadata")
		}
		hostIDs := md.Get("x-kvmc-agent-host")
		if len(hostIDs) == 0 || strings.TrimSpace(hostIDs[0]) == "" {
			return nil, status.Error(codes.Unauthenticated, "x-kvmc-agent-host required")
		}
		hostID := strings.TrimSpace(hostIDs[0])
		if store.GetHost(hostID) == nil {
			return nil, status.Error(codes.PermissionDenied, "unknown host")
		}
		ctx = context.WithValue(ctx, agentHostKey{}, hostID)
		return handler(ctx, req)
	}
}

// agentAuthStreamInterceptor rejects gRPC streams that don't identify
// themselves with a known host_id. Looking-glass auth — anyone on the
// management VLAN who can reach :8190 still gets in if they spoof a
// real host_id, but unauthenticated probes and unrelated services no
// longer reach the streaming handlers. Callers wired to use the
// interceptor must include `x-kvmc-agent-host: <id>` on every stream
// open; agents do this from `live_metrics.go` and `migrate.go`.
func agentAuthStreamInterceptor(store *Store) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if !store.work.begin() {
			return status.Error(codes.Unavailable, "server draining")
		}
		defer store.work.end()
		if store.shutdown != nil && store.shutdown.draining.Load() {
			return status.Error(codes.Unavailable, "server draining")
		}
		// v5.44.0: prefer the cluster-CA-verified client cert when
		// the agent presents one. The cert's CN is the host_id,
		// pinned at enrollment time. Cryptographically authenticated
		// (TLS handshake + CA chain check), so it beats the
		// self-asserted metadata header.
		if certHostID, ok := agentHostFromClientCert(ss.Context()); ok {
			if store.GetHost(certHostID) == nil {
				log.Printf("grpc: refusing %s — client cert CN %q is not a known host", info.FullMethod, certHostID)
				return status.Error(codes.PermissionDenied, "unknown host")
			}
			ctx := context.WithValue(ss.Context(), agentHostKey{}, certHostID)
			return handler(srv, &authedServerStream{ServerStream: ss, ctx: ctx})
		}

		// No client cert. v5.46.0: refuse outright when
		// Features.GRPCRequireClientCert is on (operator has
		// confirmed the fleet uniformly has leaf certs). Otherwise
		// fall back to the metadata header from v5.37.1 (Tier 2 #9)
		// so existing agents keep working through the rollout.
		if store.GetFeatures().GRPCRequireClientCert {
			log.Printf("grpc: refusing %s — GRPCRequireClientCert on but no verified client cert", info.FullMethod)
			return status.Error(codes.Unauthenticated, "client cert required")
		}
		md, ok := metadata.FromIncomingContext(ss.Context())
		if !ok {
			log.Printf("grpc: refusing %s — no metadata on stream", info.FullMethod)
			return status.Error(codes.Unauthenticated, "missing metadata")
		}
		hostIDs := md.Get("x-kvmc-agent-host")
		if len(hostIDs) == 0 || strings.TrimSpace(hostIDs[0]) == "" {
			log.Printf("grpc: refusing %s — no x-kvmc-agent-host header", info.FullMethod)
			return status.Error(codes.Unauthenticated, "x-kvmc-agent-host required")
		}
		hostID := strings.TrimSpace(hostIDs[0])
		if store.GetHost(hostID) == nil {
			log.Printf("grpc: refusing %s — unknown host_id=%q", info.FullMethod, hostID)
			return status.Error(codes.PermissionDenied, "unknown host")
		}
		// Carry the verified host_id in context so handlers can rely
		// on it without re-reading metadata.
		ctx := context.WithValue(ss.Context(), agentHostKey{}, hostID)
		return handler(srv, &authedServerStream{ServerStream: ss, ctx: ctx})
	}
}

// agentHostFromClientCert pulls the verified-peer host_id out of a
// TLS client cert when one was presented. Returns ("", false) when
// the peer didn't present a cert (HTTP/JSON path on the multiplexed
// listener) or when the chain didn't validate.
func agentHostFromClientCert(ctx context.Context) (string, bool) {
	p, ok := peer.FromContext(ctx)
	if !ok || p.AuthInfo == nil {
		return "", false
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok {
		return "", false
	}
	chains := tlsInfo.State.VerifiedChains
	if len(chains) == 0 || len(chains[0]) == 0 {
		return "", false
	}
	leaf := chains[0][0]
	if leaf == nil || strings.TrimSpace(leaf.Subject.CommonName) == "" {
		return "", false
	}
	return strings.TrimSpace(leaf.Subject.CommonName), true
}

type agentHostKey struct{}

type authedServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *authedServerStream) Context() context.Context { return s.ctx }

// OpenConsoleSession (v5.47.0) — issues a fresh HMAC-signed token
// for a VM console + the WebSocket URL. Same token shape as the
// existing HTTP `/api/vms/console-token` endpoint, so a kvmctl
// `console <vm>` flow can pick whichever transport is convenient.
//
// Authentication is the gRPC stream interceptor's job — by the
// time the caller reaches this RPC the host_id has been bound via
// mTLS client cert (preferred) or the metadata header.
func (s *agentGRPCServer) OpenConsoleSession(ctx context.Context, req *agentpb.ConsoleRequest) (*agentpb.ConsoleResponse, error) {
	if req == nil || req.GetHostId() == "" || req.GetVmName() == "" {
		return nil, status.Error(codes.InvalidArgument, "host_id + vm_name required")
	}
	token, exp, nonce := s.store.SignConsoleToken(req.GetHostId(), req.GetVmName())
	wsURL := "/api/console-ws/" + req.GetHostId() + "/" + req.GetVmName()
	return &agentpb.ConsoleResponse{
		WsUrl:     wsURL,
		Token:     token,
		ExpiresAt: exp,
		Nonce:     nonce,
	}, nil
}

// startGRPCServerOn binds an already-built grpc.Server to its own
// TCP listener. Used when the operator wants a dedicated gRPC port
// alongside the multiplexed-on-8190 path.
func startGRPCServerOn(addr string, srv *grpc.Server, onFailure ...func(error)) (stop func(), err error) {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	go func() {
		log.Printf("grpc agent service listening on %s (dedicated port)", addr)
		if err := srv.Serve(lis); err != nil && err != grpc.ErrServerStopped {
			log.Printf("grpc: serve exited: %v", err)
			for _, report := range onFailure {
				report(err)
			}
		}
	}()
	stop = func() {
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

// startGRPCServer is the legacy single-port helper kept for the
// integration test harness (spins up a throw-away gRPC server on
// an ephemeral port). Production code uses buildGRPCServer +
// multiplexing instead.
func startGRPCServer(addr string, store *Store, tlsCreds credentials.TransportCredentials) (stop func(), err error) {
	srv := buildGRPCServer(store)
	if tlsCreds != nil {
		// Legacy path — not used in prod. Rebuild with creds, but
		// keep the auth-stream interceptor so this code path still
		// enforces the host_id check (Tier 2 #9, v5.37.1).
		srv = grpc.NewServer(
			grpc.Creds(tlsCreds),
			grpc.StreamInterceptor(agentAuthStreamInterceptor(store)),
			grpc.UnaryInterceptor(agentAuthUnaryInterceptor(store)),
		)
		agentpb.RegisterAgentServiceServer(srv, &agentGRPCServer{store: store})
	}
	return startGRPCServerOn(addr, srv)
}
