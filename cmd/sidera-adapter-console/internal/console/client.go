package console

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"time"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/agentview"
	"github.com/aianyai/sidera/sdk/go/termout"
)

var errNotRunning = errors.New("the console adapter is not running: enable it with `sidera adapter enable console` and run the daemon (--home and --instance say which node and instance)")

// invocation is a parsed client command: where the adapter is and the first
// request the command sends. Every command is that request, and `new` and `recv`
// follow it up (see execute).
type invocation struct {
	cmd  string
	home string
	// homeGiven says that the data directory was named by --home and not left to SIDERA_HOME
	// and the default; see command.
	homeGiven bool
	instance  string
	req       request
	// wait is how long `recv` asks the daemon to wait, and timeout how long `new`
	// waits for the reply to its control signal.
	wait    time.Duration
	timeout time.Duration
	// pollEvery is how often `new` asks for the reply.
	pollEvery time.Duration
}

const (
	defaultNewTimeout = 120 * time.Second
	defaultPollEvery  = time.Second
)

func runClient(args []string, env Env) int {
	inv, err := parseCommand(args, env)
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprint(env.Stdout, program(env).Text(env.streams().Out()))
		return exitOK
	}
	if err != nil {
		fmt.Fprintln(env.Stderr, "sidera-adapter-console:", err)
		fmt.Fprintln(env.Stderr, "run `sidera-adapter-console help` for the commands")
		return exitUsage
	}
	c := newClient(env, stateDir(inv.home, inv.instance), dialLocal)
	ctx, stop := signalContext()
	defer stop()
	return c.execute(ctx, inv)
}

// parseCommand turns the arguments into the request the command sends. It reads
// no file but --body-file and calls nothing.
func parseCommand(args []string, env Env) (*invocation, error) {
	inv := &invocation{cmd: args[0], pollEvery: defaultPollEvery}
	fs := flag.NewFlagSet(inv.cmd, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var home string
	fs.StringVar(&home, "home", "", "")
	fs.StringVar(&inv.instance, "instance", AdapterID, "")
	var (
		preset, model, effort  string
		body, bodyFile, to     string
		node, cursor           string
		waitS, timeoutS, limit int
	)
	positionals := 0
	switch inv.cmd {
	case "new":
		fs.StringVar(&preset, "preset", "", "")
		fs.StringVar(&model, "model", "", "")
		fs.StringVar(&effort, "effort", "", "")
		fs.StringVar(&body, "body", "", "")
		fs.StringVar(&bodyFile, "body-file", "", "")
		fs.IntVar(&timeoutS, "timeout", int(defaultNewTimeout/time.Second), "")
		positionals = 1
	case "send":
		fs.StringVar(&to, "to", "", "")
		fs.StringVar(&body, "body", "", "")
		fs.StringVar(&bodyFile, "body-file", "", "")
	case "reply":
		fs.StringVar(&to, "to", "", "")
		fs.StringVar(&body, "body", "", "")
		fs.StringVar(&bodyFile, "body-file", "", "")
		positionals = 1
	case "status":
		positionals = 1
	case "recv":
		fs.IntVar(&waitS, "wait", 0, "")
	case "directory":
		fs.StringVar(&node, "node", "", "")
		fs.StringVar(&cursor, "cursor", "", "")
		fs.IntVar(&limit, "limit", 0, "")
	case "whoami", "peers":
	default:
		return nil, fmt.Errorf("unknown command %q", inv.cmd)
	}
	rest, err := parseFlags(fs, args[1:], positionals)
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if inv.home, err = resolveHome(home, env.Getenv); err != nil {
		return nil, err
	}
	inv.homeGiven = set["home"]

	switch inv.cmd {
	case "whoami":
		inv.req = request{Op: opWhoami}
	case "peers":
		inv.req = request{Op: opPeers}
	case "directory":
		if set["limit"] && (limit < 1 || limit > adapterproto.MaxDirectoryLimit) {
			return nil, fmt.Errorf("--limit is 1 to %d", adapterproto.MaxDirectoryLimit)
		}
		inv.req = request{Op: opDirectory, Directory: &adapterproto.DirectoryParams{Node: node, Cursor: cursor, Limit: limit}}
	case "status":
		inv.req = request{Op: opStatus, Status: &adapterproto.MessageStatusParams{MessageID: adapterproto.MessageID(rest[0])}}
	case "recv":
		if waitS < 0 || waitS > adapterproto.MaxWaitSeconds {
			return nil, fmt.Errorf("--wait is 0 to %d seconds", adapterproto.MaxWaitSeconds)
		}
		inv.wait = time.Duration(waitS) * time.Second
		inv.req = request{Op: opRecv, Recv: &adapterproto.RecvParams{WaitS: &waitS}}
	case "send", "reply", "new":
		text, err := readBody(set["body"], body, set["body-file"], bodyFile, inv.cmd == "new", env.Stdin)
		if err != nil {
			return nil, err
		}
		p := &adapterproto.SendParams{Body: text}
		if text != "" {
			p.ContentType = "text/plain"
		}
		switch inv.cmd {
		case "send":
			if to == "" {
				return nil, errors.New("send needs --to")
			}
			p.To = addresses(to)
		case "reply":
			p.ReplyTo = adapterproto.MessageID(rest[0])
			if to != "" {
				p.To = addresses(to)
			}
		case "new":
			if preset == "" {
				return nil, errors.New("new needs --preset")
			}
			if timeoutS <= 0 {
				return nil, errors.New("--timeout is a positive number of seconds")
			}
			inv.timeout = time.Duration(timeoutS) * time.Second
			p.To = []adapterproto.Address{adapterproto.Address(rest[0])}
			p.Control = &adapterproto.Control{Signal: adapterproto.SignalSessionCreate, Params: &adapterproto.ControlParams{
				Preset: preset, Model: model, Effort: adapterproto.Effort(effort)}}
		}
		inv.req = request{Op: opSend, Send: p}
	}
	return inv, nil
}

// parseFlags parses flags on either side of the positional arguments, which the
// examples in the usage text rely on, and requires exactly n of them.
func parseFlags(fs *flag.FlagSet, args []string, n int) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			break
		}
		positional = append(positional, fs.Arg(0))
		args = fs.Args()[1:]
	}
	if len(positional) != n {
		return nil, fmt.Errorf("%s takes %d argument(s), got %d", fs.Name(), n, len(positional))
	}
	return positional, nil
}

