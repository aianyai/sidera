package publication

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
)

func sampleProfile(t testing.TB) NodeProfile {
	t.Helper()
	return NodeProfile{
		Node: nodeOf(t, keyA), IssuedAt: t0,
		Name: "build-box", HostName: "inuc", Platform: "linux/amd64",
		System: "Ubuntu 24.04.1 LTS", SystemBuild: "6.8.0-45-generic", SideraVersion: "0.17.0",
	}
}

func sealProfile(t testing.TB, p NodeProfile) record.Record {
	t.Helper()
	r, err := record.Seal(NodeProfileDescriptor(acceptedA(t), declNow), p, signerOf(t, keyNetA))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	return r
}

func verifiedProfile(t testing.TB, p NodeProfile) record.Verified[NodeProfile] {
	t.Helper()
	// Through the wire, as a receiver sees it.
	v, err := record.Verify(NodeProfileDescriptor(acceptedA(t), declNow), mustDecode(t, mustEncode(t, sealProfile(t, p))))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	return v
}

func TestNodeProfileRoundTrip(t *testing.T) {
	minimal := NodeProfile{Node: nodeOf(t, keyA), IssuedAt: t0}
	for name, p := range map[string]NodeProfile{"full": sampleProfile(t), "minimal": minimal} {
		t.Run(name, func(t *testing.T) {
			v := verifiedProfile(t, p)
			if v.Payload() != p {
				t.Errorf("payload = %+v, want %+v", v.Payload(), p)
			}
			if v.Signers()[0] != signerOf(t, keyNetA).Public() {
				t.Error("not signed by the network key")
			}
		})
	}
}

// The integer keys are part of the format: a payload written by hand shows the
// code writes the keys the description names.
func TestNodeProfilePayloadKeys(t *testing.T) {
	p := NodeProfile{
		Node: nodeOf(t, keyA), IssuedAt: t0,
		Name: "n", HostName: "h", Platform: "a/b", System: "s", SystemBuild: "b", SideraVersion: "v",
	}
	got, err := record.EncodePayload(NodeProfileDescriptor(acceptedA(t), declNow), p)
	if err != nil {
		t.Fatal(err)
	}
	want := hexcat(
		"a8",
		"01", "5820"+hexNodeA,
		"02", "1a6553f100", // 1700000000
		"03", "616e",
		"04", "6168",
		"05", "63612f62",
		"06", "6173",
		"07", "6162",
		"08", "6176",
	)
	if !bytes.Equal(got, want) {
		t.Errorf("payload = %s\n want    %s", hex.EncodeToString(got), hex.EncodeToString(want))
	}
}

func verifyProfilePayload(t *testing.T, payload []byte) error {
	t.Helper()
	r, err := record.Sign(NodeProfileType, payload, signerOf(t, keyNetA))
	if err != nil {
		t.Fatal(err)
	}
	_, err = record.Verify(NodeProfileDescriptor(acceptedA(t), declNow), r)
	return err
}

