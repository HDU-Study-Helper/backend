package dbbridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"academic/backend/internal/model"
)

func TestPostgresCourseGrouping(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	owner := fixtureUser(t, db, "course_owner", model.RoleUser)
	other := fixtureUser(t, db, "course_other", model.RoleUser)
	repo := db.Platforms()
	c, e := repo.Create(ctx, owner.ID, "chaoxing")
	must(t, e)
	scope := model.DWSScope{UserID: owner.ID, ConnectionID: c.ID}
	l, e := repo.Acquire(ctx, scope)
	must(t, e)
	must(t, repo.SaveAuth(ctx, l, []byte("cipher"), "active", time.Now().Add(time.Hour)))
	for _, v := range []struct{ kind, id, course, class, title string }{{"course", "courseA", "10", "A", "课程甲"}, {"assignment", "work1", "10", "A", "作业1"}, {"progress", "progress1", "10", "A", "课程甲"}, {"course", "courseB", "10", "B", "课程乙班"}, {"assignment", "work2", "10", "B", "作业2"}, {"notice", "notice1", "", "", "账户通知"}} {
		b, _ := json.Marshal(map[string]any{"kind": v.kind, "title": v.title, "fields": map[string]string{"course_id": v.course, "class_id": v.class}})
		h := sha256.Sum256(b)
		record := model.PlatformRecord{Source: "chaoxing", Kind: v.kind, ExternalID: v.id, Hash: hex.EncodeToString(h[:]), Content: b, ObservedAt: time.Now().UTC()}
		must(t, repo.Commit(ctx, l, v.kind, []model.PlatformRecord{record}, []byte("cipher"), nil))
	}
	p := db.Preview().(model.CoursePreviewRepository)
	courses, unassociated, e := p.Courses(ctx, owner.ID, "chaoxing", c.ID)
	must(t, e)
	if len(courses) != 2 || unassociated != 1 {
		t.Fatal("course/class merged", courses, unassociated)
	}
	key := coursePreviewKey("10", "A")
	rows, e := p.CourseRecords(ctx, owner.ID, "chaoxing", c.ID, "", key, 0, 20)
	must(t, e)
	if len(rows) != 3 {
		t.Fatal("same course resources missing", len(rows))
	}
	rows, e = p.CourseRecords(ctx, owner.ID, "chaoxing", c.ID, "assignment", key, 0, 20)
	must(t, e)
	if len(rows) != 1 || rows[0].ExternalID != "work1" {
		t.Fatal("course filter crossed class")
	}
	rows, e = p.CourseRecords(ctx, owner.ID, "chaoxing", c.ID, "", "__unassociated__", 0, 20)
	must(t, e)
	if len(rows) != 1 || rows[0].Kind != "notice" {
		t.Fatal("account notice misclassified")
	}
	if _, _, e = p.Courses(ctx, other.ID, "chaoxing", c.ID); !errors.Is(e, model.ErrNotFound) {
		t.Fatal("foreign courses exposed")
	}
	if _, e = p.CourseRecords(ctx, other.ID, "chaoxing", c.ID, "", key, 0, 20); !errors.Is(e, model.ErrNotFound) {
		t.Fatal("foreign course records exposed")
	}
	if _, e = p.CourseRecords(ctx, owner.ID, "chaoxing", c.ID, "", "invalid", 0, 20); !errors.Is(e, model.ErrInvalidArgument) {
		t.Fatal("invalid course key accepted")
	}
}