// readBody returns the text of --body or of --body-file ("-" is standard input).
// A message has a body, so one source is required, except for `new`, where no body
// means a session without an initial message.
func readBody(hasBody bool, body string, hasFile bool, file string, optional bool, stdin io.Reader) (string, error) {
	switch {
	case hasBody && hasFile:
		return "", errors.New("give --body or --body-file, not both")
	case hasBody:
		return body, nil
	case hasFile:
		var data []byte
		var err error
		if file == "-" {
			data, err = io.ReadAll(stdin)
		} else {
			data, err = os.ReadFile(file)
		}
		if err != nil {
			return "", fmt.Errorf("reading the body: %w", err)
		}
		return string(data), nil
	case optional:
		return "", nil
	}
	return "", errors.New("give the text with --body or --body-file")
}

func addresses(list string) []adapterproto.Address {
	var out []adapterproto.Address
	for _, a := range strings.Split(list, ",") {
		out = append(out, adapterproto.Address(strings.TrimSpace(a)))
	}
	return out
}

// client is the client role: it sends requests to the adapter of one instance and
// writes what comes back.
type client struct {
	env Env
	// out is what the commands print, in the form their output has on env.Stdout. The
	// error lines go to env.Stderr as they are: a line of the form `<program>: <message>`
	// is the same on every stream.
	out  *termout.Stream
	dir  string
	dial func(ctx context.Context, stateDir string) (net.Conn, error)
}

// newClient opens the streams of env once; see [termout.Open].
func newClient(env Env, dir string, dial func(ctx context.Context, stateDir string) (net.Conn, error)) *client {
	return &client{env: env, out: env.streams().Out(), dir: dir, dial: dial}
}

// view is the layout the console shares with `sidera` for the results of the endpoint's
// view of the network, on the standard output stream. The commands it names in its hints
// are the console's own, spelled for the invocation they are printed for.
func (c *client) view(inv *invocation) agentview.View {
	return agentview.View{Out: c.out, Command: inv.command}
}