func goodProfileWire(t testing.TB) profileWire {
	t.Helper()
	w, err := profileToWire(sampleProfile(t))
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func verifyProfileWire(t *testing.T, w profileWire) error {
	t.Helper()
	payload, err := record.MarshalCanonical(w)
	if err != nil {
		t.Fatal(err)
	}
	return verifyProfilePayload(t, payload)
}

// The classes of character a person-text field refuses: one of each kind of
// the rule's table.
var refusedProfileChars = []struct{ name, char string }{
	{"C0 control", "\x01"},
	{"newline", "\n"},
	{"DEL", "\x7f"},
	{"C1 control", "\u0085"},
	{"Arabic letter mark", "؜"},
	{"left-to-right mark", "‎"},
	{"line separator", " "},
	{"embedding override", "‪"},
	{"isolate", "⁦"},
}

// profileTextFields names the string fields with the limit of each, and how to
// set one.
var profileTextFields = []struct {
	name   string
	maxLen int
	set    func(w *profileWire, s string)
}{
	{"name", 128, func(w *profileWire, s string) { w.Name = s }},
	{"host_name", 128, func(w *profileWire, s string) { w.HostName = s }},
	{"system", 128, func(w *profileWire, s string) { w.System = s }},
	{"system_build", 64, func(w *profileWire, s string) { w.SystemBuild = s }},
	{"sidera_version", 64, func(w *profileWire, s string) { w.SideraVersion = s }},
}

func TestNodeProfilePayloadRules(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(w *profileWire)
		want   error
	}{
		{"valid", func(w *profileWire) {}, nil},
		{"every optional field absent", func(w *profileWire) { *w = profileWire{Node: w.Node, IssuedAt: w.IssuedAt} }, nil},
		{"name absent", func(w *profileWire) { w.Name = "" }, nil},
		{"host name absent", func(w *profileWire) { w.HostName = "" }, nil},
		{"platform absent", func(w *profileWire) { w.Platform = "" }, nil},
		{"system absent", func(w *profileWire) { w.System = "" }, nil},
		{"system build absent", func(w *profileWire) { w.SystemBuild = "" }, nil},
		{"version absent", func(w *profileWire) { w.SideraVersion = "" }, nil},
		{"node unset", func(w *profileWire) { w.Node = make([]byte, 32) }, ident.ErrZero},
		{"issued_at zero", func(w *profileWire) { w.IssuedAt = 0 }, record.ErrTime},
		{"issued_at negative", func(w *profileWire) { w.IssuedAt = -1 }, record.ErrTime},
		{"issued_at beyond the wire range", func(w *profileWire) { w.IssuedAt = record.MaxUnixSeconds + 1 }, record.ErrTime},
		{"platform at the length limit", func(w *profileWire) { w.Platform = strings.Repeat("a", 15) + "/" + strings.Repeat("b", 16) }, nil},
		{"platform over the length limit", func(w *profileWire) { w.Platform = strings.Repeat("a", 16) + "/" + strings.Repeat("b", 16) }, ErrProfile},
		{"platform without a slash", func(w *profileWire) { w.Platform = "linux" }, ErrProfile},
		{"platform without an architecture", func(w *profileWire) { w.Platform = "linux/" }, ErrProfile},
		{"platform without a system", func(w *profileWire) { w.Platform = "/amd64" }, ErrProfile},
		{"platform with a third part", func(w *profileWire) { w.Platform = "linux/amd64/v3" }, ErrProfile},
		{"platform in upper case", func(w *profileWire) { w.Platform = "Linux/amd64" }, ErrProfile},
		{"platform with a space", func(w *profileWire) { w.Platform = "linux/amd 64" }, ErrProfile},
		{"platform with a dash", func(w *profileWire) { w.Platform = "linux/arm-64" }, ErrProfile},
		{"platform with a non-ASCII letter", func(w *profileWire) { w.Platform = "linux/amd6٤" }, ErrProfile},
		{"platform with digits", func(w *profileWire) { w.Platform = "plan9/386" }, nil},
	}
	for _, f := range profileTextFields {
		tests = append(tests,
			struct {
				name   string
				mutate func(w *profileWire)
				want   error
			}{f.name + " at the length limit", func(w *profileWire) { f.set(w, strings.Repeat("n", f.maxLen)) }, nil},
			struct {
				name   string
				mutate func(w *profileWire)
				want   error
			}{f.name + " over the length limit", func(w *profileWire) { f.set(w, strings.Repeat("n", f.maxLen+1)) }, ErrProfile},
			struct {
				name   string
				mutate func(w *profileWire)
				want   error
			}{f.name + " counts characters, not bytes", func(w *profileWire) { f.set(w, strings.Repeat("日", f.maxLen)) }, nil},
			struct {
				name   string
				mutate func(w *profileWire)
				want   error
			}{f.name + " over the limit in characters", func(w *profileWire) { f.set(w, strings.Repeat("日", f.maxLen+1)) }, ErrProfile},
			struct {
				name   string
				mutate func(w *profileWire)
				want   error
			}{f.name + " that is not UTF-8", func(w *profileWire) { f.set(w, "a\xffb") }, record.ErrMalformed},
		)
		for _, c := range refusedProfileChars {
			tests = append(tests, struct {
				name   string
				mutate func(w *profileWire)
				want   error
			}{fmt.Sprintf("%s with a %s", f.name, c.name), func(w *profileWire) { f.set(w, "a"+c.char+"b") }, ErrProfile})
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := goodProfileWire(t)
			tc.mutate(&w)
			err := verifyProfileWire(t, w)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("rejected: %v", err)
				}
				return
			}
			if tc.want == record.ErrMalformed || tc.want == record.ErrTime {
				requireErr(t, err, tc.want)
				return
			}
			requireErr(t, err, record.ErrPayload, tc.want)
		})
	}
}

