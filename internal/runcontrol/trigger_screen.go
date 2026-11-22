package runcontrol

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

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
	// Reject ambiguous original identities before pagination; each page's
	// short references must map to exactly one frozen change across the batch.
	seen := map[string]bool{}
	for _, change := range b.Changes {
		if change.ID == "" || seen[change.ID] {
			return nil, f.Fail("INVALID_TRIGGER_INPUT", "change IDs must be nonempty and unique before numbering")
		}
		seen[change.ID] = true
	}
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

// validateTriggerIndices translates page-local model references into the
// existing persisted decision format. Numbers never reach downstream routing.
func validateTriggerIndices(raw []byte, changes []model.ControlChange) (triggerDecision, error) {
	var wire struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
		Indices  []int  `json:"relevant_indices"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&wire) != nil || decoder.Decode(new(any)) != io.EOF {
		return triggerDecision{}, f.Fail("INVALID_TRIGGER_OUTPUT", "invalid Trigger index JSON")
	}
	if strings.TrimSpace(wire.Reason) == "" || wire.Indices == nil {
		return triggerDecision{}, f.Fail("INVALID_TRIGGER_OUTPUT", "reason and relevant_indices are required")
	}
	if wire.Decision != "TRIGGER" && wire.Decision != "UNCERTAIN" && wire.Decision != "IGNORE" {
		return triggerDecision{}, f.Fail("INVALID_TRIGGER_OUTPUT", "decision must be TRIGGER, UNCERTAIN or IGNORE")
	}
	if wire.Decision == "IGNORE" && len(wire.Indices) != 0 {
		return triggerDecision{}, f.Fail("INVALID_TRIGGER_OUTPUT", "IGNORE requires relevant_indices=[]")
	}
	if wire.Decision != "IGNORE" && len(wire.Indices) == 0 {
		return triggerDecision{}, f.Fail("INVALID_TRIGGER_OUTPUT", wire.Decision+" requires at least one current-page index")
	}
	ids := make([]string, 0, len(wire.Indices))
	previous := 0
	for position, index := range wire.Indices {
		if index < 1 || index > len(changes) {
			return triggerDecision{}, f.Fail("INVALID_TRIGGER_OUTPUT", fmt.Sprintf("relevant_indices[%d]=%d is outside this page's selectable range [1,%d]; this page contains %d changes, and page.index/page.count are not selection indices", position, index, len(changes), len(changes)))
		}
		if index <= previous {
			return triggerDecision{}, f.Fail("INVALID_TRIGGER_OUTPUT", fmt.Sprintf("relevant_indices[%d]=%d must be greater than previous index %d; this page contains %d changes and indices must be unique and in input order", position, index, previous, len(changes)))
		}
		if changes[index-1].ID == "" {
			return triggerDecision{}, f.Fail("INVALID_TRIGGER_INPUT", "numbered change has no original ID")
		}
		ids = append(ids, changes[index-1].ID)
		previous = index
	}
	// Reuse persisted decision validation for decision/list semantics and ID
	// integrity. Historical stored relevant_change_ids remain readable.
	return validateTrigger(mustJSON(triggerDecision{Decision: wire.Decision, Reason: wire.Reason, IDs: ids}), changes)
}

func triggerCorrectionMessage(err error, count int) string {
	// Validator messages can include model-supplied decision values. Bound the
	// diagnostic rather than echoing an arbitrarily long rejected response.
	diagnostic := []rune(err.Error())
	if len(diagnostic) > 768 {
		diagnostic = append(diagnostic[:768], []rune("…")...)
	}
	return fmt.Sprintf("校验失败：%s。本页selection_count=%d，selection_bounds=[1,%d]；只能选择当前changes中实际存在的index，page.index/page.count是分页信息，不是可选编号上限。只输出decision=TRIGGER/UNCERTAIN/IGNORE,reason,relevant_indices；编号须为整数、按输入顺序且不重复。IGNORE必须空数组，其他判断必须非空；不得输出真实ID或额外字段。", string(diagnostic), count, count)
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
		decision, err := validateTriggerIndices([]byte(result.Output), page.changes)
		if err != nil {
			var correction map[string]any
			if err := json.Unmarshal(page.input, &correction); err != nil {
				return out, nil, err
			}
			correction["validation_error"] = triggerCorrectionMessage(err, len(page.changes))
			// The invalid model response can be arbitrarily large. Report only a
			// bounded validation diagnostic plus the actual current-page bounds.
			input := mustJSON(correction)
			if courseBatch(b) && b.Scope.Kind == "course" && len(input) > triggerPageBytes {
				return out, nil, f.Fail("TRIGGER_INPUT_TOO_LARGE", "trigger correction exceeds page budget")
			}
			result, err = s.Agents.Run(ctx, b.OwnerID, f.RunRequest{AgentID: agentID, Trigger: f.DataChanged, Input: input})
			if err != nil {
				return out, nil, err
			}
			decision, err = validateTriggerIndices([]byte(result.Output), page.changes)
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
