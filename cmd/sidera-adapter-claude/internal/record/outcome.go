package record

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// Outcome is how a turn process ended, as far as its output stream says.
type Outcome int

const (
	// NoResult: the stream holds no result line: the process was cut off before
	// the runtime reported an end.
	NoResult Outcome = iota
	// Succeeded: the last result line is a success.
	Succeeded
	// Errored: the last result line is an error of any kind.
	Errored
)

var resultMarker = []byte(`"type":"result"`)

// LastResult reads the output stream of a turn process (stream-json, one object
// per line) and returns the outcome of its last result line. A process can write
// several: resuming a session may run a continuation first, and background
// work can wake the session again, so the first result does not end the process
// and the last one is the end of the turn. A result is a success when its
// subtype is "success" and it is not marked as an error.
func LastResult(r io.Reader) (Outcome, error) {
	br := bufio.NewReaderSize(r, 64<<10)
	out := NoResult
	for {
		line, err := readLine(br)
		if len(line) > 0 && bytes.Contains(line, resultMarker) {
			var res struct {
				Type    string `json:"type"`
				Subtype string `json:"subtype"`
				IsError bool   `json:"is_error"`
			}
			if json.Unmarshal(line, &res) == nil && res.Type == "result" {
				out = Errored
				if res.Subtype == "success" && !res.IsError {
					out = Succeeded
				}
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return out, nil
			}
			return NoResult, err
		}
	}
}

// readLine returns the next line, however long, without its newline.
func readLine(br *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		chunk, err := br.ReadSlice('\n')
		line = append(line, chunk...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return bytes.TrimRight(line, "\r\n"), err
	}
}
