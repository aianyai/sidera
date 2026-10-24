package recfile_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/node/recfile"
)

// The vectors are written out by hand. The checksums are CRC32C (Castagnoli)
// computed with a separate bitwise implementation, checked against the standard
// value 0xE3069283 for "123456789", and the names are SHA-256 of the kind, a
// space and the key, computed with shasum.
var frameVectors = []struct {
	name string
	kind string
	key  string
	body string
	file string
	hash string // the file name
}{
	{
		name: "a singleton: kind and key are the same word",
		kind: "clock", key: "clock", body: `{"a":1}`,
		file: "sidera-record 1 clock cff7d56a 7 Y2xvY2s\n{\"a\":1}",
		hash: "22e7aec0fc87e1df9286d7123c0dd75166327716223809bcbdf279a959a2855a.rec",
	},
	{
		name: "a hashed kind with a four field key",
		kind: "in", key: "node1abc ep1def message-1 ep1ghi", body: `{}`,
		file: "sidera-record 1 in 297bd0aa 2 bm9kZTFhYmMgZXAxZGVmIG1lc3NhZ2UtMSBlcDFnaGk\n{}",
		hash: "5edcf5203b4db4982edba088adf18ceeffc1678a3528065de9fa1e60e8816e22.rec",
	},
	{
		name: "an empty body has checksum zero",
		kind: "seq", key: "k", body: ``,
		file: "sidera-record 1 seq 00000000 0 aw\n",
		hash: "a3fe3849a3d1025dc05e97ddd4109327317226840768958a0bc019824a551b81.rec",
	},
	{
		name: "a control signal has its own kind in the file name",
		kind: "out-ctl", key: "k", body: ``,
		file: "sidera-record 1 out-ctl 00000000 0 aw\n",
		hash: "84fbc424624587040a48876cd954180fdbb575592ac304043842c82f07445cb2.rec",
	},
	{
		name: "the same key as a message is another file",
		kind: "out", key: "k", body: ``,
		file: "sidera-record 1 out 00000000 0 aw\n",
		hash: "8cbeac574a9a2959c29636d043d385a96748e45ac10e4e524fb7841dfd3aa9de.rec",
	},
}

func TestFrameGoldenVectors(t *testing.T) {
	for _, v := range frameVectors {
		t.Run(v.name, func(t *testing.T) {
			got, err := recfile.EncodeFrame(v.kind, []byte(v.key), []byte(v.body))
			if err != nil {
				t.Fatalf("EncodeFrame: %v", err)
			}
			if string(got) != v.file {
				t.Errorf("EncodeFrame:\n got %q\nwant %q", got, v.file)
			}
			if name := recfile.FileName(v.kind, []byte(v.key)); name != v.hash {
				t.Errorf("FileName = %q, want %q", name, v.hash)
			}
			f, dmg := recfile.DecodeFrame([]byte(v.file))
			if dmg != nil {
				t.Fatalf("DecodeFrame: %v", dmg)
			}
			if f.Kind != v.kind || string(f.Key) != v.key || string(f.Body) != v.body {
				t.Errorf("DecodeFrame = {%q %q %q}, want {%q %q %q}", f.Kind, f.Key, f.Body, v.kind, v.key, v.body)
			}
		})
	}
}

// refFrame is the format of the design written again in the test, to check
// frames of the shapes the vectors do not have.
func refFrame(kind, key, body string, crc string, length string) string {
	return "sidera-record 1 " + kind + " " + crc + " " + length + " " + b64(key) + "\n" + body
}

func b64(s string) string {
	const tbl = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	var out []byte
	b := []byte(s)
	for i := 0; i < len(b); i += 3 {
		var n uint32
		var k int
		for j := 0; j < 3; j++ {
			n <<= 8
			if i+j < len(b) {
				n |= uint32(b[i+j])
				k++
			}
		}
		for j := 0; j <= k; j++ {
			out = append(out, tbl[(n>>(18-6*uint(j)))&63])
		}
	}
	return string(out)
}