// command spells a command of this program as a person types it, for the node and the
// instance this invocation acted on. The flags that select them go after the command name,
// where the program takes its flags: --home when the invocation named the directory (what
// SIDERA_HOME says is in the environment of the next command too), --instance when it is not
// the default. The words that can hold any text arrive quoted, and so are the values added.
func (inv *invocation) command(args ...string) string {
	words := []string{ProgramName, args[0]}
	if inv.homeGiven {
		words = append(words, "--home", termout.ShellWord(runtime.GOOS, inv.home))
	}
	if inv.instance != AdapterID {
		words = append(words, "--instance", termout.ShellWord(runtime.GOOS, inv.instance))
	}
	return strings.Join(append(words, args[1:]...), " ")
}

// call makes one request on a connection of its own. A failure the adapter or the
// daemon reports comes back as an error.
func (c *client) call(ctx context.Context, req request) (response, error) {
	conn, err := c.dial(ctx, c.dir)
	if err != nil {
		return response{}, err
	}
	defer conn.Close()
	deadline := time.Now().Add(requestTimeout)
	_ = conn.SetDeadline(deadline)
	// A cancelled context (Ctrl-C) ends the wait for the answer.
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()
	line, err := json.Marshal(req)
	if err != nil {
		return response{}, err
	}
	if _, err := conn.Write(append(line, '\n')); err != nil {
		return response{}, fmt.Errorf("sending the request: %w", err)
	}
	reply, err := bufio.NewReader(io.LimitReader(conn, maxLine+1)).ReadBytes('\n')
	if err != nil {
		if ctx.Err() != nil {
			return response{}, ctx.Err()
		}
		return response{}, fmt.Errorf("reading the answer: %w", err)
	}
	var resp response
	if err := json.Unmarshal(reply, &resp); err != nil {
		return response{}, fmt.Errorf("decoding the answer: %w", err)
	}
	if resp.Error != nil {
		return resp, resp.Error
	}
	if !resp.answers(req.Op) {
		return response{}, fmt.Errorf("the adapter's answer to %s has no result", req.Op)
	}
	return resp, nil
}

// answers reports whether a successful response carries the result of op.
func (r response) answers(op string) bool {
	switch op {
	case opWhoami:
		return r.Endpoint != ""
	case opSend:
		return r.Send != nil
	case opStatus:
		return r.Status != nil
	case opRecv:
		return r.Recv != nil
	case opAck:
		return r.Ack != nil
	case opDirectory:
		return r.Directory != nil
	case opPeers:
		return r.Peers != nil
	}
	return false
}

func (f *failure) Error() string {
	if f.Code != 0 {
		return fmt.Sprintf("%s (code %d)", f.Message, f.Code)
	}
	return f.Message
}

func (c *client) errorf(err error) int {
	fmt.Fprintln(c.env.Stderr, "sidera-adapter-console:", err)
	return exitFailed
}

func (c *client) execute(ctx context.Context, inv *invocation) int {
	switch inv.cmd {
	case "whoami":
		resp, err := c.call(ctx, inv.req)
		if err != nil {
			return c.errorf(err)
		}
		fmt.Fprint(c.out, c.out.Block([]termout.Field{{Label: "Address", Value: address(resp.Node, resp.Endpoint)}}))
	case "send", "reply":
		resp, err := c.call(ctx, inv.req)
		if err != nil {
			return c.errorf(err)
		}
		kind := agentview.SentMessage
		if inv.cmd == "reply" {
			kind = agentview.SentReply
		}
		to := make([]string, len(inv.req.Send.To))
		for i, a := range inv.req.Send.To {
			to[i] = string(a)
		}
		fmt.Fprint(c.out, c.view(inv).Sent(kind, resp.Send.MessageID, to, false))
	case "status":
		resp, err := c.call(ctx, inv.req)
		if err != nil {
			return c.errorf(err)
		}
		c.printStatus(inv, *resp.Status)
	case "directory":
		return c.directory(ctx, inv)
	case "peers":
		return c.peers(ctx, inv)
	case "recv":
		return c.receive(ctx, inv)
	case "new":
		return c.create(ctx, inv)
	}
	return exitOK
}

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt)
}
