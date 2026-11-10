package platform

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"time"

	"academic/backend/internal/contract"
	"academic/backend/internal/model"
	"academic/backend/internal/schedule"
)

// SyncTimetable always starts a fresh complete snapshot. Partial pages never
// change either archived timetable records or materialized schedules.
func (s *Service) SyncTimetable(ctx context.Context, scope model.DWSScope, sem model.Semester, calendar schedule.Calendar) (model.TimetableResult, error) {
	result := model.TimetableResult{Semester: sem}
	if _, _, err := calendar.Validate(sem); err != nil {
		return result, err
	}
	committer, ok := s.repo.(model.TimetableCommitter)
	if !ok {
		return result, contract.ErrUnsupported
	}
	key := "schedule:" + strconv.Itoa(sem.AcademicYear) + ":" + strconv.Itoa(sem.Term)
	err := s.operate(ctx, scope, key, func(ctx context.Context, l model.PlatformLease, state model.PlatformState, a contract.Adapter) error {
		if state.Connection.Source != "jwxt" {
			return contract.ErrUnsupported
		}
		if state.Connection.State != "active" {
			return contract.ErrAuthRequired
		}
		reader, ok := a.(contract.AcademicTermReader)
		if !ok {
			return contract.ErrUnsupported
		}
		l.CheckpointKey = key
		started := time.Now()
		count := 0
		var syncErr error
		if observer, ok := s.repo.(model.SyncObserver); ok {
			if e := observer.StartSync(ctx, l, "schedule"); e != nil {
				return e
			}
			defer func() {
				cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				code := ""
				if syncErr != nil {
					code = errorCode(syncErr)
				}
				_ = observer.FinishSync(cleanup, l, "schedule", code, time.Since(started).Milliseconds(), count, false)
			}()
		}
		// Capture all failures for the existing sync status UI.
		syncErr = func() error {
			raw, e := s.vault.Open(scope.UserID, scope.ConnectionID, state.Session)
			if e != nil {
				return e
			}
			defer func() { clear(raw) }()
			cursor := ""
			all := []contract.Record{}
			seenCursors := map[string]bool{}
			seenRecords := map[string]bool{}
			parent, _ := json.Marshal([]string{strconv.Itoa(sem.AcademicYear), strconv.Itoa(sem.Term)})
			for pages := 0; ; pages++ {
				if pages >= 100 {
					return contract.ErrParse
				}
				page, e := reader.SyncTerm(ctx, adapterScope(scope), contract.Session(raw), contract.Schedule, sem.AcademicYear, sem.Term, cursor)
				if e != nil {
					return e
				}
				if len(page.Records) > 500 || len(all)+len(page.Records) > 5000 {
					return contract.ErrParse
				}
				for _, record := range page.Records {
					if record.Kind != contract.Schedule || record.ExternalID == "" || record.ParentID != string(parent) || seenRecords[record.ExternalID] {
						return contract.ErrParse
					}
					seenRecords[record.ExternalID] = true
					all = append(all, record)
				}
				if page.Session != nil {
					updated := append([]byte{}, page.Session...)
					clear(raw)
					clear(page.Session)
					raw = updated
				}
				if !page.HasMore {
					if page.NextCursor != "" {
						return contract.ErrParse
					}
					break
				}
				if page.NextCursor == "" || page.NextCursor == cursor || seenCursors[page.NextCursor] {
					return contract.ErrParse
				}
				seenCursors[page.NextCursor] = true
				cursor = page.NextCursor
			}
			bundle, skipped, e := schedule.Expand(all, sem, calendar)
			if e != nil {
				return e
			}
			records := make([]model.PlatformRecord, 0, len(all))
			total := 0
			for _, record := range all {
				record.ObservedAt = time.Time{}
				content, e := json.Marshal(record)
				if e != nil || len(content) > 1<<20 {
					return contract.ErrParse
				}
				total += len(content)
				if total > 8<<20 {
					return contract.ErrParse
				}
				hash := sha256.Sum256(content)
				records = append(records, model.PlatformRecord{Source: "jwxt", Kind: "schedule", ExternalID: record.ExternalID, ParentID: record.ParentID, Hash: hex.EncodeToString(hash[:]), Content: content, ObservedAt: time.Now().UTC()})
			}
			sealed, e := s.vault.Seal(scope.UserID, scope.ConnectionID, raw)
			if e != nil {
				return e
			}
			result, e = committer.CommitTimetable(ctx, l, records, sealed, bundle)
			if e != nil {
				return e
			}
			result.SkippedPractice = skipped
			count = len(records)
			return nil
		}()
		return syncErr
	})
	return result, err
}
