package app

import (
	"context"
	"errors"
	"time"

	"academic/backend/internal/model"
)

type CollectionResult struct {
	RoundID string                `json:"round_id"`
	Results []model.RefreshResult `json:"results"`
	Failed  bool                  `json:"failed"`
}

// RunCollectionOnce collects a bounded complete pass without HTTP, timers or Agent
// workers. Stop the normal API before invoking this explicit experiment entry.
func (a *App) RunCollectionOnce(ctx context.Context) (out CollectionResult, err error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	ctx, _ = model.WithRefreshReport(ctx)
	out.Results = []model.RefreshResult{}
	round, e := a.control.StartRefresh(ctx)
	if e != nil {
		return out, e
	}
	out.RoundID = round
	ctx = model.WithControlRound(ctx, round)
	defer func() {
		out.Results = model.RefreshSnapshot(ctx)
		out.Failed = err != nil || ctx.Err() != nil
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer stop()
		err = errors.Join(err, a.control.Store.FinishRound(cleanup, round, out.Results, out.Failed))
		err = errors.Join(err, a.control.Store.Seal(cleanup))
		out.Failed = out.Failed || err != nil
	}()
	for _, source := range []string{"chaoxing", "mooc", "ucampus"} {
		model.InitRefreshSource(ctx, source, a.platforms != nil)
	}
	model.InitRefreshSource(ctx, "dws", a.dws != nil)
	if a.platforms != nil {
		err = a.platforms.CollectOnce(ctx)
	}
	if a.dws != nil && ctx.Err() == nil {
		if a.dws.Poll(ctx) {
			err = errors.Join(err, errors.New("DWS collection partially failed"))
		}
	}
	if ctx.Err() != nil {
		err = errors.Join(err, ctx.Err())
	}
	return out, err
}
