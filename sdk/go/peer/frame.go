package peer

import (
	"bufio"
	"io"
)

// readBufferSize is the size of the buffer the frame reader reads through. It
// is independent of the line limit: longer lines are accumulated chunk by
// chunk and refused as soon as they exceed the limit.
const readBufferSize = 32 << 10

// frameReader splits a byte stream into newline-terminated lines of at most
// max bytes (the terminator not counted).
type frameReader struct {
	br  *bufio.Reader
	max int
	buf []byte
}

// newFrameReader reads through a buffer of bufferSize bytes, but never one
// larger than max+1: a full buffer without a terminator then already proves
// the line is too long, so an unterminated flood is refused without first
// buffering a whole bufferSize of it.
func newFrameReader(r io.Reader, max, bufferSize int) *frameReader {
	return &frameReader{br: bufio.NewReaderSize(r, min(bufferSize, max+1)), max: max}
}

// next returns the next line without its terminator. The slice is valid until
// the following call. It returns:
//
//   - io.EOF when the stream ends at a line boundary;
//   - io.ErrUnexpectedEOF when it ends inside a line, so a truncated message is
//     never mistaken for a complete one;
//   - ErrLineTooLong as soon as more than max bytes have been read without a
//     terminator. The reader must not be used afterwards: the rest of the long
//     line is still in the stream and skipping it would resynchronize on the
//     middle of a message.
//   - any other error from the underlying reader.
func (f *frameReader) next() ([]byte, error) {
	f.buf = f.buf[:0]
	for {
		chunk, err := f.br.ReadSlice('\n')
		switch err {
		case nil:
			if len(f.buf)+len(chunk)-1 > f.max {
				return nil, ErrLineTooLong
			}
			if len(f.buf) == 0 {
				return chunk[:len(chunk)-1], nil
			}
			f.buf = append(f.buf, chunk[:len(chunk)-1]...)
			return f.buf, nil
		case bufio.ErrBufferFull:
			if len(f.buf)+len(chunk) > f.max {
				return nil, ErrLineTooLong
			}
			f.buf = append(f.buf, chunk...)
		case io.EOF:
			switch n := len(f.buf) + len(chunk); {
			case n == 0:
				return nil, io.EOF
			case n > f.max:
				return nil, ErrLineTooLong
			}
			return nil, io.ErrUnexpectedEOF
		default:
			return nil, err
		}
	}
}