// The wire form of an absent field is the absent key. An empty string written
// out is a second spelling of absent and is refused, with every other
// non-canonical spelling and every unknown key.
func TestNodeProfileEncodingIsStrict(t *testing.T) {
	type explicit struct {
		Node          []byte  `cbor:"1,keyasint"`
		IssuedAt      int64   `cbor:"2,keyasint"`
		Name          *string `cbor:"3,keyasint,omitempty"`
		HostName      *string `cbor:"4,keyasint,omitempty"`
		Platform      *string `cbor:"5,keyasint,omitempty"`
		System        *string `cbor:"6,keyasint,omitempty"`
		SystemBuild   *string `cbor:"7,keyasint,omitempty"`
		SideraVersion *string `cbor:"8,keyasint,omitempty"`
		Extra         *string `cbor:"9,keyasint,omitempty"`
	}
	empty := ""
	node := nodeOf(t, keyA).Bytes()
	tests := []struct {
		name string
		v    explicit
		want error
	}{
		{"empty name", explicit{Node: node, IssuedAt: unixT0, Name: &empty}, record.ErrNotCanonical},
		{"empty host name", explicit{Node: node, IssuedAt: unixT0, HostName: &empty}, record.ErrNotCanonical},
		{"empty platform", explicit{Node: node, IssuedAt: unixT0, Platform: &empty}, record.ErrNotCanonical},
		{"empty system", explicit{Node: node, IssuedAt: unixT0, System: &empty}, record.ErrNotCanonical},
		{"empty system build", explicit{Node: node, IssuedAt: unixT0, SystemBuild: &empty}, record.ErrNotCanonical},
		{"empty version", explicit{Node: node, IssuedAt: unixT0, SideraVersion: &empty}, record.ErrNotCanonical},
		{"unknown key", explicit{Node: node, IssuedAt: unixT0, Extra: ptrTo("x")}, record.ErrMalformed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			payload, err := record.MarshalCanonical(tc.v)
			if err != nil {
				t.Fatal(err)
			}
			requireErr(t, verifyProfilePayload(t, payload), tc.want)
		})
	}
	t.Run("issued_at not in the shortest form", func(t *testing.T) {
		payload := hexcat("a2", "01", "5820"+hexNodeA, "02", "1b000000006553f100")
		requireErr(t, verifyProfilePayload(t, payload), record.ErrNotCanonical)
	})
	t.Run("keys out of order", func(t *testing.T) {
		payload := hexcat("a2", "02", "1a6553f100", "01", "5820"+hexNodeA)
		requireErr(t, verifyProfilePayload(t, payload), record.ErrNotCanonical)
	})
}

