package dbbridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"time"

	"academic/backend/internal/model"
	"github.com/jackc/pgx/v5"
)

type ArchiveReplayOptions struct {
	Key        string
	Execute    bool
	ParkQueued bool
	// Backup must durably save a private immutable manifest before commit. An
	// error rolls back round creation, queue changes and observations together.
	Backup func(context.Context, ArchiveReplayReport) error
}
type ArchiveReplaySource struct {
	Source   string `json:"source"`
	Resource string `json:"resource"`
	Count    int    `json:"count"`
}
type ArchiveReplayParked struct {
	Batch    model.ControlBatch `json:"batch"`
	EventIDs []string           `json:"event_ids"`
	Reason   string             `json:"reason"`
}
type ArchiveReplayReport struct {
	Key                  string                `json:"key"`
	RoundID              string                `json:"round_id"`
	SnapshotAt           time.Time             `json:"snapshot_at"`
	Execute              bool                  `json:"execute"`
	Existing             bool                  `json:"existing"`
	ObservationSemantics string                `json:"observation_semantics"`
	Sources              []ArchiveReplaySource `json:"sources"`
	FreezeCount          int                   `json:"freeze_count"`
	Digest               string                `json:"snapshot_sha256"`
	Batches              map[string]int        `json:"batch_counts"`
	BatchIDs             []string              `json:"batch_ids"`
	Parked               []ArchiveReplayParked `json:"parked_queued"`
}
type archiveObservation struct {
	owner, source string
	change        model.ControlChange
}

