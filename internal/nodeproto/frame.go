package nodeproto

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/aianyai/sidera/internal/record"
)

// ErrFrame is wrapped by every refusal of the framing: a length prefix that is
// not the one shortest encoding, a frame that is empty or above the bound.
// A stream that ends inside a frame is io.ErrUnexpectedEOF.
var ErrFrame = errors.New("invalid frame")

// ReadMessage reads the one message of a single-message protocol: everything up
// to the end of the stream, which the writer's half-close marks. It reads at
// most max bytes plus one and refuses more with record.ErrOversize, so a peer
// cannot make the reader hold more than the limit. The read ends with the
// reader's own error, a deadline included.
func ReadMessage(r io.Reader, max int) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, int64(max)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > max {
		return nil, fmt.Errorf("%w: more than %d bytes", record.ErrOversize, max)
	}
	return data, nil
}

// AppendFrame appends one frame of a multi-frame stream to dst: the length of
// the payload as an unsigned varint, then the payload. A payload of no bytes or
// of more than max bytes is refused, because no reader would take it.
func AppendFrame(dst, payload []byte, max int) ([]byte, error) {
	if len(payload) == 0 || len(payload) > max {
		return nil, fmt.Errorf("%w: %d bytes, want 1..%d", ErrFrame, len(payload), max)
	}
	return append(binary.AppendUvarint(dst, uint64(len(payload))), payload...), nil
}

// ReadFrame reads one frame written by AppendFrame. It returns io.EOF when the
// stream ends cleanly before a frame, and wraps ErrFrame for anything else that
// is wrong with one. The length is checked against max before any byte of the
// payload is read or allocated, so a hostile length costs nothing. The prefix
// must be the shortest encoding of its value: an overlong one is refused, so
// that one payload has one stream form.
func ReadFrame(r *bufio.Reader, max int) ([]byte, error) {
	var n uint64
	for i := 0; ; i++ {
		b, err := r.ReadByte()
		if err != nil {
			if err == io.EOF && i > 0 {
				err = io.ErrUnexpectedEOF
			}
			return nil, err
		}
		if i == binary.MaxVarintLen64 {
			return nil, fmt.Errorf("%w: length prefix is too long", ErrFrame)
		}
		if b < 0x80 {
			if i > 0 && b == 0 {
				return nil, fmt.Errorf("%w: length prefix is not the shortest form", ErrFrame)
			}
			if i == binary.MaxVarintLen64-1 && b > 1 {
				return nil, fmt.Errorf("%w: length prefix overflows", ErrFrame)
			}
			n |= uint64(b) << (7 * i)
			break
		}
		n |= uint64(b&0x7f) << (7 * i)
	}
	if n == 0 || n > uint64(max) {
		return nil, fmt.Errorf("%w: length %d, want 1..%d", ErrFrame, n, max)
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return nil, err
	}
	return payload, nil
}
