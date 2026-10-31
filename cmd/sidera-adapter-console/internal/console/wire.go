package console

import (
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// The local protocol between the client role and the adapter role: one request per
// connection, one line of JSON each way. A request names an operation and carries
// the parameters of the endpoint.* call it becomes; the adapter fills in the
// endpoint, which is its own, so the client never names one.
const (
	opWhoami = "whoami"
	opSend   = "send"
	opStatus = "status"
	opRecv   = "recv"
	opAck    = "ack"
)

// maxLine bounds one line of the local protocol: a message body is at most 64 KiB,
// and JSON escaping at most six times that.
const maxLine = 1 << 20

type request struct {
	Op     string                            `json:"op"`
	Send   *adapterproto.SendParams          `json:"send,omitempty"`
	Status *adapterproto.MessageStatusParams `json:"status,omitempty"`
	Recv   *adapterproto.RecvParams          `json:"recv,omitempty"`
	Ack    *adapterproto.AckParams           `json:"ack,omitempty"`
}

// response is the answer: an error, or the result of the call that was made. Node
// comes with every success so the client can write addresses.
type response struct {
	Error    *failure                          `json:"error,omitempty"`
	Node     string                            `json:"node,omitempty"`
	Endpoint adapterproto.EndpointRef          `json:"endpoint,omitempty"`
	Send     *adapterproto.SendResult          `json:"send,omitempty"`
	Status   *adapterproto.MessageStatusResult `json:"status,omitempty"`
	Recv     *adapterproto.RecvResult          `json:"recv,omitempty"`
	Ack      *adapterproto.AckResult           `json:"ack,omitempty"`
}

// failure is what went wrong; Code is the protocol's when the daemon refused.
type failure struct {
	Code    int    `json:"code,omitempty"`
	Message string `json:"message"`
}

// address is the text form of an endpoint on this node, `<node>/<endpoint>`.
func address(node string, endpoint adapterproto.EndpointRef) string {
	return node + "/" + string(endpoint)
}