var archiveReplayKey = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,119}$`)

func archiveReplayRound(key string) string {
	sum := sha256.Sum256([]byte("archive-replay/" + key))
	return "replay_" + hex.EncodeToString(sum[:16])
}

// PrepareArchiveReplay creates observations of the existing authorized archive.
// It never collects sources, activates batches, or edits records/checkpoints,
// Workflows, memories or prior terminal history. Default mode is READ ONLY.
func (d *DB) PrepareArchiveReplay(ctx context.Context, o ArchiveReplayOptions) (out ArchiveReplayReport, err error) {
	if !archiveReplayKey.MatchString(o.Key) {
		return out, model.ErrInvalidArgument
	}
	access := pgx.ReadOnly
	if o.Execute {
		access = pgx.ReadWrite
	}
	tx, err := d.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: access})
	if err != nil {
		return out, storageError(err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if o.Execute {
		// Claim and Activate acquire conflicting table locks. Take this before the
		// repeatable-read snapshot so parked queued rows cannot race a claim.
		if _, err = tx.Exec(ctx, `LOCK TABLE control_batches IN EXCLUSIVE MODE`); err != nil {
			return out, storageError(err)
		}
		if _, err = tx.Exec(ctx, `LOCK TABLE agent_runs,control_runs IN SHARE MODE`); err != nil {
			return out, storageError(err)
		}
		if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(730022411)`); err != nil {
			return out, storageError(err)
		}
	}
	out = ArchiveReplayReport{Key: o.Key, RoundID: archiveReplayRound(o.Key), Execute: o.Execute, ObservationSemantics: "created means replay observation of frozen authorized archive, not a newly published source item; occurred_at preserves original observed_at", Sources: []ArchiveReplaySource{}, Batches: map[string]int{}, BatchIDs: []string{}, Parked: []ArchiveReplayParked{}}
	if err = tx.QueryRow(ctx, `SELECT transaction_timestamp()`).Scan(&out.SnapshotAt); err != nil {
		return out, storageError(err)
	}
	var stored []byte
	err = tx.QueryRow(ctx, `SELECT results FROM control_rounds WHERE id=$1 AND lease_owner=$2`, out.RoundID, "archive-replay:"+o.Key).Scan(&stored)
	if err == nil {
		var results []struct {
			Report *ArchiveReplayReport `json:"archive_replay_report"`
		}
		if json.Unmarshal(stored, &results) != nil || len(results) == 0 || results[0].Report == nil {
			return out, errors.New("existing replay lacks a durable manifest; review before reuse")
		}
		out = *results[0].Report
		out.Existing = true
		out.Execute = o.Execute
		if err = archiveReplayBatches(ctx, tx, &out); err != nil {
			return out, err
		}
		return out, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return out, storageError(err)
	}
	if err = archiveReplayQueue(ctx, tx, &out); err != nil {
		return out, err
	}
	observations, err := archiveReplayObservations(ctx, tx)
	if err != nil {
		return out, err
	}
	counts := map[[2]string]int{}
	hash := sha256.New()
	for _, v := range observations {
		counts[[2]string{v.source, v.change.Resource}]++
		raw, _ := json.Marshal(v.change)
		hash.Write([]byte(v.owner))
		hash.Write([]byte(v.source))
		hash.Write(raw)
	}
	out.FreezeCount = len(observations)
	out.Digest = hex.EncodeToString(hash.Sum(nil))
	for key, count := range counts {
		out.Sources = append(out.Sources, ArchiveReplaySource{Source: key[0], Resource: key[1], Count: count})
	}
	sort.Slice(out.Sources, func(i, j int) bool {
		a, b := out.Sources[i], out.Sources[j]
		return a.Source+"/"+a.Resource < b.Source+"/"+b.Resource
	})
	if !o.Execute {
		return out, tx.Commit(ctx)
	}
	var running int
	if err = tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM control_batches WHERE status='running')+(SELECT count(*) FROM agent_runs WHERE status='running')+(SELECT count(*) FROM control_runs WHERE state NOT IN('succeeded','failed','cancelled','interrupted'))`).Scan(&running); err != nil {
		return out, storageError(err)
	}
	if running != 0 {
		return out, errors.New("archive replay refused: active runs exist")
	}
	if len(out.Parked) > 0 && !o.ParkQueued {
		return out, errors.New("archive replay refused: queued batches exist; review backup and use --park-queued")
	}
	if o.Backup == nil {
		return out, errors.New("execute requires a durable private backup callback")
	}
	if _, err = tx.Exec(ctx, `INSERT INTO control_rounds(id,status,lease_owner,finished_at) VALUES($1,'completed',$2,now())`, out.RoundID, "archive-replay:"+o.Key); err != nil {
		return out, storageError(err)
	}
	replayCtx := model.WithControlRound(ctx, out.RoundID)
	for _, v := range observations {
		after, e := archiveReplaySnapshot(v.change.After, o.Key, out.RoundID)
		if e != nil {
			return out, e
		}
		v.change.After = after
		if e = captureControl(replayCtx, tx, v.owner, v.source, []model.ControlChange{v.change}); e != nil {
			return out, storageError(e)
		}
	}
	if err = sealControlRoundBatches(ctx, tx, out.RoundID); err != nil {
		return out, storageError(err)
	}
	if err = seedControlRoundItems(ctx, tx, out.RoundID); err != nil {
		return out, storageError(err)
	}
	if err = archiveReplayBatches(ctx, tx, &out); err != nil {
		return out, err
	}
	// The manifest includes every original queued payload and event identity. It
	// is saved before any transaction commit, and may survive a later rollback.
	if err = o.Backup(ctx, out); err != nil {
		return out, fmt.Errorf("private replay backup failed: %w", err)
	}
	if len(out.Parked) > 0 {
		ids := make([]string, len(out.Parked))
		for i, p := range out.Parked {
			ids[i] = p.Batch.ID
		}
		tag, e := tx.Exec(ctx, `UPDATE control_batches SET status='pending',error_code='ARCHIVE_REPLAY_PARKED_QUEUED',updated_at=now() WHERE id=ANY($1::text[]) AND status='queued'`, ids)
		if e != nil {
			return out, storageError(e)
		}
		if int(tag.RowsAffected()) != len(ids) {
			return out, model.ErrConflict
		}
	}
	totals := map[string]int{}
	for _, s := range out.Sources {
		totals[s.Source] += s.Count
	}
	results := []map[string]any{}
	sourceNames := []string{}
	for source := range totals {
		sourceNames = append(sourceNames, source)
	}
	sort.Strings(sourceNames)
	for _, source := range sourceNames {
		results = append(results, map[string]any{"source": source, "status": "completed", "created": totals[source], "succeeded": 1})
	}
	if len(results) == 0 {
		results = append(results, map[string]any{"source": "archive-replay", "status": "completed", "created": 0})
	}
	results[0]["archive_replay_report"] = out
	raw, _ := json.Marshal(results)
	if _, err = tx.Exec(ctx, `UPDATE control_rounds SET results=$2 WHERE id=$1`, out.RoundID, raw); err != nil {
		return out, storageError(err)
	}
	return out, storageError(tx.Commit(ctx))
}

func archiveReplayQueue(ctx context.Context, tx pgx.Tx, out *ArchiveReplayReport) error {
	rows, e := tx.Query(ctx, `SELECT `+batchCols+` FROM control_batches WHERE status='queued' ORDER BY created_at,id`)
	if e != nil {
		return storageError(e)
	}
	for rows.Next() {
		b, e := scanBatch(rows)
		if e != nil {
			rows.Close()
			return e
		}
		out.Parked = append(out.Parked, ArchiveReplayParked{Batch: b, EventIDs: []string{}, Reason: "dequeue old unclaimed batch so this replay cannot mix with an earlier queue; all contents/history retained"})
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return storageError(e)
	}
	for i := range out.Parked {
		rows, e = tx.Query(ctx, `SELECT id FROM control_events WHERE batch_id=$1 ORDER BY id`, out.Parked[i].Batch.ID)
		if e != nil {
			return storageError(e)
		}
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				rows.Close()
				return e
			}
			out.Parked[i].EventIDs = append(out.Parked[i].EventIDs, id)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return storageError(e)
		}
	}
	return nil
}
func archiveReplayBatches(ctx context.Context, tx pgx.Tx, out *ArchiveReplayReport) error {
	out.Batches = map[string]int{}
	out.BatchIDs = []string{}
	rows, e := tx.Query(ctx, `SELECT id,status FROM control_batches WHERE round_id=$1 ORDER BY created_at,id`, out.RoundID)
	if e != nil {
		return storageError(e)
	}
	defer rows.Close()
	for rows.Next() {
		var id, status string
		if e = rows.Scan(&id, &status); e != nil {
			return e
		}
		out.BatchIDs = append(out.BatchIDs, id)
		out.Batches[status]++
	}
	return storageError(rows.Err())
}
func archiveReplaySnapshot(raw json.RawMessage, key, round string) (json.RawMessage, error) {
	var value map[string]json.RawMessage
	if json.Unmarshal(raw, &value) != nil || value == nil {
		return nil, errors.New("archive snapshot is not a JSON object")
	}
	marker, _ := json.Marshal(map[string]any{"key": key, "round_id": round, "kind": "frozen_archive_observation", "new_publication": false})
	value["archive_replay"] = marker
	return json.Marshal(value)
}
func archiveReplayObservations(ctx context.Context, tx pgx.Tx) (out []archiveObservation, err error) {
	out = []archiveObservation{}
	rows, e := tx.Query(ctx, `SELECT p.user_id,p.source,p.kind,p.connection_id,p.id::text,p.content,p.observed_at FROM platform_records p JOIN platform_connections c ON(c.user_id,c.id)=(p.user_id,p.connection_id) JOIN users u ON u.id=p.user_id WHERE NOT u.disabled AND c.enabled AND c.state='active' ORDER BY p.user_id,p.source,p.connection_id,p.id`)
	if e != nil {
		return nil, storageError(e)
	}
	for rows.Next() {
		var v archiveObservation
		v.change.Operation = "created"
		if e = rows.Scan(&v.owner, &v.source, &v.change.Resource, &v.change.ConnectionID, &v.change.RecordID, &v.change.After, &v.change.OccurredAt); e != nil {
			rows.Close()
			return nil, e
		}
		out = append(out, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	rows, e = tx.Query(ctx, `SELECT m.user_id,m.connection_id,m.group_id,m.external_id,m.content,m.observed_at FROM dws_messages m JOIN dws_connections c ON(c.user_id,c.id)=(m.user_id,m.connection_id) JOIN dws_groups g ON(g.user_id,g.connection_id,g.id)=(m.user_id,m.connection_id,m.group_id) JOIN users u ON u.id=m.user_id WHERE NOT u.disabled AND c.enabled AND c.state='active' ORDER BY m.user_id,m.connection_id,m.group_id,m.id`)
	if e != nil {
		return nil, storageError(e)
	}
	for rows.Next() {
		v := archiveObservation{source: "dws", change: model.ControlChange{Operation: "created", Resource: "message"}}
		if e = rows.Scan(&v.owner, &v.change.ConnectionID, &v.change.GroupID, &v.change.RecordID, &v.change.After, &v.change.OccurredAt); e != nil {
			rows.Close()
			return nil, e
		}
		out = append(out, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	rows, e = tx.Query(ctx, `SELECT a.user_id,a.connection_id,m.group_id,m.id::text||':'||a.id,jsonb_build_object('name',a.name,'media_type',a.media_type,'sha256',a.sha256),m.observed_at FROM dws_attachments a JOIN dws_messages m ON(m.user_id,m.connection_id,m.id)=(a.user_id,a.connection_id,a.message_id) JOIN dws_connections c ON(c.user_id,c.id)=(m.user_id,m.connection_id) JOIN dws_groups g ON(g.user_id,g.connection_id,g.id)=(m.user_id,m.connection_id,m.group_id) JOIN users u ON u.id=m.user_id WHERE NOT u.disabled AND c.enabled AND c.state='active' ORDER BY a.user_id,a.connection_id,m.group_id,m.id,a.id`)
	if e != nil {
		return nil, storageError(e)
	}
	for rows.Next() {
		v := archiveObservation{source: "dws", change: model.ControlChange{Operation: "created", Resource: "attachment"}}
		if e = rows.Scan(&v.owner, &v.change.ConnectionID, &v.change.GroupID, &v.change.RecordID, &v.change.After, &v.change.OccurredAt); e != nil {
			rows.Close()
			return nil, e
		}
		out = append(out, v)
	}
	e = rows.Err()
	rows.Close()
	return out, storageError(e)
}
