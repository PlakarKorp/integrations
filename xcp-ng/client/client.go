// Package client exposes XAPI classes as typed services on top of an
// *xapi.Client. Method names follow XAPI (get_record -> GetRecord), so the
// XAPI docs map 1:1 to the code.
package client

import "context"

// RPC is the part of *xapi.Client the services use.
type RPC interface {
	Call(ctx context.Context, method string, out any, params ...any) error
}

type Client struct {
	VM VMService
}

func New(rpc RPC) *Client {
	return &Client{
		VM: VMService{objects[VMRecord]{rpc: rpc, class: "VM"}},
	}
}

// objects holds the calls every XAPI class supports. Services embed it and
// add their own methods.
type objects[R any] struct {
	rpc   RPC
	class string // "VM", "VDI", ...
}

func (o objects[R]) GetRecord(ctx context.Context, ref Ref[R]) (R, error) {
	var record R
	err := o.rpc.Call(ctx, o.class+".get_record", &record, ref)
	return record, err
}
