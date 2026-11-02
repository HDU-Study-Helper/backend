package app

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestRunWithoutDWSCancelsPromptly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	a := &App{config: Config{HTTPAddr: "127.0.0.1:0"}, handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })}
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown waited on a nonexistent worker")
	}
}
