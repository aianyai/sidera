package peer

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

// oracleFrames splits data the obvious way and says what a frame reader with
// the given limit must yield: the lines, then the terminal error.
func oracleFrames(data []byte, max int) (lines [][]byte, final error) {
	rest := data
	for {
		i := bytes.IndexByte(rest, '\n')
		if i < 0 {
			switch {
			case len(rest) == 0:
				return lines, io.EOF
			case len(rest) > max:
				return lines, ErrLineTooLong
			}
			return lines, io.ErrUnexpectedEOF
		}
		if i > max {
			return lines, ErrLineTooLong
		}
		lines = append(lines, rest[:i])
		rest = rest[i+1:]
	}
}

func readAllFrames(r io.Reader, max, bufSize int) (lines [][]byte, final error) {
	fr := newFrameReader(r, max, bufSize)
	for {
		line, err := fr.next()
		if err != nil {
			return lines, err
		}
		lines = append(lines, bytes.Clone(line))
	}
}

func checkFrames(t *testing.T, data []byte, max, bufSize int, oneByte bool) {
	t.Helper()
	var r io.Reader = bytes.NewReader(data)
	if oneByte {
		r = iotest.OneByteReader(r)
	}
	gotLines, gotErr := readAllFrames(r, max, bufSize)
	wantLines, wantErr := oracleFrames(data, max)
	if !errors.Is(gotErr, wantErr) {
		t.Fatalf("max=%d buf=%d data=%q: final error %v, want %v", max, bufSize, data, gotErr, wantErr)
	}
	if len(gotLines) != len(wantLines) {
		t.Fatalf("max=%d buf=%d data=%q: %d lines, want %d", max, bufSize, data, len(gotLines), len(wantLines))
	}
	for i := range gotLines {
		if !bytes.Equal(gotLines[i], wantLines[i]) {
			t.Fatalf("line %d = %q, want %q", i, gotLines[i], wantLines[i])
		}
	}
}

func TestFrameReader(t *testing.T) {
	for _, tc := range []struct {
		name string
		data string
		max  int
	}{
		{"empty stream", "", 10},
		{"one line", "abc\n", 10},
		{"empty lines", "\n\n", 10},
		{"line of exactly max", strings.Repeat("a", 10) + "\n", 10},
		{"line of max+1", strings.Repeat("a", 11) + "\n", 10},
		{"unterminated within max", "abcde", 10},
		{"unterminated beyond max", strings.Repeat("a", 11), 10},
		{"good lines then overlong", "ab\ncd\n" + strings.Repeat("a", 40) + "\nnever read\n", 10},
		{"long line across many buffers", strings.Repeat("a", 5000) + "\nx\n", 6000},
		{"overlong across many buffers", strings.Repeat("a", 5000) + "\nx\n", 4999},
		{"crlf keeps the carriage return", "ab\r\n", 10},
	} {
		for _, bufSize := range []int{1, 16, 17, 64, 4096} {
			for _, oneByte := range []bool{false, true} {
				t.Run(tc.name, func(t *testing.T) { checkFrames(t, []byte(tc.data), tc.max, bufSize, oneByte) })
			}
		}
	}
}

func TestFrameReaderPropagatesReadErrors(t *testing.T) {
	boom := errors.New("boom")
	_, err := readAllFrames(io.MultiReader(strings.NewReader("ab\ncd"), iotest.ErrReader(boom)), 10, 64)
	if !errors.Is(err, boom) {
		t.Fatalf("got %v, want the reader's error", err)
	}
}

func FuzzFrameReader(f *testing.F) {
	f.Add([]byte("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"m\"}\n"), uint8(63), uint8(40), false)
	f.Add([]byte("a\nbb\nccc\n"), uint8(2), uint8(0), true)
	f.Add(bytes.Repeat([]byte("x"), 300), uint8(5), uint8(3), false)
	f.Add([]byte("\n\n\nabc"), uint8(10), uint8(30), true)
	f.Fuzz(func(t *testing.T, data []byte, maxByte, bufByte uint8, oneByte bool) {
		checkFrames(t, data, int(maxByte)%64+1, int(bufByte)%80+1, oneByte)
	})
}
