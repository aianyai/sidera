package nodeproto

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	ma "github.com/multiformats/go-multiaddr"

	"github.com/aianyai/sidera/internal/authz"
	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/publication"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

func TestReadMessage(t *testing.T) {
	t.Run("a message of exactly the limit is read whole", func(t *testing.T) {
		got, err := ReadMessage(bytes.NewReader(make([]byte, 100)), 100)
		if err != nil || len(got) != 100 {
			t.Fatalf("got %d bytes, %v", len(got), err)
		}
	})
	t.Run("one byte over the limit is refused", func(t *testing.T) {
		if _, err := ReadMessage(bytes.NewReader(make([]byte, 101)), 100); !errors.Is(err, record.ErrOversize) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("a reader that never ends is read no further than the limit plus one", func(t *testing.T) {
		var r countingReader
		if _, err := ReadMessage(&r, 100); !errors.Is(err, record.ErrOversize) {
			t.Fatalf("got %v", err)
		}
		if r.n != 101 {
			t.Errorf("read %d bytes, want 101", r.n)
		}
	})
	t.Run("the reader's own error is returned", func(t *testing.T) {
		boom := errors.New("deadline")
		if _, err := ReadMessage(io.MultiReader(strings.NewReader("ab"), errReader{boom}), 100); !errors.Is(err, boom) {
			t.Fatalf("got %v", err)
		}
	})
}

type countingReader struct{ n int }

func (c *countingReader) Read(p []byte) (int, error) { c.n += len(p); return len(p), nil }

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

func TestFrames(t *testing.T) {
	t.Run("frames written one after another are read back and the stream then ends", func(t *testing.T) {
		var stream []byte
		var err error
		payloads := [][]byte{{1}, bytes.Repeat([]byte{2}, 127), bytes.Repeat([]byte{3}, 128), bytes.Repeat([]byte{4}, 300)}
		for _, p := range payloads {
			if stream, err = AppendFrame(stream, p, 1000); err != nil {
				t.Fatal(err)
			}
		}
		r := bufio.NewReader(bytes.NewReader(stream))
		for i, want := range payloads {
			got, err := ReadFrame(r, 1000)
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("frame %d: %d bytes, %v", i, len(got), err)
			}
		}
		if _, err := ReadFrame(r, 1000); err != io.EOF {
			t.Fatalf("after the last frame: %v, want io.EOF", err)
		}
	})
	t.Run("the prefix is the unsigned varint of the length", func(t *testing.T) {
		got, _ := AppendFrame(nil, bytes.Repeat([]byte{9}, 300), 1000)
		if !bytes.Equal(got[:2], []byte{0xac, 0x02}) {
			t.Errorf("prefix %x, want ac02", got[:2])
		}
	})
	for _, row := range []struct {
		rule   string
		stream []byte
		is     error
	}{
		{"a length above the bound is refused before any payload is read", []byte{0x0b}, ErrFrame},
		{"a length of zero", []byte{0x00}, ErrFrame},
		{"a prefix that is not the shortest form", []byte{0x81, 0x00, 0x01}, ErrFrame},
		{"a prefix of more than ten bytes", append(bytes.Repeat([]byte{0x80}, 10), 0x01), ErrFrame},
		{"a prefix that overflows 64 bits", append(bytes.Repeat([]byte{0xff}, 9), 0x02), ErrFrame},
		{"a stream that ends inside the prefix", []byte{0x80}, io.ErrUnexpectedEOF},
		{"a stream that ends inside the payload", []byte{0x05, 1, 2}, io.ErrUnexpectedEOF},
	} {
		t.Run(row.rule, func(t *testing.T) {
			if _, err := ReadFrame(bufio.NewReader(bytes.NewReader(row.stream)), 10); !errors.Is(err, row.is) {
				t.Errorf("got %v, want %v", err, row.is)
			}
		})
	}
	t.Run("a payload of no bytes or over the bound is not written", func(t *testing.T) {
		for _, n := range []int{0, 11} {
			if _, err := AppendFrame(nil, make([]byte, n), 10); !errors.Is(err, ErrFrame) {
				t.Errorf("%d bytes: %v", n, err)
			}
		}
	})
}

func TestLimits(t *testing.T) {
	for _, p := range Protocols() {
		l, ok := LimitOf(p)
		if !ok || l.Request <= 0 || l.Answer <= 0 {
			t.Errorf("%s: %+v, %v", p, l, ok)
		}
	}
	if _, ok := LimitOf("/sidera/pairing/1.0.0"); ok {
		t.Error("a protocol of another package has a limit here")
	}
	if got, want := limit(ProtocolSend).Request, envelope.MaxEncoded()+512; got != want {
		t.Errorf("send request limit %d, want the longest envelope plus 512 = %d", got, want)
	}
}

// The send protocol is spoken under three identifiers, the newest first. The
// 3.0.0 identifier and the 2.0.0 one share their frames and limits, so the node
// that serves the older one can still be sent to; only the newest carries a
// model choice, and that is the sender's to offer alone.
func TestSendProtocolVersions(t *testing.T) {
	if got, want := Offer(ProtocolSend), []string{"/sidera/send/3.0.0", "/sidera/send/2.0.0", "/sidera/send/1.0.0"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("offered %v, want %v", got, want)
	}
	if IsLegacy(PreviousProtocolSend) || !IsLegacy(LegacyProtocolSend) {
		t.Error("only the 1.0.0 stream is unframed")
	}
	for _, p := range []string{PreviousProtocolSend, LegacyProtocolSend} {
		if limit(p) != limit(ProtocolSend) {
			t.Errorf("%s has the limits %+v, want those of %s", p, limit(p), ProtocolSend)
		}
		if !slices.Contains(Protocols(), p) {
			t.Errorf("a node does not list %s among the identifiers it serves", p)
		}
	}
}

// TestOfferedIdentifiers: a call proposes the current identifier of a protocol
// first and its legacy one last, and every identifier is held to the same limits,
// which bound the payload of a frame as they bound the message of a legacy
// stream. The directory has one between, TestDirectoryProtocolVersions.
func TestOfferedIdentifiers(t *testing.T) {
	for _, current := range []string{ProtocolSend, ProtocolPull, ProtocolDirectory} {
		offered := Offer(current)
		if len(offered) < 2 || offered[0] != current || IsLegacy(current) || !IsLegacy(offered[len(offered)-1]) {
			t.Fatalf("%s is offered as %v", current, offered)
		}
		for _, o := range offered {
			if a, b := limit(offered[0]), limit(o); a != b {
				t.Errorf("%s: limits %+v and %+v differ", current, a, b)
			}
		}
	}
	for _, only := range []string{ProtocolDialBack, ProtocolProfile} {
		if offered := Offer(only); len(offered) != 1 || offered[0] != only || IsLegacy(only) {
			t.Errorf("%s is offered as %v", only, offered)
		}
	}
	for _, other := range []string{LegacyProtocolSend, "/sidera/pairing/1.0.0"} {
		if Offer(other) != nil {
			t.Errorf("%s is offered", other)
		}
	}
}

// The largest legal message of each kind fits its limit, as the frame that
// carries it: a limit below it would make a node refuse what the protocol lets a
// peer send.
func TestLargestMessagesFit(t *testing.T) {
	t.Run("send request with the longest envelope", func(t *testing.T) {
		b, err := SendRequest{Sender: ep1, Envelope: make([]byte, envelope.MaxEncoded())}.Encode()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := AppendFrame(nil, b, limit(ProtocolSend).Request); err != nil {
			t.Errorf("%d bytes over the limit %d: %v", len(b), limit(ProtocolSend).Request, err)
		}
	})
	t.Run("directory answer with a full page of the longest entries", func(t *testing.T) {
		entry := DirectoryEntry{
			Endpoint: ep1, Kind: 3, DisplayName: strings.Repeat("\U0001F600", 128), Description: strings.Repeat("\U0001F600", 1024),
		}
		entry.Actions = authz.Actions
		entry.Adapter = &DirectoryAdapter{
			Instance: strings.Repeat("a", 32), ID: strings.Repeat("a", 32), Version: "1.0.0-" + strings.Repeat("a", 58),
			Runtime: &adapterproto.RuntimeInfo{Name: strings.Repeat("\U0001F600", 64), Version: strings.Repeat("\U0001F600", 64)},
		}
		entries := make([]DirectoryEntry, MaxDirectoryPage)
		for i := range entries {
			entries[i] = entry
		}
		b, err := DirectoryAnswer{Outcome: OutcomeOK, Entries: entries, NextCursor: strings.Repeat("c", MaxCursorLen)}.Encode()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := AppendFrame(nil, b, limit(ProtocolDirectory).Answer); err != nil {
			t.Errorf("%d bytes over the limit %d: %v", len(b), limit(ProtocolDirectory).Answer, err)
		}
	})
	t.Run("dial-back answer with the longest address and a reason", func(t *testing.T) {
		// A multiaddr of MaxAddressLen bytes: a dns name filling the rest of it.
		name := strings.Repeat("a", MaxAddressLen-4)
		long, err := ma.NewMultiaddr("/dns/" + name)
		if err != nil || len(long.Bytes()) > MaxAddressLen {
			t.Fatalf("test address: %d bytes, %v", len(long.Bytes()), err)
		}
		b, err := DialBackAnswer{Outcome: OutcomeOK, Result: DialBackReached, Address: long}.Encode()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := AppendFrame(nil, b, limit(ProtocolDialBack).Answer); err != nil {
			t.Errorf("%d bytes over the limit %d: %v", len(b), limit(ProtocolDialBack).Answer, err)
		}
		if b, err = (DialBackAnswer{Outcome: OutcomeRefused, Reason: strings.Repeat("a", MaxReasonLen)}).Encode(); err != nil {
			t.Fatal(err)
		} else if _, err := AppendFrame(nil, b, limit(ProtocolDialBack).Answer); err != nil {
			t.Errorf("%d bytes over the limit: %v", len(b), err)
		}
	})
	t.Run("profile answer with the largest profile a node can make", func(t *testing.T) {
		const fourByte = "\U0001F600"
		node, err := ident.NodeIDFromKey(testSigner(t).Public())
		if err != nil {
			t.Fatal(err)
		}
		p := publication.NodeProfile{
			Node: node, IssuedAt: time.Unix(record.MaxUnixSeconds, 0).UTC(),
			Name: strings.Repeat(fourByte, adapterproto.MaxDisplayName), HostName: strings.Repeat(fourByte, adapterproto.MaxDisplayName),
			System:      strings.Repeat(fourByte, adapterproto.MaxDisplayName),
			SystemBuild: strings.Repeat(fourByte, publication.MaxProfileLabelLen), SideraVersion: strings.Repeat(fourByte, publication.MaxProfileLabelLen),
			Platform: strings.Repeat("a", 15) + "/" + strings.Repeat("b", 16),
		}
		payload, err := record.EncodePayload(publication.NodeProfileDescriptor(record.AcceptedNetworkKey{}, p.IssuedAt), p)
		if err != nil {
			t.Fatal(err)
		}
		rec, err := record.Sign(publication.NodeProfileType, payload, testSigner(t))
		if err != nil {
			t.Fatal(err)
		}
		b, err := ProfileAnswer{Outcome: OutcomeOK, Profile: &rec}.Encode()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := AppendFrame(nil, b, limit(ProtocolProfile).Answer); err != nil {
			t.Errorf("%d bytes over the limit %d: %v", len(b), limit(ProtocolProfile).Answer, err)
		}
		t.Logf("largest profile answer: %d bytes of %d", len(b), limit(ProtocolProfile).Answer)
	})
	t.Run("send answer with a report per recipient and a control reply", func(t *testing.T) {
		big, err := record.Sign("sidera/test/big/1", bytes.Repeat([]byte{1}, record.DefaultMaxInput-1024), testSigner(t))
		if err != nil {
			t.Fatal(err)
		}
		reports := make([]record.Record, MaxRecipients)
		for i := range reports {
			reports[i] = big
		}
		b, err := SendAnswer{Outcome: OutcomeOK, Reports: reports, ControlReply: &big}.Encode()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := AppendFrame(nil, b, limit(ProtocolSend).Answer); err != nil {
			t.Errorf("%d bytes over the limit %d: %v", len(b), limit(ProtocolSend).Answer, err)
		}
	})
}