// A frame of every size the format allows round-trips, and the first line and
// body limits are the ones written down.
func TestFrameLimits(t *testing.T) {
	key := bytes.Repeat([]byte{'k'}, 100)
	body := bytes.Repeat([]byte{'b'}, recfile.MaxBodyLen)
	file, err := recfile.EncodeFrame("in", key, body)
	if err != nil {
		t.Fatalf("a body of MaxBodyLen: %v", err)
	}
	f, dmg := recfile.DecodeFrame(file)
	if dmg != nil || !bytes.Equal(f.Body, body) || !bytes.Equal(f.Key, key) {
		t.Fatalf("round trip of a body of MaxBodyLen: %v", dmg)
	}
	if _, err := recfile.EncodeFrame("in", key, append(body, 'b')); err == nil {
		t.Error("a body over MaxBodyLen was encoded")
	}

	// A first line near MaxHeaderLen bytes is accepted, one over it is not.
	prefix := len("sidera-record 1 in 00000000 0 ") + 1 // plus the newline
	keyLen := (recfile.MaxHeaderLen - prefix) / 4 * 3   // base64url of 3n bytes is 4n characters
	exact := bytes.Repeat([]byte{'k'}, keyLen)
	file, err = recfile.EncodeFrame("in", exact, nil)
	if err != nil {
		t.Fatalf("a first line near MaxHeaderLen: %v", err)
	}
	if first := bytes.IndexByte(file, '\n') + 1; first > recfile.MaxHeaderLen {
		t.Fatalf("first line is %d bytes, over the limit, yet encoded", first)
	}
	if _, dmg := recfile.DecodeFrame(file); dmg != nil {
		t.Errorf("a first line within the limit: %v", dmg)
	}
	if _, err := recfile.EncodeFrame("in", bytes.Repeat([]byte{'k'}, recfile.MaxHeaderLen), nil); err == nil {
		t.Error("a first line over MaxHeaderLen was encoded")
	}
}

func TestEncodeFrameRefusals(t *testing.T) {
	for _, c := range []struct {
		name string
		kind string
		key  string
	}{
		{"kind in capitals", "In", "k"},
		{"kind starting with a digit", "1in", "k"},
		{"kind longer than 32 characters", strings.Repeat("a", 33), "k"},
		{"empty kind", "", "k"},
		{"kind with a space", "in x", "k"},
		{"empty key", "in", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := recfile.EncodeFrame(c.kind, []byte(c.key), nil); err == nil {
				t.Errorf("EncodeFrame(%q, %q) succeeded", c.kind, c.key)
			}
		})
	}
	if _, err := recfile.EncodeFrame(strings.Repeat("a", 32), []byte("k"), nil); err != nil {
		t.Errorf("a kind of 32 characters is valid: %v", err)
	}
}