func TestNodeProfileSignerBinding(t *testing.T) {
	p := sampleProfile(t)
	if _, err := record.Verify(NodeProfileDescriptor(acceptedA(t), declNow), sealProfile(t, p)); err != nil {
		t.Fatalf("signed by the network key: %v", err)
	}
	tests := []struct {
		name   string
		signer int
		want   record.Binding
	}{
		{"signed by the identity key instead of the network key", keyA, record.BindingKeyMismatch},
		{"signed by another node's network key", keyNetOther, record.BindingKeyMismatch},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			payload, err := record.EncodePayload(NodeProfileDescriptor(acceptedA(t), declNow), p)
			if err != nil {
				t.Fatal(err)
			}
			forged, err := record.Sign(NodeProfileType, payload, signerOf(t, tc.signer))
			if err != nil {
				t.Fatal(err)
			}
			_, err = record.Verify(NodeProfileDescriptor(acceptedA(t), declNow), forged)
			requireErr(t, err, record.ErrSignerNotAllowed)
			if _, err := record.Seal(NodeProfileDescriptor(acceptedA(t), declNow), p, signerOf(t, tc.signer)); err == nil {
				t.Error("Seal accepted the signer")
			}
		})
	}
	t.Run("wrong type string", func(t *testing.T) {
		payload, err := record.EncodePayload(NodeProfileDescriptor(acceptedA(t), declNow), p)
		if err != nil {
			t.Fatal(err)
		}
		r, err := record.Sign("sidera/node-profile/2", payload, signerOf(t, keyNetA))
		if err != nil {
			t.Fatal(err)
		}
		_, err = record.Verify(NodeProfileDescriptor(acceptedA(t), declNow), r)
		requireErr(t, err, record.ErrType)
	})
}

// The claimed node is read before verification, only to choose the context.
func TestNodeProfileNodeReadsTheClaimedNode(t *testing.T) {
	r := sealProfile(t, sampleProfile(t))
	if node, err := NodeProfileNode(r); err != nil || node != nodeOf(t, keyA) {
		t.Fatalf("NodeProfileNode = %v, %v", node, err)
	}
	if _, err := NodeProfileNode(mustDecode(t, goldenDeclaration)); err == nil {
		t.Error("a declaration was read as a profile")
	}
}

