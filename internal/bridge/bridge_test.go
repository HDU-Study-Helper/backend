package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestDemoProtocol(t *testing.T) {
	payload := json.RawMessage(`{"text":"demo"}`)
	result, err := (Demo{}).Call(context.Background(), Request{Version: Version, OwnerID: "user-a", Operation: "demo.echo", Payload: payload})
	if err != nil || string(result.Payload) != string(payload) {
		t.Fatalf("reply=%v err=%v", result, err)
	}
	payload[0] = 'x'
	if result.Payload[0] != '{' {
		t.Fatal("reply aliases caller memory")
	}
	for _, req := range []Request{
		{Version: Version, Operation: "demo.echo", Payload: json.RawMessage(`{}`)},
		{Version: "v2", OwnerID: "u", Operation: "demo.echo", Payload: json.RawMessage(`{}`)},
		{Version: Version, OwnerID: "u", Operation: "demo.echo", Payload: json.RawMessage(`bad`)},
	} {
		if _, err := (Demo{}).Call(context.Background(), req); !errors.Is(err, ErrInvalid) {
			t.Fatalf("want validation error: %v", err)
		}
	}
	if _, err := (Demo{}).Call(context.Background(), Request{Version: Version, OwnerID: "u", Operation: "agent.run", Payload: json.RawMessage(`{}`)}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("want unsupported: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (Demo{}).Call(ctx, Request{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
