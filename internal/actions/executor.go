// Package actions provides host-registered local business tools and an explicitly driven due-job executor.
package actions

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	f "academic/agent/framework"
	"academic/backend/internal/model"
)

var scheduledPermissions = map[string]string{"notice_list": "notice.read", "public_todo_list": "todo.read", "schedule_get": "schedule.read", "public_todo_upsert": "todo.write", "public_todo_complete": "todo.write"}

type AgentReader interface {
	GetAgent(context.Context, f.Scope) (f.AgentInstance, error)
}
type SafeToolRunner func(context.Context, model.ClaimedAction) (string, error)
type WorkerRunner func(context.Context, model.ClaimedAction) (string, error)
type Executor struct {
	Repo   model.ActionRepository
	Agents AgentReader
	Tools  SafeToolRunner
	Worker WorkerRunner
}

func (x Executor) RunOne(ctx context.Context) (bool, error) {
	if x.Repo == nil || x.Agents == nil {
		return false, model.ErrUnsupported
	}
	j, err := x.Repo.ClaimAction(ctx)
	if errors.Is(err, model.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	ref, runErr := x.execute(ctx, &j)
	status := "succeeded"
	if runErr != nil {
		status = "failed"
		ref = "execution_failed"
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err = x.Repo.FinishAction(cleanup, j, status, ref); err != nil {
		return true, err
	}
	return true, runErr
}
func (x Executor) execute(ctx context.Context, j *model.ClaimedAction) (string, error) {
	in := j.Input
	if err := in.Validate("job"); err != nil {
		return "", err
	}
	a, err := x.Agents.GetAgent(ctx, f.Scope{OwnerID: j.OwnerID, AgentID: j.AgentID})
	if err != nil {
		return "", err
	}
	if a.OwnerID != j.OwnerID || a.ID != j.AgentID || a.Status != "enabled" {
		return "", model.ErrUnauthenticated
	}
	granted := func(name, perm string) bool {
		return slices.Contains(a.EnabledTools, name) && slices.Contains(a.Permissions, perm)
	}
	if in.Origin == "notice_create" {
		if in.Kind != "Notice" || !granted("notice_create", "notice.write") {
			return "", model.ErrUnauthenticated
		}
	} else if !granted("planned_job_create", "job.write") {
		return "", model.ErrUnauthenticated
	}
	switch in.Kind {
	case "Notice":
		if !granted("notice_create", "notice.write") {
			return "", model.ErrUnauthenticated
		}
		return "", nil
	case "Worker":
		if !strings.EqualFold(a.Type, "worker") || x.Worker == nil {
			return "", model.ErrUnsupported
		}
		ref, err := x.Worker(ctx, *j)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(ref) == "" || len(ref) > 480 {
			return "", model.ErrInternal
		}
		return "run:" + ref, nil
	case "ToolCall":
		perm := scheduledPermissions[in.Tool]
		if perm == "" || !granted(in.Tool, perm) {
			return "", model.ErrUnauthenticated
		}
		if in.Tool == "public_todo_upsert" || in.Tool == "public_todo_complete" {
			scope := model.ActionScope{OwnerID: j.OwnerID, AgentID: j.AgentID, RunID: j.RunID}
			var item model.PublicAction
			if in.Tool == "public_todo_upsert" {
				arg, e := in.ScheduledTodo(j.ID)
				if e != nil {
					return "", e
				}
				item, err = x.Repo.PutAction(ctx, scope, "todo", arg)
			} else {
				var arg struct{ ID string }
				if json.Unmarshal(in.Args, &arg) != nil {
					return "", model.ErrInvalidArgument
				}
				item, err = x.Repo.ChangeAction(ctx, scope, "todo", arg.ID)
			}
			if err != nil {
				return "", err
			}
			if item.ID == "" || item.Kind != "todo" {
				return "", model.ErrInternal
			}
			j.Result = raw(obj{"id": item.ID, "kind": item.Kind, "status": item.Status, "updated_at": item.Updated})
			return "todo:" + item.ID, nil
		}
		if in.Tool == "schedule_get" {
			if x.Tools == nil {
				return "", model.ErrUnsupported
			}
			ref, err := x.Tools(ctx, *j)
			if err == nil && (strings.TrimSpace(ref) == "" || len(ref) > 512) {
				return "", model.ErrInternal
			}
			return ref, err
		}
		kind := "todo"
		if in.Tool == "notice_list" {
			kind = "notice"
		}
		items, err := x.Repo.ListActions(ctx, j.OwnerID, kind, 20, 0)
		if err != nil {
			return "", err
		}
		// Bound the scheduled read result without silently chopping an individual row.
		rows := make([]map[string]string, 0, len(items))
		for _, item := range items {
			rows = append(rows, map[string]string{"id": item.ID, "kind": item.Kind, "title": item.Input.Title, "status": item.Status})
		}
		j.Result, err = json.Marshal(obj{"items": rows, "limit": 20, "projection": "id,kind,title,status"})
		if err != nil || len(j.Result) > 32768 {
			return "", model.ErrInternal
		}
		return "job:" + j.ID, nil
	}
	return "", model.ErrInvalidArgument
}
