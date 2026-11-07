package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
)

// askDefaultNo asks a question whose answer, when only Enter is pressed, is no: "Update?"
// is asked as "Update? [y/N]". A command that changes something the person has not yet
// looked at asks this way.
func askDefaultNo(out io.Writer, answers *bufio.Reader, question string) (bool, error) {
	return ask(out, answers, question, false)
}

// askDefaultYes asks a question whose answer, when only Enter is pressed, is yes: "write a?" is
// asked as "write a? [Y/n]". The person ran the command to be asked these questions, so
// accepting what is listed is the usual answer.
func askDefaultYes(out io.Writer, answers *bufio.Reader, question string) (bool, error) {
	return ask(out, answers, question, true)
}

// ask writes question and the choices to out and reads one line of answer. "y" and
// "yes", in any case, are a yes; "n" and "no" are a no; an empty line (Enter, spaces)
// is the default; any other text is a no, so a mistyped answer never accepts a change.
// The end of the input is a no whatever the default is, because it is not an answer: a
// script, a closed pipe or a dropped terminal never accepts anything. A last line that
// has no newline before the end of the input is still an answer.
// A command that asks more than once passes the same reader every time: a reader
// made for each question would swallow the lines that follow the one it was asked for.
func ask(out io.Writer, answers *bufio.Reader, question string, defaultYes bool) (bool, error) {
	choices := " [y/N] "
	if defaultYes {
		choices = " [Y/n] "
	}
	fmt.Fprint(out, question+choices)
	answer, err := answers.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	switch {
	case answer == "" && err != nil:
		return false, nil
	case answer == "":
		return defaultYes, nil
	}
	return answer == "y" || answer == "yes", nil
}

// noTerminalToAsk is the usage error of a command that asks before it changes anything
// and is run with no terminal to ask on and without --yes. Every such command says it in
// the same words.
func noTerminalToAsk(command string) error {
	return usage(command + " asks before it changes anything, and there is no terminal to ask on: run it in a terminal, or add --yes (--check only reports)")
}
