package dbbridge

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"os"
	"testing"
)

// Opt-in read-only preview over existing events. It runs the production resolver
// and grouping keys; it never seals, activates, mutates or calls a model/source.
func TestReadOnlyCourseBatchPreview(t *testing.T) {
	if os.Getenv("COURSE_PREVIEW_READ_ONLY") != "1" {
		t.Skip("explicit read-only preview required")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, os.Getenv("DATABASE_URL"))
	must(t, err)
	defer conn.Close(ctx)
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly, IsoLevel: pgx.RepeatableRead})
	must(t, err)
	defer tx.Rollback(ctx)
	round := "0e7955c99a5a4c934b8eace86e9ce6f4"
	rows, err := tx.Query(ctx, `SELECT id,round_id,owner_id,source,payload FROM control_events WHERE round_id=$1 ORDER BY owner_id,source,created_at,id`, round)
	must(t, err)
	all := []controlSealEvent{}
	for rows.Next() {
		var e controlSealEvent
		must(t, rows.Scan(&e.id, &e.round, &e.owner, &e.source, &e.raw))
		all = append(all, e)
	}
	must(t, rows.Err())
	rows.Close()
	type group struct {
		Source     string         `json:"source"`
		Connection string         `json:"connection_id,omitempty"`
		CourseRef  string         `json:"course_ref,omitempty"`
		Name       string         `json:"course_name,omitempty"`
		Kind       string         `json:"kind"`
		Count      int            `json:"changes"`
		Bytes      int            `json:"raw_event_bytes"`
		Blocked    bool           `json:"would_block"`
		Resources  map[string]int `json:"resources"`
	}
	out := []group{}
	for len(all) > 0 {
		n := 1
		for n < len(all) && all[n].owner == all[0].owner && all[n].source == all[0].source {
			n++
		}
		events := all[:n]
		if events[0].source == "chaoxing" || events[0].source == "mooc" {
			resolver, err := loadControlCourseResolver(ctx, tx, events)
			must(t, err)
			index := map[[2]string]int{}
			for _, e := range events {
				scope := resolver.resolve(e.change)
				key := [2]string{scope.ConnectionID, scope.CourseRef}
				i, ok := index[key]
				if !ok {
					i = len(out)
					index[key] = i
					out = append(out, group{Source: e.source, Connection: scope.ConnectionID, CourseRef: scope.CourseRef, Name: scope.CourseName, Kind: scope.Kind, Resources: map[string]int{}})
				}
				out[i].Count++
				out[i].Bytes += len(e.raw)
				out[i].Resources[e.change.Resource]++
				out[i].Blocked = out[i].Kind == "course" && out[i].Bytes > ControlCourseBatchBytes
			}
		} else {
			g := group{Source: events[0].source, Kind: "legacy_platform", Resources: map[string]int{}}
			for _, e := range events {
				var c struct {
					Resource string `json:"resource"`
				}
				must(t, json.Unmarshal(e.raw, &c))
				g.Count++
				g.Bytes += len(e.raw)
				g.Resources[c.Resource]++
			}
			out = append(out, g)
		}
		all = all[n:]
	}
	raw, err := json.MarshalIndent(map[string]any{"round_id": round, "read_only": true, "uses_production_resolver": true, "historical_batches_changed": false, "agents_started": false, "groups": out}, "", "  ")
	must(t, err)
	must(t, os.WriteFile("../../../.build/course-batch-preview.json", raw, 0600))
	must(t, tx.Commit(ctx))
}
