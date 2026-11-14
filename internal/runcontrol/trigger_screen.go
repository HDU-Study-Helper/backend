package runcontrol

import (
	"context"
	"encoding/json"
	"fmt"

	f "academic/agent/framework"
	"academic/backend/internal/model"
)

type triggerPage struct {
	input   json.RawMessage
	changes []model.ControlChange
}

// Paging bounds model inputs, while the persisted course batch remains intact.
func triggerPages(b model.ControlBatch) ([]triggerPage, error) {
	if !courseBatch(b) || b.Scope.Kind != "course" {
		raw, err := triggerInput(b)
		return []triggerPage{{input: raw, changes: b.Changes}}, err
	}
	pages := []triggerPage{}
	for start := 0; start < len(b.Changes) || start == 0; {
		end := start
		var input json.RawMessage
		for end < len(b.Changes) && end-start < triggerPageEvents {
			candidate := b
			candidate.Changes = b.Changes[start : end+1]
			// Use the maximum possible page count while sizing, then replace it
			// with the actual count after all page boundaries are known.
			raw, err := encodeTriggerInput(candidate, map[string]int{"index": len(pages) + 1, "count": len(b.Changes), "change_offset": start, "total_changes": len(b.Changes)})
			if err != nil || len(raw) > triggerPageBytes-triggerCorrectionReserve {
				if end == start {
					if err != nil {
						return nil, err
					}
					return nil, f.Fail("TRIGGER_INPUT_TOO_LARGE", "one course change exceeds trigger page budget; complete evidence remains saved")
				}
				break
			}
			input = raw
			end++
		}
		if len(b.Changes) == 0 {
			raw, err := encodeTriggerInput(b, map[string]int{"index": 1, "count": 1, "change_offset": 0, "total_changes": 0})
			if err != nil {
				return nil, err
			}
			return []triggerPage{{input: raw, changes: b.Changes}}, nil
		}
		pages = append(pages, triggerPage{input: input, changes: b.Changes[start:end]})
		start = end
	}
	offset := 0
	for i := range pages {
		candidate := b
		candidate.Changes = pages[i].changes
		raw, err := encodeTriggerInput(candidate, map[string]int{"index": i + 1, "count": len(pages), "change_offset": offset, "total_changes": len(b.Changes)})
		if err != nil {
			return nil, err
		}
		pages[i].input = raw
		offset += len(candidate.Changes)
	}
	return pages, nil
}

func (s *Service) screenTrigger(ctx context.Context, b model.ControlBatch, agentID string) (triggerDecision, json.RawMessage, error) {
	pages, err := triggerPages(b)
	if err != nil {
		return triggerDecision{}, nil, err
	}
	out := triggerDecision{Decision: "IGNORE", IDs: []string{}}
	selected := map[string]bool{}
	counts := map[string]int{}
	for _, page := range pages {
		result, err := s.Agents.Run(ctx, b.OwnerID, f.RunRequest{AgentID: agentID, Trigger: f.DataChanged, Input: page.input})
		if err != nil {
			return out, nil, err
		}
		decision, err := validateTrigger([]byte(result.Output), page.changes)
		if err != nil {
			var correction map[string]any
			if err := json.Unmarshal(page.input, &correction); err != nil {
				return out, nil, err
			}
			correction["validation_error"] = "只输出decision=TRIGGER/UNCERTAIN/IGNORE,reason,relevant_change_ids；ID不得重复、仅来自本页change_id。IGNORE必须空数组，不得额外字段。"
			// The invalid model response can be arbitrarily large. A fixed error
			// describes the repair without echoing it into the next request.
			input := mustJSON(correction)
			if courseBatch(b) && b.Scope.Kind == "course" && len(input) > triggerPageBytes {
				return out, nil, f.Fail("TRIGGER_INPUT_TOO_LARGE", "trigger correction exceeds page budget")
			}
			result, err = s.Agents.Run(ctx, b.OwnerID, f.RunRequest{AgentID: agentID, Trigger: f.DataChanged, Input: input})
			if err != nil {
				return out, nil, err
			}
			decision, err = validateTrigger([]byte(result.Output), page.changes)
			if err != nil {
				return out, nil, err
			}
		}
		if len(pages) == 1 {
			return decision, mustJSON(decision), nil
		}
		counts[decision.Decision]++
		if decision.Decision == "TRIGGER" || decision.Decision == "UNCERTAIN" && out.Decision == "IGNORE" {
			out.Decision = decision.Decision
		}
		for _, id := range decision.IDs {
			selected[id] = true
		}
	}
	for _, change := range b.Changes {
		if selected[change.ID] {
			out.IDs = append(out.IDs, change.ID)
			delete(selected, change.ID)
		}
	}
	out.Reason = fmt.Sprintf("完整筛查本课程%d条变化，共%d页：TRIGGER=%d，UNCERTAIN=%d，IGNORE=%d；保留各页需处理或待核查的变化。", len(b.Changes), len(pages), counts["TRIGGER"], counts["UNCERTAIN"], counts["IGNORE"])
	return out, mustJSON(out), nil
}
