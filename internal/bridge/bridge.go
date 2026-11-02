// Package bridge defines a small in-process port for future module communication.
// It intentionally provides no queue, delivery guarantee or Agent implementation.
package bridge

import (
	"context"
	"encoding/json"
	"errors"
)

const Version = "v1"

var ErrUnsupported = errors.New("unsupported bridge operation")
var ErrInvalid = errors.New("invalid bridge request")

// Request is constructed by trusted application code; OwnerID never comes from a model or HTTP body.
type Request struct {
	Version   string
	OwnerID   string
	Operation string
	Payload   json.RawMessage
}

type Reply struct {
	Version   string          `json:"version"`
	Operation string          `json:"operation"`
	Payload   json.RawMessage `json:"payload"`
}

// Port is an explicit dependency a module may receive, rather than a global bus.
type Port interface {
	Call(context.Context, Request) (Reply, error)
}

// Demo only echoes a small payload. It does not contact an Agent or send messages.
type Demo struct{}

func (Demo) Call(ctx context.Context, req Request) (Reply, error) {
	if err := ctx.Err(); err != nil {
		return Reply{}, err
	}
	if req.OwnerID == "" || req.Version != Version || len(req.Payload) > 4096 || !json.Valid(req.Payload) {
		return Reply{}, ErrInvalid
	}
	if req.Operation != "demo.echo" {
		return Reply{}, ErrUnsupported
	}
	return Reply{Version: Version, Operation: req.Operation, Payload: append(json.RawMessage(nil), req.Payload...)}, nil
}

var _ Port = Demo{}
