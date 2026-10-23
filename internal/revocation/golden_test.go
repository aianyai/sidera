package revocation

import "encoding/hex"

// Golden vectors. Every byte below was written out by hand from the format
// description, and the identifiers, digests and signatures were produced by an
// independent implementation (Python: SHA-256 from the standard library,
// hand-built CBOR, and the cryptography package's Ed25519), never by the code
// under test. Keys are the RFC 8032 test keys 1, 2 and 3. Identifiers are
// SHA-256 of the framed domain, the algorithm tag and the key.

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

// goldenNodeSelfRevocation: type sidera/node-self-revocation/1, signed by key 1.
// The node's identity key declares itself void.
// Signed bytes: 0x11 "sidera/record-sig" uvarint(29) "sidera/node-self-revocation/1" uvarint(36) payload.
// Digest: SHA-256 of the same layout with the domain "sidera/record-digest".
var goldenNodeSelfRevocation = hexcat(
	"a3", // { 3 pairs: the envelope
	"01", // 1: type
	"781d7369646572612f6e6f64652d73656c662d7265766f636174696f6e2f31", // "sidera/node-self-revocation/1"
	"02",   // 2: payload
	"5824", // bstr(36)
	"a1",   // payload: { 1 pair
	"01",   // payload: 1: node
	"5820e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d", // payload: h'e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d' (node ID of RFC 8032 key 1)
	"03", // 3: signatures
	"81", // [ 1 signature(s), sorted by encoded public key
	"a2", // { signature item
	"01", // 1: public key
	"a20101025820d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a", // {1: 1 (Ed25519), 2: h'd75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a'} (RFC 8032 key 1)
	"02", // 2: signature
	"58402d10ff94a6b6f52556b407f16e0e2861be58182b181c1a0c9a92de7a0d9688091912dd9986bc3ec32fd17370f6670f309a312870abe5d3b65423d5705cebc804", // bstr(64) Ed25519 signature by key 1
)

const goldenNodeSelfRevocationPayload = "a1015820e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d"
const goldenNodeSelfRevocationDigest = "9c98b22ce57911e44a3ad0f641c752bb7b97b9a5f33a3077917378b59a6a82d5"
const goldenNodeSelfRevocationSignedBytes = "117369646572612f7265636f72642d7369671d7369646572612f6e6f64652d73656c662d7265766f636174696f6e2f3124a1015820e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d"

// goldenPersonSelfRevocation: type sidera/person-self-revocation/1, signed by key 3.
// The person's root key declares itself void.
// Signed bytes: 0x11 "sidera/record-sig" uvarint(31) "sidera/person-self-revocation/1" uvarint(36) payload.
// Digest: SHA-256 of the same layout with the domain "sidera/record-digest".
var goldenPersonSelfRevocation = hexcat(
	"a3", // { 3 pairs: the envelope
	"01", // 1: type
	"781f7369646572612f706572736f6e2d73656c662d7265766f636174696f6e2f31", // "sidera/person-self-revocation/1"
	"02",   // 2: payload
	"5824", // bstr(36)
	"a1",   // payload: { 1 pair
	"01",   // payload: 1: person
	"58200300c202af68893402261cf312f5ffb2e956064e997abfe28ef485cfd179e39d", // payload: h'0300c202af68893402261cf312f5ffb2e956064e997abfe28ef485cfd179e39d' (person ID of RFC 8032 key 3)
	"03", // 3: signatures
	"81", // [ 1 signature(s), sorted by encoded public key
	"a2", // { signature item
	"01", // 1: public key
	"a20101025820fc51cd8e6218a1a38da47ed00230f0580816ed13ba3303ac5deb911548908025", // {1: 1 (Ed25519), 2: h'fc51cd8e6218a1a38da47ed00230f0580816ed13ba3303ac5deb911548908025'} (RFC 8032 key 3)
	"02", // 2: signature
	"584090aa28aa6a4b47d665eb6e729f116d66de79db232e8ee6e09a8ee5a18fa1156f282227ada86eaac6d4006920586d912009909944b795e1066a0f154760fda20f", // bstr(64) Ed25519 signature by key 3
)

const goldenPersonSelfRevocationPayload = "a10158200300c202af68893402261cf312f5ffb2e956064e997abfe28ef485cfd179e39d"
const goldenPersonSelfRevocationDigest = "b5635739ad3fac5107bfc5461bcb63f71e53cf6a03bee746c2615232a1ce7e99"
const goldenPersonSelfRevocationSignedBytes = "117369646572612f7265636f72642d7369671f7369646572612f706572736f6e2d73656c662d7265766f636174696f6e2f3124a10158200300c202af68893402261cf312f5ffb2e956064e997abfe28ef485cfd179e39d"