// Each row breaks one rule of the first line, the length or the checksum, and
// names the class the damage must get. A damage of the first line means the key
// cannot be trusted; a damage of the body means the first line was readable.
func TestDecodeFrameDamageByRule(t *testing.T) {
	ok := "{}"
	const crc = "297bd0aa" // CRC32C of "{}"
	long := strings.Repeat("a", recfile.MaxHeaderLen)
	for _, c := range []struct {
		name   string
		data   string
		class  recfile.DamageClass
		reason recfile.DamageReason
	}{
		{"no newline at all", "sidera-record 1 in " + crc + " 2 aw", recfile.HeaderUnreadable, recfile.ReasonHeaderNoNewline},
		{"newline only after the limit", "sidera-record 1 in " + crc + " 2 " + long + "\n{}", recfile.HeaderUnreadable, recfile.ReasonHeaderNoNewline},
		{"five fields", "sidera-record 1 in " + crc + " 2\n{}", recfile.HeaderUnreadable, recfile.ReasonHeaderFields},
		{"seven fields", "sidera-record 1 in " + crc + " 2 aw x\n{}", recfile.HeaderUnreadable, recfile.ReasonHeaderFields},
		{"a double space makes an empty field", "sidera-record 1 in " + crc + "  2 aw\n{}", recfile.HeaderUnreadable, recfile.ReasonHeaderFields},
		{"an empty key", "sidera-record 1 in " + crc + " 2 \n{}", recfile.HeaderUnreadable, recfile.ReasonHeaderFields},
		{"a tab is not a separator", "sidera-record\t1 in " + crc + " 2 aw\n{}", recfile.HeaderUnreadable, recfile.ReasonHeaderFields},
		{"wrong magic", "sidera-recorD 1 in " + crc + " 2 aw\n{}", recfile.HeaderUnreadable, recfile.ReasonHeaderMagic},
		{"wrong format version", "sidera-record 2 in " + crc + " 2 aw\n{}", recfile.HeaderUnreadable, recfile.ReasonHeaderMagic},
		{"kind in capitals", "sidera-record 1 In " + crc + " 2 aw\n{}", recfile.HeaderUnreadable, recfile.ReasonHeaderKind},
		{"kind longer than 32", "sidera-record 1 " + strings.Repeat("a", 33) + " " + crc + " 2 aw\n{}", recfile.HeaderUnreadable, recfile.ReasonHeaderKind},
		{"checksum in capitals", "sidera-record 1 in 297BD0AA 2 aw\n{}", recfile.HeaderUnreadable, recfile.ReasonHeaderChecksum},
		{"checksum of seven digits", "sidera-record 1 in 97bd0aa 2 aw\n{}", recfile.HeaderUnreadable, recfile.ReasonHeaderChecksum},
		{"checksum of nine digits", "sidera-record 1 in 0297bd0aa 2 aw\n{}", recfile.HeaderUnreadable, recfile.ReasonHeaderChecksum},
		{"checksum that is not hex", "sidera-record 1 in 297bd0ag 2 aw\n{}", recfile.HeaderUnreadable, recfile.ReasonHeaderChecksum},
		{"length with a leading zero", "sidera-record 1 in " + crc + " 02 aw\n{}", recfile.HeaderUnreadable, recfile.ReasonHeaderLength},
		{"length with a sign", "sidera-record 1 in " + crc + " +2 aw\n{}", recfile.HeaderUnreadable, recfile.ReasonHeaderLength},
		{"length that is not a number", "sidera-record 1 in " + crc + " two aw\n{}", recfile.HeaderUnreadable, recfile.ReasonHeaderLength},
		{"length too large for an int", "sidera-record 1 in " + crc + " 99999999999999999999 aw\n{}", recfile.HeaderUnreadable, recfile.ReasonHeaderLength},
		{"key with padding", "sidera-record 1 in " + crc + " 2 aw==\n{}", recfile.HeaderUnreadable, recfile.ReasonHeaderKey},
		{"key with a character of the other alphabet", "sidera-record 1 in " + crc + " 2 a+\n{}", recfile.HeaderUnreadable, recfile.ReasonHeaderKey},
		{"key whose last character carries stray bits", "sidera-record 1 in " + crc + " 2 ax\n{}", recfile.HeaderUnreadable, recfile.ReasonHeaderKey},
		{"key that is only a carriage return, which the decoder skips", "sidera-record 1 in " + crc + " 2 \r\n{}", recfile.HeaderUnreadable, recfile.ReasonHeaderKey},
		{"key with a carriage return inside, which the decoder skips", "sidera-record 1 in " + crc + " 2 a\rw\n{}", recfile.HeaderUnreadable, recfile.ReasonHeaderKey},
		{"key of an impossible length", "sidera-record 1 in " + crc + " 2 a\n{}", recfile.HeaderUnreadable, recfile.ReasonHeaderKey},
		{"body shorter than the length", "sidera-record 1 in " + crc + " 2 aw\n{", recfile.BodyDamaged, recfile.ReasonLengthMismatch},
		{"no body at all", "sidera-record 1 in " + crc + " 2 aw\n", recfile.BodyDamaged, recfile.ReasonLengthMismatch},
		{"one byte after the body", "sidera-record 1 in " + crc + " 2 aw\n{}\n", recfile.BodyDamaged, recfile.ReasonLengthMismatch},
		{"length above the limit", "sidera-record 1 in " + crc + " 262145 aw\n" + ok, recfile.BodyDamaged, recfile.ReasonBodyTooLarge},
		{"checksum of another body", "sidera-record 1 in 00000000 2 aw\n{}", recfile.BodyDamaged, recfile.ReasonChecksumMismatch},
		{"a flipped body bit", "sidera-record 1 in " + crc + " 2 aw\n{]", recfile.BodyDamaged, recfile.ReasonChecksumMismatch},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, dmg := recfile.DecodeFrame([]byte(c.data))
			if dmg == nil {
				t.Fatalf("DecodeFrame accepted %q", truncate(c.data))
			}
			if dmg.Class != c.class || dmg.Reason != c.reason {
				t.Errorf("damage = class %d reason %d, want class %d reason %d (%s)", dmg.Class, dmg.Reason, c.class, c.reason, dmg.Detail)
			}
			if c.class == recfile.BodyDamaged && (dmg.Kind != "in" || string(dmg.Key) != "k") {
				// aw is base64url of the single byte 'k'.
				t.Errorf("a body damage must carry the kind and key of the first line, got %q %q", dmg.Kind, dmg.Key)
			}
			if c.class == recfile.HeaderUnreadable && (dmg.Kind != "" || dmg.Key != nil) {
				t.Errorf("a header damage carries no trusted kind or key, got %q %q", dmg.Kind, dmg.Key)
			}
		})
	}
}

