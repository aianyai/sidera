package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
)

// askYes writes question and " [y/N] " to out and reads one line of answers. Only "y"
// and "yes", in any case, are a yes; anything else, and the end of the input, is a no.
// A command that asks more than once passes the same reader every time: a reader
// made for each question would swallow the lines that follow the one it was asked for.
func askYes(out io.Writer, answers *bufio.Reader, question string) (bool, error) {
	fmt.Fprint(out, question+" [y/N] ")
	answer, err := answers.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return true, nil
	}
	return false, nil
}

// noTerminalToAsk is the usage error of a command that asks before it changes anything
// and is run with no terminal to ask on and without --yes. Every such command says it in
// the same words.
func noTerminalToAsk(command string) error {
	return usage(command + " asks before it changes anything, and there is no terminal to ask on: run it in a terminal, or add --yes (--check only reports)")
}
