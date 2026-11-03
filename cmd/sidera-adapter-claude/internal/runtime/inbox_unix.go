//go:build !windows

package runtime

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"
)

func (osInbox) Write(ctx context.Context, socket string, data []byte) error {
	d := net.Dialer{Timeout: inboxTimeout}
	conn, err := d.DialContext(ctx, "unix", socket)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInboxUnreachable, withoutPath(err))
	}
	defer conn.Close()
	if err := conn.SetWriteDeadline(time.Now().Add(inboxTimeout)); err != nil {
		return fmt.Errorf("write to the inbox: %v", withoutPath(err))
	}
	if _, err := conn.Write(data); err != nil {
		return fmt.Errorf("write to the inbox: %v", withoutPath(err))
	}
	return nil
}

// withoutPath is the cause of a network error without the address it names.
func withoutPath(err error) error {
	var op *net.OpError
	if errors.As(err, &op) && op.Err != nil {
		return op.Err
	}
	return err
}