func TestProfileReplaces(t *testing.T) {
	stored := sampleProfile(t)
	later := stored
	later.IssuedAt = t0.Add(time.Second)
	earlier := stored
	earlier.IssuedAt = t0.Add(-time.Second)
	sameTime := stored
	sameTime.Name = "renamed"
	tests := []struct {
		name     string
		incoming NodeProfile
		want     bool
	}{
		{"issued later", later, true},
		{"issued later with nothing else changed", func() NodeProfile { p := stored; p.IssuedAt = later.IssuedAt; return p }(), true},
		{"issued earlier", earlier, false},
		{"the very record stored", stored, false},
		{"issued at the same second with other content: the stored one stays", sameTime, false},
	}
	storedV := verifiedProfile(t, stored)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ProfileReplaces(storedV, verifiedProfile(t, tc.incoming)); got != tc.want {
				t.Errorf("ProfileReplaces = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestProfileOfAnotherNodeDoesNotReplace(t *testing.T) {
	cert, err := record.Verify(record.NetworkKeyCertificateDescriptor,
		networkCertRecord(t, keyOther, keyNetOther, 1, t0, t0.Add(24*time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	accepted := record.AcceptedNetworkKey{Cert: ptrTo(cert.Payload()), Revocation: record.KeyRevocationClear, HighestSerial: 1}
	p := sampleProfile(t)
	p.Node, p.IssuedAt = nodeOf(t, keyOther), t0.Add(time.Hour)
	r, err := record.Seal(NodeProfileDescriptor(accepted, declNow), p, signerOf(t, keyNetOther))
	if err != nil {
		t.Fatal(err)
	}
	v, err := record.Verify(NodeProfileDescriptor(accepted, declNow), r)
	if err != nil {
		t.Fatal(err)
	}
	if ProfileReplaces(verifiedProfile(t, sampleProfile(t)), v) {
		t.Error("a later profile of another node replaced the stored one")
	}
}

func TestNewNodeProfile(t *testing.T) {
	good := sampleProfile(t)
	tests := []struct {
		name        string
		mutate      func(p *NodeProfile)
		want        func(p *NodeProfile)
		wantDropped []string
	}{
		{"every value valid", func(p *NodeProfile) {}, func(p *NodeProfile) {}, nil},
		{"empty values stay absent and are not reported",
			func(p *NodeProfile) { p.HostName, p.SystemBuild = "", "" },
			func(p *NodeProfile) { p.HostName, p.SystemBuild = "", "" }, nil},
		{"name with a control character",
			func(p *NodeProfile) { p.Name = "box\x1b[2J" },
			func(p *NodeProfile) { p.Name = "" }, []string{"name"}},
		{"host name over the limit",
			func(p *NodeProfile) { p.HostName = strings.Repeat("h", 129) },
			func(p *NodeProfile) { p.HostName = "" }, []string{"host_name"}},
		{"host name that is not UTF-8",
			func(p *NodeProfile) { p.HostName = "a\xffb" },
			func(p *NodeProfile) { p.HostName = "" }, []string{"host_name"}},
		{"platform of a wrong shape",
			func(p *NodeProfile) { p.Platform = "Linux" },
			func(p *NodeProfile) { p.Platform = "" }, []string{"platform"}},
		{"system with a bidirectional control",
			func(p *NodeProfile) { p.System = "Win‮dows" },
			func(p *NodeProfile) { p.System = "" }, []string{"system"}},
		{"system build over its limit",
			func(p *NodeProfile) { p.SystemBuild = strings.Repeat("b", 65) },
			func(p *NodeProfile) { p.SystemBuild = "" }, []string{"system_build"}},
		{"version with a newline",
			func(p *NodeProfile) { p.SideraVersion = "0.17\n.0" },
			func(p *NodeProfile) { p.SideraVersion = "" }, []string{"sidera_version"}},
		{"several invalid values, reported in the order of the payload keys",
			func(p *NodeProfile) { p.SideraVersion, p.Name, p.Platform = "\x00", "\x00", "x" },
			func(p *NodeProfile) { p.SideraVersion, p.Name, p.Platform = "", "", "" }, []string{"name", "platform", "sidera_version"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw := good
			tc.mutate(&raw)
			want := good
			tc.want(&want)
			got, dropped, err := NewNodeProfile(raw)
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Errorf("profile = %+v, want %+v", got, want)
			}
			if fmt.Sprint(dropped) != fmt.Sprint(tc.wantDropped) {
				t.Errorf("dropped = %v, want %v", dropped, tc.wantDropped)
			}
			sealProfile(t, got) // what the builder returns is publishable
		})
	}
	t.Run("the time is cut to whole seconds in UTC", func(t *testing.T) {
		raw := good
		raw.IssuedAt = time.Unix(unixT0, 999).In(time.FixedZone("x", 3600))
		got, _, err := NewNodeProfile(raw)
		if err != nil || got.IssuedAt != t0 || got.IssuedAt.Location() != time.UTC {
			t.Errorf("issued_at = %v, %v", got.IssuedAt, err)
		}
	})
	t.Run("a node or a time that is not valid is an error, not a dropped field", func(t *testing.T) {
		noNode, noTime := good, good
		noNode.Node = ident.NodeID{}
		noTime.IssuedAt = time.Time{}
		for name, raw := range map[string]NodeProfile{"no node": noNode, "no time": noTime} {
			if _, _, err := NewNodeProfile(raw); err == nil {
				t.Errorf("%s: accepted", name)
			}
		}
	})
}

// The largest profile a node can make stays far below the input limit of a
// record, and below the figure the protocol that carries it will cap its answer
// at.
func TestLargestNodeProfile(t *testing.T) {
	const fourByte = "\U0001F600"
	p := NodeProfile{
		Node: nodeOf(t, keyA), IssuedAt: time.Unix(record.MaxUnixSeconds, 0).UTC(),
		Name: strings.Repeat(fourByte, 128), HostName: strings.Repeat(fourByte, 128), System: strings.Repeat(fourByte, 128),
		SystemBuild: strings.Repeat(fourByte, 64), SideraVersion: strings.Repeat(fourByte, 64),
		Platform: strings.Repeat("a", 15) + "/" + strings.Repeat("b", 16),
	}
	n := len(mustEncode(t, sealProfile(t, p)))
	t.Logf("largest encoded profile: %d bytes", n)
	if n > 3<<10 {
		t.Errorf("largest profile is %d bytes", n)
	}
}
