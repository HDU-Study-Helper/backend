package dws

import (
	"context"
	"sync"
	"testing"
	"time"

	"academic/backend/internal/model"
)

type disconnectRepo struct {
	model.DWSRepository
	mu     sync.Mutex
	fenced map[model.DWSScope]bool
}

func (r *disconnectRepo) Disconnect(_ context.Context, scope model.DWSScope) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fenced[scope] = true
	return nil
}
func TestDisconnectFencesThenCancelsOnlyExactScopeAndWaits(t *testing.T) {
	a := model.DWSScope{UserID: "a", ConnectionID: "same"}
	b := model.DWSScope{UserID: "b", ConnectionID: "same"}
	repo := &disconnectRepo{fenced: map[model.DWSScope]bool{}}
	aCtx, aCancel := context.WithCancel(context.Background())
	defer aCancel()
	bCtx, bCancel := context.WithCancel(context.Background())
	defer bCancel()
	aDone := make(chan struct{})
	bDone := make(chan struct{})
	s := &Service{repo: repo, active: map[model.DWSScope]*activeOperation{a: {cancel: aCancel, done: aDone}, b: {cancel: bCancel, done: bDone}}}
	observed := make(chan bool, 1)
	allowCleanup := make(chan struct{})
	go func() {
		<-aCtx.Done()
		repo.mu.Lock()
		fenced := repo.fenced[a]
		repo.mu.Unlock()
		observed <- fenced
		<-allowCleanup
		close(aDone)
	}()
	returned := make(chan error, 1)
	go func() { returned <- s.Disconnect(context.Background(), a) }()
	select {
	case fenced := <-observed:
		if !fenced {
			t.Fatal("cancel preceded database fencing")
		}
	case <-time.After(time.Second):
		t.Fatal("active operation not cancelled")
	}
	select {
	case <-returned:
		t.Fatal("disconnect returned before cleanup")
	default:
	}
	select {
	case <-bCtx.Done():
		t.Fatal("other owner's connection cancelled")
	default:
	}
	close(allowCleanup)
	select {
	case e := <-returned:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("disconnect did not finish after cleanup")
	}
}
