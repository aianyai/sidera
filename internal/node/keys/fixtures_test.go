package keys_test

import "encoding/hex"

// Golden fixtures. Every byte below was produced by an implementation that
// does not share code with this package: Ed25519 from Python's cryptography
// package, SHA-256 from hashlib, and the record framing, CBOR, bech32m, CRC32C
// (Castagnoli) and base64 written out by hand in the generator script, never
// read from the code under test. The keys are the RFC 8032 test keys 1
// (identity) and 2 (network); the certificate is the network-key certificate
// of the record package's own golden vectors, the one that key 1 signed for key
// 2 at serial 1.

const (
	goldenIdentitySeedHex = "9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60"
	goldenNetworkSeedHex  = "4ccd089b28ff96da9db6c346ec114e0f5b8a319f35aba624da8cf6ed4fb8a6fb"
	goldenNode            = "node1ufd2fh9u0xuwcjwzt4fu97hafsmw754hz0exug8e7as824s67qwsw4tkv5"
)

// sidera-record 1 identity-key <crc32c of the body> <body length> base64url("identity-key")
const goldenIdentityKeyFile = "sidera-record 1 identity-key 8d7a810c 76 aWRlbnRpdHkta2V5\n" +
	`{"algorithm":"ed25519","seed":"nWGxne_9WmC6hEr0kuwsxERJxWl7MmkZcDusAxyuf2A"}`

const goldenNetworkKeyFile = "sidera-record 1 network-key b497e582 76 bmV0d29yay1rZXk\n" +
	`{"algorithm":"ed25519","seed":"TM0Imyj_ltqdtsNG7BFOD1uKMZ81q6Yk2oz27U-4pvs"}`

const goldenCertificateDigest = "e6bdc451ac725c2be0632bcce2f18e30c5a0c3fe43265832adab4adb64ad9531"

var goldenCertificate = mustHex(
	"a3",
	"01",
	"78197369646572612f6e6574776f726b2d6b65792d636572742f31",
	"02",
	"5859",
	"a5",
	"01",
	"5820e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d",
	"02",
	"a201010258203d4017c3e843895a92b70aa74d1b7ebc9c982ccf2ec4968cc0cd55f12af4660c",
	"03",
	"01",
	"04",
	"1a6553f100",
	"05",
	"1a657b7e00",
	"03",
	"81",
	"a2",
	"01",
	"a20101025820d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a",
	"02",
	"5840d341cca122fb3d35608e5e6dc3a2ad7cfdf2a566c6025c0a9fe3f5837823005f5e8f6bfbaa1bfb1dac1e42f1d3a4fb47504ac7f912eee8038c1821caf338370c",
)

// The serial file that names the golden certificate (serial 1), and the same
// with serial 2: a renewal that was interrupted after the serial was written.
const goldenSerialFile = "sidera-record 1 serial 2766add4 161 c2VyaWFs\n" +
	`{"node":"` + goldenNode + `","serial":1,"digest":"` + goldenCertificateDigest + `"}`

const goldenSerialFileInterrupted = "sidera-record 1 serial e52573bb 161 c2VyaWFs\n" +
	`{"node":"` + goldenNode + `","serial":2,"digest":"` + goldenCertificateDigest + `"}`

// The self-revocation of node 1: type sidera/node-self-revocation/1, payload
// {1: node}, signed by key 1.
const (
	goldenSelfRevocationHex    = "a301781d7369646572612f6e6f64652d73656c662d7265766f636174696f6e2f31025824a1015820e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d0381a201a20101025820d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a0258402d10ff94a6b6f52556b407f16e0e2861be58182b181c1a0c9a92de7a0d9688091912dd9986bc3ec32fd17370f6670f309a312870abe5d3b65423d5705cebc804"
	goldenSelfRevocationDigest = "9c98b22ce57911e44a3ad0f641c752bb7b97b9a5f33a3077917378b59a6a82d5"
)

// The export of that statement: base64 of the record bytes, 64 columns.
const goldenExport = "-----BEGIN SIDERA SELF-REVOCATION-----\n" +
	"owF4HXNpZGVyYS9ub2RlLXNlbGYtcmV2b2NhdGlvbi8xAlgkoQFYIOJapNy8ebjs\n" +
	"ScJdU8L6/Uw271K3E/JuIPn3YHVWGvAdA4GiAaIBAQJYINdamAGCsQq31Uv+08lk\n" +
	"BzoO4XLz2qYjJa8CGmj3B1EaAlhALRD/lKa29SVWtAfxbg4oYb5YGCsYHBoMmpLe\n" +
	"eg2WiAkZEt2Zhrw+wy/Rc3D2Zw8wmjEocKvl07ZUI9VwXOvIBA==\n" +
	"-----END SIDERA SELF-REVOCATION-----\n"

const goldenClockFile = "sidera-record 1 clock 766ed943 37 Y2xvY2s\n" +
	`{"high_water":"2026-10-24T05:56:00Z"}`

const goldenClockPausedFile = "sidera-record 1 clock f7e270f8 84 Y2xvY2s\n" +
	`{"high_water":"2026-10-24T05:56:00Z","rollback_paused_since":"2026-10-24T05:50:00Z"}`

const goldenInitFile = "sidera-record 1 init f1544c70 39 aW5pdA\n" +
	`{"completed_at":"2026-10-24T06:00:00Z"}`

func mustHex(parts ...string) []byte {
	var all string
	for _, p := range parts {
		all += p
	}
	b, err := hex.DecodeString(all)
	if err != nil {
		panic(err)
	}
	return b
}
