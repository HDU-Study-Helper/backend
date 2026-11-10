package dws

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"academic/backend/internal/model"
)

// AvailableGroupsPage contains one verified upstream page, never the allowlist.
type AvailableGroupsPage struct {
	Items      []model.Group `json:"items"`
	HasMore    bool          `json:"has_more"`
	NextCursor string        `json:"next_cursor,omitempty"`
}

func (s *Service) AvailableGroups(ctx context.Context, scope model.DWSScope, cursor string) (AvailableGroupsPage, error) {
	page := AvailableGroupsPage{Items: []model.Group{}}
	if len(cursor) > 4096 || strings.ContainsAny(cursor, "\r\n\x00") {
		return page, model.ErrInvalidArgument
	}
	err := s.withWorkspace(ctx, scope, "", func(ctx context.Context, c model.DWSConnection, _ model.Lease, root string) error {
		if c.State != "active" {
			return fmt.Errorf("auth_required")
		}
		args := []string{"chat", "+my-groups", "--limit", "100", "--profile", c.Profile, "-f", "json"}
		if cursor != "" {
			args = append(args, "--cursor", cursor)
		}
		data, err := s.runner.execute(ctx, root, args, "")
		if err != nil {
			return err
		}
		page, err = parseAvailableGroups(data, cursor)
		return err
	})
	if err != nil {
		return AvailableGroupsPage{}, err
	}
	return page, nil
}

func parseAvailableGroups(data []byte, cursor string) (AvailableGroupsPage, error) {
	fail := fmt.Errorf("parse_failed")
	var raw struct {
		Groups *[]struct {
			ID   string `json:"conversationId"`
			Name string `json:"name"`
		} `json:"groups"`
		Count    *int              `json:"count"`
		Known    *bool             `json:"paginationKnown"`
		More     *bool             `json:"hasMore"`
		Complete *bool             `json:"complete"`
		Next     json.RawMessage   `json:"nextCursor"`
		Partial  bool              `json:"partial"`
		Failed   int               `json:"failedCount"`
		Failures []json.RawMessage `json:"failures"`
		Success  *bool             `json:"success"`
	}
	page := AvailableGroupsPage{Items: []model.Group{}}
	if json.Unmarshal(data, &raw) != nil || raw.Groups == nil || raw.Count == nil || (raw.Known != nil && !*raw.Known) || raw.More == nil || raw.Complete == nil || raw.Partial || raw.Failed != 0 || len(raw.Failures) > 0 || (raw.Success != nil && !*raw.Success) {
		return page, fail
	}
	if len(*raw.Groups) > 100 || *raw.Count != len(*raw.Groups) || *raw.Complete == *raw.More {
		return page, fail
	}
	seen := map[string]bool{}
	for _, g := range *raw.Groups {
		if g.ID == "" || len(g.ID) > 512 || len(g.Name) > 512 || strings.ContainsAny(g.ID, "\r\n\x00") || seen[g.ID] {
			return page, fail
		}
		seen[g.ID] = true
		page.Items = append(page.Items, model.Group{ID: g.ID, Name: g.Name})
	}
	page.HasMore = *raw.More
	if page.HasMore {
		var next string
		if json.Unmarshal(raw.Next, &next) != nil {
			var number json.Number
			if json.Unmarshal(raw.Next, &number) != nil {
				return page, fail
			}
			next = number.String()
		}
		if next == "" || next == "0" || next == cursor || len(next) > 4096 || strings.ContainsAny(next, "\r\n\x00") {
			return page, fail
		}
		page.NextCursor = next
	}
	return page, nil
}