func truncate(s string) string {
	if len(s) > 80 {
		return s[:80] + "..."
	}
	return s
}

// A damaged newline can glue the body of a file into its first line, and the
// body of a file under keys/ is a private key: whatever the first line holds
// stays out of what a damage says, in any field.
func TestHeaderDamageNeverEchoesTheFirstLine(t *testing.T) {
	const secret = "SECRETKEYMATERIAL"
	for _, c := range []struct {
		name string
		line string
	}{
		{"the body glued after the key", "sidera-record 1 in 297bd0aa 2 aw " + secret + "\n{}"},
		{"a secret as the magic", secret + " 1 in 297bd0aa 2 aw\n{}"},
		{"a secret as the format version", "sidera-record " + secret + " in 297bd0aa 2 aw\n{}"},
		{"a secret as the kind", "sidera-record 1 " + secret + " 297bd0aa 2 aw\n{}"},
		{"a secret as the checksum", "sidera-record 1 in " + secret + " 2 aw\n{}"},
		{"a secret as the length", "sidera-record 1 in 297bd0aa " + secret + " aw\n{}"},
		{"a secret as the key", "sidera-record 1 in 297bd0aa 2 " + secret + "+\n{}"},
		{"a secret as a non-canonical key", "sidera-record 1 in 297bd0aa 2 " + secret + "\n{}"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, _, dmg := recfile.ParseHeader([]byte(c.line))
			if dmg == nil {
				// A key made of letters only is a key; the rule is about damage.
				return
			}
			if strings.Contains(dmg.Detail, secret) || strings.Contains(dmg.Error(), secret) {
				t.Errorf("the damage echoes the first line: %q", dmg.Error())
			}
		})
	}
	for _, line := range []string{
		"sidera-record 1 " + secret + " 297bd0aa 2 aw\n{}",
		"sidera-record 1 clock 297bd0aa 2 " + secret + "\n{}",
	} {
		if _, dmg := recfile.DecodeSingleton("clock", []byte(line)); dmg == nil || strings.Contains(dmg.Error(), secret) {
			t.Errorf("DecodeSingleton(%q) damage = %v, want one without the first line", line, dmg)
		}
	}
}
