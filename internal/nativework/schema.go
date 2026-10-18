package nativework

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

var schemaOrder = []string{"native_meta", "native_targets", "native_requests", "native_writers"}
var supportedSchema = map[string]string{
	"native_meta":     `CREATE TABLE native_meta(version INTEGER NOT NULL CHECK(version=1))`,
	"native_targets":  `CREATE TABLE native_targets(uuid TEXT PRIMARY KEY,name TEXT NOT NULL COLLATE NOCASE UNIQUE,closed INTEGER NOT NULL DEFAULT 0 CHECK(closed IN(0,1)),revision INTEGER NOT NULL DEFAULT 0)`,
	"native_requests": `CREATE TABLE native_requests(id TEXT PRIMARY KEY,attempt TEXT NOT NULL UNIQUE,action TEXT NOT NULL,digest TEXT NOT NULL,target TEXT NOT NULL,name TEXT NOT NULL,session TEXT NOT NULL)`,
	"native_writers":  `CREATE TABLE native_writers(request TEXT NOT NULL,writer TEXT NOT NULL,parent TEXT NOT NULL,stopped INTEGER NOT NULL DEFAULT 0 CHECK(stopped IN(0,1)),cancel_requested INTEGER NOT NULL DEFAULT 0 CHECK(cancel_requested IN(0,1)),PRIMARY KEY(request,writer))`,
}

// Validate the complete supported schema and bounded row graph before any
// admission or affirmative evidence. Unsupported rows must not disappear in a
// join or be interpreted as stopped. No repair, pruning or migration is implied.
func validateJournal(ctx context.Context, tx *sql.Tx) error {
	bad := errors.New("unsupported or inconsistent native writer journal")
	rows, err := tx.QueryContext(ctx, `SELECT name,type,sql FROM sqlite_master WHERE name NOT GLOB 'sqlite_*'`)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for rows.Next() {
		var name, kind, ddl string
		if err = rows.Scan(&name, &kind, &ddl); err != nil {
			break
		}
		if kind != "table" || supportedSchema[name] != ddl || seen[name] {
			err = bad
			break
		}
		seen[name] = true
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	if len(seen) != len(schemaOrder) {
		return bad
	}
	var version, n int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(MIN(version),0) FROM native_meta`).Scan(&n, &version); err != nil {
		return err
	}
	if n != 1 || version != 1 {
		return bad
	}
	targets := map[string]Target{}
	names := map[string]string{}
	rows, err = tx.QueryContext(ctx, `SELECT uuid,name,closed,revision FROM native_targets`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var t Target
		var closed int
		var rev int64
		if err = rows.Scan(&t.UUID, &t.Name, &closed, &rev); err != nil {
			break
		}
		if !validTarget(t) || (closed != 0 && closed != 1) || rev < 0 || len(targets) >= maxRecords {
			err = bad
			break
		}
		key := strings.ToLower(t.Name)
		if _, ok := targets[t.UUID]; ok {
			err = bad
			break
		}
		if _, ok := names[key]; ok {
			err = bad
			break
		}
		targets[t.UUID] = t
		names[key] = t.UUID
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for name, id := range names {
		if other, ok := targets[name]; ok && other.UUID != id {
			return bad
		}
	}
	requests := map[string]Request{}
	attempts := map[string]bool{}
	rows, err = tx.QueryContext(ctx, `SELECT id,attempt,action,digest,target,name,session FROM native_requests`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var r Request
		var session string
		if err = rows.Scan(&r.ID, &r.AttemptID, &r.Action, &r.ParamsSHA256, &r.Target.UUID, &r.Target.Name, &session); err != nil {
			break
		}
		if !validRequest(r) || !digestPattern.MatchString(session) || targets[r.Target.UUID] != r.Target || len(requests) >= maxRecords || attempts[r.AttemptID] {
			err = bad
			break
		}
		if _, ok := requests[r.ID]; ok {
			err = bad
			break
		}
		requests[r.ID] = r
		attempts[r.AttemptID] = true
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	type key struct{ request, writer string }
	writers := map[key]string{}
	rows, err = tx.QueryContext(ctx, `SELECT request,writer,parent,stopped,cancel_requested FROM native_writers`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var k key
		var parent string
		var stopped, cancel int
		if err = rows.Scan(&k.request, &k.writer, &parent, &stopped, &cancel); err != nil {
			break
		}
		_, known := requests[k.request]
		if !known || !textOK(k.writer) || (stopped != 0 && stopped != 1) || (cancel != 0 && cancel != 1) || len(writers) >= maxRecords || (k.writer == "root" && parent != "") || (k.writer != "root" && !textOK(parent)) {
			err = bad
			break
		}
		if _, ok := writers[k]; ok {
			err = bad
			break
		}
		writers[k] = parent
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for id := range requests {
		if _, ok := writers[key{id, "root"}]; !ok {
			return bad
		}
	}
	// Every descendant must lead to its own immutable attempt's root, not a
	// missing writer, another attempt or a cycle.
	state := map[key]int{}
	var visit func(key) bool
	visit = func(k key) bool {
		if state[k] == 2 {
			return true
		}
		if state[k] == 1 {
			return false
		}
		parent, ok := writers[k]
		if !ok {
			return false
		}
		state[k] = 1
		if k.writer != "root" && !visit(key{k.request, parent}) {
			return false
		}
		state[k] = 2
		return true
	}
	for k := range writers {
		if !visit(k) {
			return bad
		}
	}
	return nil
}
