package main

// Expiry watcher for all certificate material the control plane owns:
// server TLS leaf, cluster CA, and every enrolled hypervisor cert.
// Ticks every 6 hours; fires a single warning per (kind, identity)
// edge into the configured threshold window.
//
// Thresholds:
//   - server TLS leaf  → warn at <30 days
//   - hypervisor leaf  → warn at <30 days
//   - cluster CA       → warn at <180 days (6 months — reissuance is
//     invasive; give operators long lead time)
//
// Severity escalates to "critical" at <7 days for leaves and <30
// days for the CA so the UI + webhooks can distinguish a tractable
// warning from an imminent outage.

import (
	"log"
	"strconv"
	"time"

	"kvm-center/internal/models"
)

const (
	expiryWatcherInterval = 6 * time.Hour

	leafWarnDays     = 30
	leafCriticalDays = 7
	caWarnDays       = 180
	caCriticalDays   = 30
)

// warnedCerts tracks one-shot edge-trigger state keyed by a composite
// (kind, identity, severity). Transitions back out of the window
// clear the entry so a cert that gets rotated and later approaches
// expiry again is re-warned.
type certWatcherState struct {
	warned map[string]struct{}
}

func newCertWatcherState() *certWatcherState {
	return &certWatcherState{warned: make(map[string]struct{})}
}

func (s *certWatcherState) markWarned(key string) { s.warned[key] = struct{}{} }
func (s *certWatcherState) alreadyWarned(key string) bool {
	_, ok := s.warned[key]
	return ok
}
func (s *certWatcherState) clear(key string) { delete(s.warned, key) }

func startExpiryWatcher(store *Store, reloader *CertReloader, ca *ClusterCA) {
	state := newCertWatcherState()
	t := time.NewTicker(expiryWatcherInterval)
	defer t.Stop()
	check := func() { runExpiryCheck(store, reloader, ca, state, time.Now()) }
	check()
	for {
		select {
		case <-serverContext().Done():
			return
		case <-t.C:
			check()
		}
	}
}

// runExpiryCheck is factored for testability — pass a frozen `now`
// and a prepared state, assert on the notifications fired.
func runExpiryCheck(store *Store, reloader *CertReloader, ca *ClusterCA, state *certWatcherState, now time.Time) {
	// Server TLS leaf.
	if reloader != nil {
		if c, err := reloader.CurrentCert(); err == nil {
			days := daysUntil(c.NotAfter, now)
			evaluate(state, "server-tls", c.Subject.String(), days, leafWarnDays, leafCriticalDays, func(sev string) {
				store.fireNotification(&models.NotificationEvent{
					Type:     NotifyTLSExpiring,
					Title:    titleFor("Server TLS certificate", sev),
					Message:  "Server cert expires in " + strconv.Itoa(days) + " days (" + c.NotAfter.UTC().Format("2006-01-02") + ").",
					Resource: "tls",
					Severity: sev,
					Detail: map[string]string{
						"kind":    "server-tls",
						"days":    strconv.Itoa(days),
						"subject": c.Subject.String(),
					},
				})
			})
		} else {
			log.Printf("cert-watcher: server tls cert unreadable: %v", err)
		}
	}

	// Cluster CA.
	if ca != nil {
		cert := ca.Cert()
		if cert != nil {
			days := daysUntil(cert.NotAfter, now)
			evaluate(state, "cluster-ca", cert.Subject.String(), days, caWarnDays, caCriticalDays, func(sev string) {
				store.fireNotification(&models.NotificationEvent{
					Type:     NotifyCAExpiring,
					Title:    titleFor("Cluster CA", sev),
					Message:  "Cluster CA expires in " + strconv.Itoa(days) + " days (" + cert.NotAfter.UTC().Format("2006-01-02") + "). Reissuance requires re-enrolling every hypervisor — schedule a maintenance window.",
					Resource: "ca",
					Severity: sev,
					Detail: map[string]string{
						"kind":    "cluster-ca",
						"days":    strconv.Itoa(days),
						"subject": cert.Subject.String(),
					},
				})
			})
		}
	}

	// Hypervisor leaf certs. Skip revoked entries — those are already
	// unusable and operator action is documented elsewhere.
	for _, hc := range store.listHypervisorCerts() {
		if hc == nil || !hc.RevokedAt.IsZero() {
			continue
		}
		days := daysUntil(hc.NotAfter, now)
		identity := "hypervisor:" + hc.HostID
		evaluate(state, "hypervisor-cert", identity, days, leafWarnDays, leafCriticalDays, func(sev string) {
			store.fireNotification(&models.NotificationEvent{
				Type:     NotifyHypervisorCertExpiring,
				Title:    titleFor("Hypervisor certificate ("+hc.HostID+")", sev),
				Message:  "Hypervisor " + hc.HostID + " cert expires in " + strconv.Itoa(days) + " days (" + hc.NotAfter.UTC().Format("2006-01-02") + "). Migrations to/from this host will fail after expiry — re-run the enroll flow before then.",
				Resource: "hypervisor:" + hc.HostID,
				Severity: sev,
				Detail: map[string]string{
					"kind":    "hypervisor-cert",
					"days":    strconv.Itoa(days),
					"host_id": hc.HostID,
					"serial":  hc.SerialHex,
				},
			})
		})
	}
}

// evaluate decides which (if any) severity threshold the cert has
// crossed and invokes `fire` exactly once per transition edge.
func evaluate(state *certWatcherState, kind, identity string, days, warnDays, criticalDays int, fire func(severity string)) {
	critKey := kind + "|" + identity + "|critical"
	warnKey := kind + "|" + identity + "|warning"

	switch {
	case days < criticalDays:
		if !state.alreadyWarned(critKey) {
			fire("critical")
			state.markWarned(critKey)
		}
		// Leave the warning edge flagged too so a cert that jumps
		// straight into critical on a restart doesn't re-fire warning
		// if it later drifts back into the warn window.
		state.markWarned(warnKey)
	case days < warnDays:
		if !state.alreadyWarned(warnKey) {
			fire("warning")
			state.markWarned(warnKey)
		}
		state.clear(critKey)
	default:
		state.clear(critKey)
		state.clear(warnKey)
	}
}

func daysUntil(t time.Time, now time.Time) int {
	return int(t.Sub(now).Hours() / 24)
}

func titleFor(subject, severity string) string {
	if severity == "critical" {
		return subject + " expires imminently"
	}
	return subject + " expiring soon"
}
