package attached

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strconv"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// The keys of a wake, the names of the environment variables of a Claude Code
// session that the process attaching it copies.
const (
	WakeSocket = "CLAUDE_CODE_MESSAGING_SOCKET"
	WakeToken  = "CLAUDE_CODE_MESSAGING_TOKEN"
	WakePID    = "CLAUDE_PID"
)

// Wake is what the adapter needs to deliver a prompt to a session someone else
// runs. The socket and the token are secrets: a Wake never prints them, so that
// a log line or an error that carries one cannot leak them.
type Wake struct {
	// Socket is the session's inbox socket; empty when the wake has none.
	Socket string
	// Token is the inbox's authentication token; empty when the wake has none.
	// Windows requires it and macOS and Linux do not check it.
	Token string
	// PID is the Claude Code process; zero when the wake has none.
	PID int
}

// ParseWake reads the wake the daemon holds. A key that is not valid is named
// by its key, never by its value. Keys the adapter does not know are ignored.
func ParseWake(info adapterproto.WakeInfo) (Wake, error) {
	w := Wake{Socket: info[WakeSocket], Token: info[WakeToken]}
	if v, ok := info[WakePID]; ok {
		pid, err := strconv.Atoi(v)
		if err != nil || pid <= 0 {
			return Wake{}, fmt.Errorf("the wake key %s is not a process id", WakePID)
		}
		w.PID = pid
	}
	return w, nil
}

// Problem says why the wake cannot deliver a prompt on the operating system goos,
// empty when it can. Claude Code on Windows drops a message that no
// authentication line precedes without a word, which a delivery cannot tell from
// success, so such a wake is refused before anything is written.
func (w Wake) Problem(goos string) string {
	switch {
	case w.Socket == "":
		return "the wake carries no inbox socket (" + WakeSocket + ")"
	case w.Token == "" && goos == "windows":
		return "the wake carries no token (" + WakeToken + "), which Claude Code on Windows requires: a message without it is dropped unanswered"
	}
	return ""
}

const redacted = "wake{redacted}"

// Format and LogValue hide every secret in formatted output and in logs, whatever
// the verb or the handler.
func (Wake) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, redacted) }
func (Wake) LogValue() slog.Value       { return slog.StringValue(redacted) }

// Lines is what is written to the inbox socket to deliver a prompt: the
// authentication line when the wake has a token, then one user message whose
// uuid becomes the uuid of the session record the message gives rise to, so
// that the record is found by the prompt's attempt. The runtime answers
// nothing on the socket.
func (w Wake) Lines(uuid, text string) ([]byte, error) {
	var out bytes.Buffer
	line := func(v any) error {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		out.Write(b)
		out.WriteByte('\n')
		return nil
	}
	if w.Token != "" {
		if err := line(struct {
			Type  string `json:"type"`
			Token string `json:"token"`
		}{"auth", w.Token}); err != nil {
			return nil, err
		}
	}
	type message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	if err := line(struct {
		Type    string  `json:"type"`
		Message message `json:"message"`
		UUID    string  `json:"uuid"`
	}{"user", message{"user", text}, uuid}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
