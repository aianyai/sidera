package record

import "encoding/hex"

// Golden vectors. Every byte below was written out by hand from the format
// description, and the signatures were produced by an independent Ed25519
// implementation over the signed bytes shown. None of it comes from the encoder
// under test. Keys are the RFC 8032 test keys 1, 2 and 3.

func hexcat(parts ...string) []byte {
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

// goldenNetworkKeyCertificate: type sidera/network-key-cert/1, signed by key 1.
// Signed bytes: 0x11 "sidera/record-sig" 0x19 "sidera/network-key-cert/1" uvarint(89) payload.
// Digest: SHA-256 of the same layout with the domain "sidera/record-digest".
var goldenNetworkKeyCertificate = hexcat(
	"a3", // { 3 pairs: the envelope
	"01", // 1: type
	"78197369646572612f6e6574776f726b2d6b65792d636572742f31", // "sidera/network-key-cert/1"
	"02",   // 2: payload
	"5859", // bstr(89)
	"a5",   // payload: { 5 pairs
	"01",   // payload: 1: node
	"58208bfff7394412d3b94d49843e935680113278c5fae441e989308bde061be4f382", // payload: h'8bfff7394412d3b94d49843e935680113278c5fae441e989308bde061be4f382' (node ID of RFC 8032 key 1)
	"02", // payload: 2: network key
	"a201010258203d4017c3e843895a92b70aa74d1b7ebc9c982ccf2ec4968cc0cd55f12af4660c", // payload: {1: 1, 2: h'3d4017c3e843895a92b70aa74d1b7ebc9c982ccf2ec4968cc0cd55f12af4660c'} (RFC 8032 key 2)
	"03",         // payload: 3: serial
	"01",         // payload: 1
	"04",         // payload: 4: not_before
	"1a6553f100", // payload: 1700000000
	"05",         // payload: 5: not_after
	"1a657b7e00", // payload: 1702592000 (30 days later)
	"03",         // 3: signatures
	"81",         // [ 1 signature(s), sorted by encoded public key
	"a2",         // { signature item
	"01",         // 1: public key
	"a20101025820d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a", // {1: 1 (Ed25519), 2: h'd75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a'} (RFC 8032 key 1)
	"02", // 2: signature
	"5840bacead9a45d511f8937b9fc6cc31bd227e37f2c0147e6dacc9cf495c6e36d5ee817a54d483fb75a36c813603b6e6ce73ffd92afc81421194787b043c243ace01", // bstr(64) Ed25519 signature by key 1
)

const goldenNetworkKeyCertificateDigest = "fa7d58c643363d6ba9c2fceec7f84ad56f051e561f6aac8f14b1b064a7816a0f"
const goldenNetworkKeyCertificatePayload = "a50158208bfff7394412d3b94d49843e935680113278c5fae441e989308bde061be4f38202a201010258203d4017c3e843895a92b70aa74d1b7ebc9c982ccf2ec4968cc0cd55f12af4660c0301041a6553f100051a657b7e00"
const goldenNetworkKeyCertificateSignedBytes = "117369646572612f7265636f72642d736967197369646572612f6e6574776f726b2d6b65792d636572742f3159a50158208bfff7394412d3b94d49843e935680113278c5fae441e989308bde061be4f38202a201010258203d4017c3e843895a92b70aa74d1b7ebc9c982ccf2ec4968cc0cd55f12af4660c0301041a6553f100051a657b7e00"

// goldenDeviceCertificate: type sidera/device-cert/1, signed by key 3.
// Signed bytes: 0x11 "sidera/record-sig" 0x14 "sidera/device-cert/1" uvarint(89) payload.
// Digest: SHA-256 of the same layout with the domain "sidera/record-digest".
var goldenDeviceCertificate = hexcat(
	"a3", // { 3 pairs: the envelope
	"01", // 1: type
	"747369646572612f6465766963652d636572742f31", // "sidera/device-cert/1"
	"02",   // 2: payload
	"5859", // bstr(89)
	"a5",   // payload: { 5 pairs
	"01",   // payload: 1: person
	"582069889e61c6d596f9dbd04509e9e2b3ad10995b59ea01e2ba8c36364ccc2667e2", // payload: h'69889e61c6d596f9dbd04509e9e2b3ad10995b59ea01e2ba8c36364ccc2667e2' (person ID of RFC 8032 key 3)
	"02", // payload: 2: device key
	"a201010258203d4017c3e843895a92b70aa74d1b7ebc9c982ccf2ec4968cc0cd55f12af4660c", // payload: {1: 1, 2: h'3d4017c3e843895a92b70aa74d1b7ebc9c982ccf2ec4968cc0cd55f12af4660c'} (RFC 8032 key 2)
	"03",         // payload: 3: max level
	"02",         // payload: 2 (manage)
	"04",         // payload: 4: not_before
	"1a6553f100", // payload: 1700000000
	"05",         // payload: 5: not_after
	"1a65540d20", // payload: 1700007200 (2 hours later)
	"03",         // 3: signatures
	"81",         // [ 1 signature(s), sorted by encoded public key
	"a2",         // { signature item
	"01",         // 1: public key
	"a20101025820fc51cd8e6218a1a38da47ed00230f0580816ed13ba3303ac5deb911548908025", // {1: 1 (Ed25519), 2: h'fc51cd8e6218a1a38da47ed00230f0580816ed13ba3303ac5deb911548908025'} (RFC 8032 key 3)
	"02", // 2: signature
	"58406f539ea4036e37c646a523638f0da94fd1228204685d8413ef510616700447622c9964df54cec138e1585363afbde54da3ee42bd008e7e79629053811c71910a", // bstr(64) Ed25519 signature by key 3
)

const goldenDeviceCertificateDigest = "c75f2e67eccf601a72bc5c70f5ea108252ee69c6b05a3b94f9745186cff7779d"
const goldenDeviceCertificatePayload = "a501582069889e61c6d596f9dbd04509e9e2b3ad10995b59ea01e2ba8c36364ccc2667e202a201010258203d4017c3e843895a92b70aa74d1b7ebc9c982ccf2ec4968cc0cd55f12af4660c0302041a6553f100051a65540d20"
const goldenDeviceCertificateSignedBytes = "117369646572612f7265636f72642d736967147369646572612f6465766963652d636572742f3159a501582069889e61c6d596f9dbd04509e9e2b3ad10995b59ea01e2ba8c36364ccc2667e202a201010258203d4017c3e843895a92b70aa74d1b7ebc9c982ccf2ec4968cc0cd55f12af4660c0302041a6553f100051a65540d20"

// goldenPairComplete: type sidera/test/pair/1, signed by key 1, key 2.
// Signed bytes: 0x11 "sidera/record-sig" 0x12 "sidera/test/pair/1" uvarint(71) payload.
// Digest: SHA-256 of the same layout with the domain "sidera/record-digest".
var goldenPairComplete = hexcat(
	"a3",                                     // { 3 pairs: the envelope
	"01",                                     // 1: type
	"727369646572612f746573742f706169722f31", // "sidera/test/pair/1"
	"02",                                     // 2: payload
	"5847",                                   // bstr(71)
	"a2",                                     // payload: { 2 pairs
	"01",                                     // payload: 1: first node
	"58208bfff7394412d3b94d49843e935680113278c5fae441e989308bde061be4f382", // payload: h'8bfff7394412d3b94d49843e935680113278c5fae441e989308bde061be4f382' (node ID of RFC 8032 key 1)
	"02", // payload: 2: second node
	"5820eb6e331ecb9a006226db8afdd19181735baf296bc86102b4fbe7e2188ddb2b89", // payload: h'eb6e331ecb9a006226db8afdd19181735baf296bc86102b4fbe7e2188ddb2b89' (node ID of RFC 8032 key 2)
	"03", // 3: signatures
	"82", // [ 2 signature(s), sorted by encoded public key
	"a2", // { signature item
	"01", // 1: public key
	"a201010258203d4017c3e843895a92b70aa74d1b7ebc9c982ccf2ec4968cc0cd55f12af4660c", // {1: 1 (Ed25519), 2: h'3d4017c3e843895a92b70aa74d1b7ebc9c982ccf2ec4968cc0cd55f12af4660c'} (RFC 8032 key 2)
	"02", // 2: signature
	"58408caf1656d66f5ec7b4d5010ca23f2f8e1a7a09ea3c15cd70c8cf4522eb27c05c92ae94c907da78af6accb22fa3a871e574e1712b0d8269e5c9dca8fef2249f0f", // bstr(64) Ed25519 signature by key 2
	"a2", // { signature item
	"01", // 1: public key
	"a20101025820d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a", // {1: 1 (Ed25519), 2: h'd75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a'} (RFC 8032 key 1)
	"02", // 2: signature
	"584007eac24e6eb09457f543d690f97ce79dae7baf6cff375321b7f21429411d83ac9c5833df1a7e47c760f740d87112409524466ab88ad65914f1f490bef8a47d08", // bstr(64) Ed25519 signature by key 1
)

const goldenPairCompleteDigest = "df97ba38bbf16c51aeb634951cc5c54b140fff43225eb8f99e5a496017e41224"
const goldenPairCompletePayload = "a20158208bfff7394412d3b94d49843e935680113278c5fae441e989308bde061be4f382025820eb6e331ecb9a006226db8afdd19181735baf296bc86102b4fbe7e2188ddb2b89"
const goldenPairCompleteSignedBytes = "117369646572612f7265636f72642d736967127369646572612f746573742f706169722f3147a20158208bfff7394412d3b94d49843e935680113278c5fae441e989308bde061be4f382025820eb6e331ecb9a006226db8afdd19181735baf296bc86102b4fbe7e2188ddb2b89"

// goldenPairFirstOnly: type sidera/test/pair/1, signed by key 1.
// Signed bytes: 0x11 "sidera/record-sig" 0x12 "sidera/test/pair/1" uvarint(71) payload.
// Digest: SHA-256 of the same layout with the domain "sidera/record-digest".
var goldenPairFirstOnly = hexcat(
	"a3",                                     // { 3 pairs: the envelope
	"01",                                     // 1: type
	"727369646572612f746573742f706169722f31", // "sidera/test/pair/1"
	"02",                                     // 2: payload
	"5847",                                   // bstr(71)
	"a2",                                     // payload: { 2 pairs
	"01",                                     // payload: 1: first node
	"58208bfff7394412d3b94d49843e935680113278c5fae441e989308bde061be4f382", // payload: h'8bfff7394412d3b94d49843e935680113278c5fae441e989308bde061be4f382' (node ID of RFC 8032 key 1)
	"02", // payload: 2: second node
	"5820eb6e331ecb9a006226db8afdd19181735baf296bc86102b4fbe7e2188ddb2b89", // payload: h'eb6e331ecb9a006226db8afdd19181735baf296bc86102b4fbe7e2188ddb2b89' (node ID of RFC 8032 key 2)
	"03", // 3: signatures
	"81", // [ 1 signature(s), sorted by encoded public key
	"a2", // { signature item
	"01", // 1: public key
	"a20101025820d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a", // {1: 1 (Ed25519), 2: h'd75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a'} (RFC 8032 key 1)
	"02", // 2: signature
	"584007eac24e6eb09457f543d690f97ce79dae7baf6cff375321b7f21429411d83ac9c5833df1a7e47c760f740d87112409524466ab88ad65914f1f490bef8a47d08", // bstr(64) Ed25519 signature by key 1
)

const goldenPairFirstOnlyDigest = "df97ba38bbf16c51aeb634951cc5c54b140fff43225eb8f99e5a496017e41224"
const goldenPairFirstOnlyPayload = "a20158208bfff7394412d3b94d49843e935680113278c5fae441e989308bde061be4f382025820eb6e331ecb9a006226db8afdd19181735baf296bc86102b4fbe7e2188ddb2b89"
const goldenPairFirstOnlySignedBytes = "117369646572612f7265636f72642d736967127369646572612f746573742f706169722f3147a20158208bfff7394412d3b94d49843e935680113278c5fae441e989308bde061be4f382025820eb6e331ecb9a006226db8afdd19181735baf296bc86102b4fbe7e2188ddb2b89"
