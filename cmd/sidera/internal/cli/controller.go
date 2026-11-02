package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/aianyai/sidera/cmd/sidera/internal/controller"
	"github.com/aianyai/sidera/internal/node/agentapi"
	"github.com/aianyai/sidera/internal/node/manageapi"
)

// agentCaller is what an agent command calls the node through.
type agentCaller interface {
	Call(ctx context.Context, method string, in any) (any, error)
}

// attachingClient is the Agent client of an agent command. It attaches the session
// the command runs in (attach) just before the command first calls the node, so a
// command refused for its arguments attaches nothing. Every agent command makes one
// call.
type attachingClient struct {
	*agentapi.Client
	attach func(context.Context) error
}

func (a *attachingClient) Call(ctx context.Context, method string, in any) (any, error) {
	if err := a.attach(ctx); err != nil {
		return nil, err
	}
	return a.Client.Call(ctx, method, in)
}

// attachController makes an agent command run inside a session a person opened
// work without SIDERA_TOKEN: it attaches the session, which is safe to repeat,
// and gives the Agent client the token it gets, in memory only. A process that
// has a credential, or is in no such session, is left as it is. Each command
// attaches once, so a session that only uses the command line has the wake
// information of its latest command and no heartbeat between commands; its
// endpoint is closed a queue time after the last one when nothing waits on it.
func (c *command) attachController(ctx context.Context, home string, agent *agentapi.Client) error {
	req, err := controller.RequestFromEnv(c.Getenv, os.Getwd, "")
	if errors.Is(err, controller.ErrNotController) {
		return nil
	}
	if err != nil {
		return err
	}
	manage, err := manageapi.NewClient(manageapi.ClientConfig{Home: home})
	if err != nil {
		return err
	}
	defer manage.Close()
	att, err := controller.Attach(ctx, manage, req)
	if err != nil {
		return err
	}
	agent.SetToken(att.Token)
	if att.Wake.State != "ready" {
		fmt.Fprintln(c.Stderr, controller.Notice(att.Wake))
	}
	return nil
}