// goldenNetworkKeyCertificateRevocation: type sidera/network-key-cert-revocation/1, signed by key 1.
// Revokes the certificate whose digest is in field 3.
// Signed bytes: 0x11 "sidera/record-sig" uvarint(36) "sidera/network-key-cert-revocation/1" uvarint(73) payload.
// Digest: SHA-256 of the same layout with the domain "sidera/record-digest".
var goldenNetworkKeyCertificateRevocation = hexcat(
	"a3", // { 3 pairs: the envelope
	"01", // 1: type
	"78247369646572612f6e6574776f726b2d6b65792d636572742d7265766f636174696f6e2f31", // "sidera/network-key-cert-revocation/1"
	"02",   // 2: payload
	"5849", // bstr(73)
	"a3",   // payload: { 3 pairs
	"01",   // payload: 1: node
	"5820e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d", // payload: h'e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d' (node ID of RFC 8032 key 1)
	"02", // payload: 2: serial
	"01", // payload: 1
	"03", // payload: 3: certificate digest
	"5820e6bdc451ac725c2be0632bcce2f18e30c5a0c3fe43265832adab4adb64ad9531", // payload: h'e6bdc451ac725c2be0632bcce2f18e30c5a0c3fe43265832adab4adb64ad9531' (digest of the network-key certificate with serial 1 for node 1 naming key 2, valid 1700000000 to 1702592000)
	"03", // 3: signatures
	"81", // [ 1 signature(s), sorted by encoded public key
	"a2", // { signature item
	"01", // 1: public key
	"a20101025820d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a", // {1: 1 (Ed25519), 2: h'd75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a'} (RFC 8032 key 1)
	"02", // 2: signature
	"5840b40acc886e3e309d6ed663c55c82a6a07ee43c1c4bba6a7f558376d8cbbdd212a393f5e9362cfb94235adffd68b46f3f9ed1cf69c684448df06bc6cad60a0e07", // bstr(64) Ed25519 signature by key 1
)

const goldenNetworkKeyCertificateRevocationPayload = "a3015820e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d0201035820e6bdc451ac725c2be0632bcce2f18e30c5a0c3fe43265832adab4adb64ad9531"
const goldenNetworkKeyCertificateRevocationDigest = "167214de871abfd2088b86d8663f0dfbebf80fc5941763c896e107437ab7dcd5"
const goldenNetworkKeyCertificateRevocationSignedBytes = "117369646572612f7265636f72642d736967247369646572612f6e6574776f726b2d6b65792d636572742d7265766f636174696f6e2f3149a3015820e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d0201035820e6bdc451ac725c2be0632bcce2f18e30c5a0c3fe43265832adab4adb64ad9531"

// goldenDeviceKeyRevocation: type sidera/device-key-revocation/1, signed by key 3.
// The root key of the person revokes the device key.
// Signed bytes: 0x11 "sidera/record-sig" uvarint(30) "sidera/device-key-revocation/1" uvarint(75) payload.
// Digest: SHA-256 of the same layout with the domain "sidera/record-digest".
var goldenDeviceKeyRevocation = hexcat(
	"a3", // { 3 pairs: the envelope
	"01", // 1: type
	"781e7369646572612f6465766963652d6b65792d7265766f636174696f6e2f31", // "sidera/device-key-revocation/1"
	"02",   // 2: payload
	"584b", // bstr(75)
	"a2",   // payload: { 2 pairs
	"01",   // payload: 1: person
	"58200300c202af68893402261cf312f5ffb2e956064e997abfe28ef485cfd179e39d", // payload: h'0300c202af68893402261cf312f5ffb2e956064e997abfe28ef485cfd179e39d' (person ID of RFC 8032 key 3)
	"02", // payload: 2: device key
	"a201010258203d4017c3e843895a92b70aa74d1b7ebc9c982ccf2ec4968cc0cd55f12af4660c", // payload: {1: 1, 2: h'3d4017c3e843895a92b70aa74d1b7ebc9c982ccf2ec4968cc0cd55f12af4660c'} (RFC 8032 key 2)
	"03", // 3: signatures
	"81", // [ 1 signature(s), sorted by encoded public key
	"a2", // { signature item
	"01", // 1: public key
	"a20101025820fc51cd8e6218a1a38da47ed00230f0580816ed13ba3303ac5deb911548908025", // {1: 1 (Ed25519), 2: h'fc51cd8e6218a1a38da47ed00230f0580816ed13ba3303ac5deb911548908025'} (RFC 8032 key 3)
	"02", // 2: signature
	"584009589ab33cb280a711369ff9a982ad140017e5e3a785166e84492956d0f30975edc346beb84a830a4ce2400e0da93d43af33f261b8931043c8ceb4ddc416c305", // bstr(64) Ed25519 signature by key 3
)

const goldenDeviceKeyRevocationPayload = "a20158200300c202af68893402261cf312f5ffb2e956064e997abfe28ef485cfd179e39d02a201010258203d4017c3e843895a92b70aa74d1b7ebc9c982ccf2ec4968cc0cd55f12af4660c"
const goldenDeviceKeyRevocationDigest = "eecfd72fb5fde95ff84521f6870f10bc3f1942503f583679755aabac45c08340"
const goldenDeviceKeyRevocationSignedBytes = "117369646572612f7265636f72642d7369671e7369646572612f6465766963652d6b65792d7265766f636174696f6e2f314ba20158200300c202af68893402261cf312f5ffb2e956064e997abfe28ef485cfd179e39d02a201010258203d4017c3e843895a92b70aa74d1b7ebc9c982ccf2ec4968cc0cd55f12af4660c"
