# KVM-Center Development Notes

## Architecture Decisions

### Why Host Networking for Server?
- Docker's default bridge network isolates the container from the host's LAN
- Agents are on separate hypervisors (10.10.100.11-13)
- Host networking allows direct routing to agent IPs
- Alternative: Use overlay network or explicit routing

### Why Flask for Agent Command Server?
- Lightweight, minimal dependencies
- Easy to embed in agent script
- No need for full FastAPI complexity on agents
- Single endpoint (/command) is sufficient

### UUID Handling
- libvirt returns UUIDs as 16-byte binary
- Must convert to hex string: `uuid_bytes.hex()`
- Never decode as UTF-8 (produces garbage)
- Store as 32-character hex string in database

### Database Choice: SQLite
- Single file, easy backup/restore
- No external dependencies
- Sufficient for homelab scale (<100 hosts, <1000 VMs)
- For production: Consider PostgreSQL with proper migrations

## Known Issues & Workarounds

### Issue: Port Conflicts on Docker Host
**Symptom:** Server won't start, "address already in use"
**Cause:** Port 8000/8080/8085 already bound by other containers
**Fix:** Change server port in server.py (uvicorn.run port=XXXX)

### Issue: Agent Can't Decode VM Names
**Symptom:** Telemetry fails with encoding errors
**Cause:** Some VM names contain non-UTF8 characters
**Fix:** Use `safe_decode()` with 'ignore' error handling

### Issue: Commands Return 500 Error
**Symptom:** UI shows "Error: undefined" on button click
**Causes:**
1. Server can't reach agent (network isolation)
2. Agent not running on port 8081
3. VM UUID mismatch (binary vs hex)
**Fix:** 
- Use host networking for server
- Verify agent status: `systemctl status kvm-center-agent`
- Ensure UUIDs are hex strings in database

### Issue: Database Tables Missing
**Symptom:** "no such table: hosts" errors
**Cause:** Database file exists but was created before init_db() ran
**Fix:** Delete database file, restart server to trigger init_db()

## Performance Considerations

### Telemetry Interval
- Default: 10 seconds
- Trade-off: Freshness vs. API load
- For 10 hosts: ~60 requests/minute to server
- Acceptable for homelab; consider 30-60s for larger deployments

### Database Growth
- Hosts table: ~1 row per host (updated in-place)
- VMs table: ~1 row per VM (updated in-place)
- No historical data stored (yet)
- For 1000 VMs: ~100KB database size

### Memory Usage
- Server: ~50MB RAM (FastAPI + SQLite)
- Agent: ~25MB RAM (Flask + libvirt)
- Negligible for modern hypervisors

## Future Enhancements

### v2.0 Features
1. **Console Access:**
   - Integrate noVNC or SPICE web client
   - Proxy VM console through server
   - Authentication required

2. **VM Migration:**
   - libvirt live migration support
   - Pre-flight checks (network, storage)
   - Progress tracking in UI

3. **Snapshots:**
   - Create/delete/revert snapshots
   - Snapshot list per VM
   - Storage impact warnings

4. **Alerting:**
   - Configurable thresholds (CPU, RAM, disk)
   - Telegram/Discord/Email notifications
   - Integrate with existing host-monitor system

5. **User Management:**
   - Simple auth (username/password)
   - Role-based access (admin vs viewer)
   - Audit logging for actions

### v3.0 Features
1. **VM Creation Wizard:**
   - Template-based deployment
   - Cloud-init support
   - Network/storage selection

2. **Resource Scheduling:**
   - DRS-like VM placement suggestions
   - Load balancing across hosts
   - Affinity/anti-affinity rules

3. **Advanced Networking:**
   - Virtual switch management
   - VLAN configuration
   - Port mirroring for debugging

## Testing Checklist

### Server Tests
- [ ] API responds on configured port
- [ ] Database initializes on first run
- [ ] Telemetry ingestion works
- [ ] Command routing to agents works
- [ ] UI loads and displays data

### Agent Tests
- [ ] Connects to libvirt successfully
- [ ] Reports telemetry every N seconds
- [ ] Command endpoint responds
- [ ] Handles VM power state changes
- [ ] Recovers from network interruptions

### Integration Tests
- [ ] Start VM from UI → VM powers on
- [ ] Stop VM from UI → VM powers off
- [ ] VM state reflects in UI within N seconds
- [ ] Search/filter works correctly
- [ ] Multiple concurrent users don't cause issues

## Debugging Tips

### Enable Verbose Logging

**Server:**
```python
# Add to server.py
import logging
logging.basicConfig(level=logging.DEBUG)
```

**Agent:**
```bash
# Edit systemd service
ExecStart=/usr/bin/python3 -u /opt/kvm-center-agent/agent.py
```

### Capture Network Traffic
```bash
# On server host
tcpdump -i any port 8081 -w commands.pcap

# On agent host
tcpdump -i any port 8090 -w telemetry.pcap
```

### Database Inspection
```bash
docker exec kvm-center-server sqlite3 /data/kvm_center.db \
  "SELECT vm_id, name, state FROM vms LIMIT 10;"
```

---

*Last updated: 2026-04-11*
