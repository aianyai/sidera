package keys

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/record"
)

// White-box tests of the decoders, which Fuzz* below drive: whatever decodes
// encodes back to the same bytes, and nothing panics.

const (
	seedIdentityGolden = "9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60"
	identityFileGolden = "sidera-record 1 identity-key 8d7a810c 76 aWRlbnRpdHkta2V5\n" +
		`{"algorithm":"ed25519","seed":"nWGxne_9WmC6hEr0kuwsxERJxWl7MmkZcDusAxyuf2A"}`
	networkFileGolden = "sidera-record 1 network-key b497e582 76 bmV0d29yay1rZXk\n" +
		`{"algorithm":"ed25519","seed":"TM0Imyj_ltqdtsNG7BFOD1uKMZ81q6Yk2oz27U-4pvs"}`
	serialFileGolden = "sidera-record 1 serial 2766add4 161 c2VyaWFs\n" +
		`{"node":"node1ufd2fh9u0xuwcjwzt4fu97hafsmw754hz0exug8e7as824s67qwsw4tkv5","serial":1,"digest":"e6bdc451ac725c2be0632bcce2f18e30c5a0c3fe43265832adab4adb64ad9531"}`
	clockFileGolden       = "sidera-record 1 clock 766ed943 37 Y2xvY2s\n" + `{"high_water":"2026-10-24T05:56:00Z"}`
	clockPausedFileGolden = "sidera-record 1 clock f7e270f8 84 Y2xvY2s\n" +
		`{"high_water":"2026-10-24T05:56:00Z","rollback_paused_since":"2026-10-24T05:50:00Z"}`
	initFileGolden = "sidera-record 1 init f1544c70 39 aW5pdA\n" + `{"completed_at":"2026-10-24T06:00:00Z"}`
)

func TestEncodersWriteTheGoldenFiles(t *testing.T) {
	var seed [32]byte
	for i := range seed {
		seed[i] = mustHexByte(seedIdentityGolden[2*i : 2*i+2])
	}
	if got := string(encodeKeyFile(kindIdentityKey, seed)); got != identityFileGolden {
		t.Errorf("identity key file:\n%q\nwant\n%q", got, identityFileGolden)
	}
	if got := string(encodeClock(ClockState{HighWater: time.Date(2026, 10, 24, 5, 56, 0, 0, time.UTC)})); got != clockFileGolden {
		t.Errorf("clock file %q", got)
	}
	paused := ClockState{HighWater: time.Date(2026, 10, 24, 5, 56, 0, 0, time.UTC), RollbackPausedSince: time.Date(2026, 10, 24, 5, 50, 0, 0, time.UTC)}
	if got := string(encodeClock(paused)); got != clockPausedFileGolden {
		t.Errorf("paused clock file %q", got)
	}
	if got := string(encodeInit(time.Date(2026, 10, 24, 6, 0, 0, 0, time.UTC))); got != initFileGolden {
		t.Errorf("init file %q", got)
	}
}

func mustHexByte(s string) byte {
	var b byte
	for _, c := range s {
		b <<= 4
		switch {
		case c >= '0' && c <= '9':
			b |= byte(c - '0')
		default:
			b |= byte(c-'a') + 10
		}
	}
	return b
}

func FuzzDecodeKeyFile(f *testing.F) {
	for _, s := range []string{identityFileGolden, networkFileGolden, clockFileGolden, "", "sidera-record 1 identity-key 00000000 0 aWRlbnRpdHkta2V5\n"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		for _, kind := range []string{kindIdentityKey, kindNetworkKey} {
			seed, err := decodeKeyFile(kind, []byte(s))
			if err != nil {
				// an error never repeats the file: a key file is secret
				if nl := strings.IndexByte(s, '\n'); nl >= 0 && len(s)-nl-1 > 8 && strings.Contains(err.Error(), s[nl+1:]) {
					t.Fatalf("the error repeats the body: %v", err)
				}
				continue
			}
			if again := encodeKeyFile(kind, seed); !bytes.Equal(again, []byte(s)) {
				t.Fatalf("decoded and encoded again differs:\n%q\n%q", s, again)
			}
		}
	})
}

func FuzzDecodeClock(f *testing.F) {
	for _, s := range []string{clockFileGolden, clockPausedFileGolden, initFileGolden, ""} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		c, err := decodeClock([]byte(s))
		if err != nil {
			return
		}
		if c.HighWater.IsZero() || c.HighWater.Nanosecond() != 0 || c.HighWater.Location() != time.UTC {
			t.Fatalf("decoded to %+v", c)
		}
		if again := encodeClock(c); !bytes.Equal(again, []byte(s)) {
			t.Fatalf("decoded and encoded again differs:\n%q\n%q", s, again)
		}
	})
}

func FuzzDecodeSerial(f *testing.F) {
	for _, s := range []string{serialFileGolden, clockFileGolden, ""} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		v, err := decodeSerial([]byte(s))
		if err != nil {
			return
		}
		if v.Node.IsZero() || v.Serial < 1 || v.Digest == (record.Digest{}) {
			t.Fatalf("decoded to %+v", v)
		}
		if again := encodeSerial(v); !bytes.Equal(again, []byte(s)) {
			t.Fatalf("decoded and encoded again differs:\n%q\n%q", s, again)
		}
	})
}

func FuzzDecodeInit(f *testing.F) {
	for _, s := range []string{initFileGolden, clockFileGolden, ""} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		at, err := decodeInit([]byte(s))
		if err != nil {
			return
		}
		if at.IsZero() {
			t.Fatal("decoded to the zero time")
		}
		if again := encodeInit(at); !bytes.Equal(again, []byte(s)) {
			t.Fatalf("decoded and encoded again differs:\n%q\n%q", s, again)
		}
	})
}

func TestDecodersAcceptTheGoldenFiles(t *testing.T) {
	if _, err := decodeKeyFile(kindIdentityKey, []byte(identityFileGolden)); err != nil {
		t.Errorf("identity: %v", err)
	}
	if _, err := decodeKeyFile(kindNetworkKey, []byte(networkFileGolden)); err != nil {
		t.Errorf("network: %v", err)
	}
	if _, err := decodeKeyFile(kindNetworkKey, []byte(identityFileGolden)); err == nil {
		t.Error("an identity key file decoded as a network key file")
	}
	if v, err := decodeSerial([]byte(serialFileGolden)); err != nil || v.Serial != 1 {
		t.Errorf("serial: %+v, %v", v, err)
	}
	if c, err := decodeClock([]byte(clockPausedFileGolden)); err != nil || !c.Paused() {
		t.Errorf("clock: %+v, %v", c, err)
	}
	if at, err := decodeInit([]byte(initFileGolden)); err != nil || at.Hour() != 6 {
		t.Errorf("init: %v, %v", at, err)
	}
}
