package record

import "encoding/hex"

// Golden vectors. Every byte below was written out by hand from the format
// description, and the identifiers, digests and signatures were produced by an
// independent implementation (Python: SHA-256 from the standard library,
// hand-built CBOR, and the cryptography package's Ed25519), never by the code
// under test. Keys are the RFC 8032 test keys 1, 2 and 3. Identifiers are
// SHA-256 of the framed domain, the algorithm byte and the key.

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
	"5820e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d", // payload: h'e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d' (node ID of RFC 8032 key 1)
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
	"5840d341cca122fb3d35608e5e6dc3a2ad7cfdf2a566c6025c0a9fe3f5837823005f5e8f6bfbaa1bfb1dac1e42f1d3a4fb47504ac7f912eee8038c1821caf338370c", // bstr(64) Ed25519 signature by key 1
)

const goldenNetworkKeyCertificateDigest = "e6bdc451ac725c2be0632bcce2f18e30c5a0c3fe43265832adab4adb64ad9531"
const goldenNetworkKeyCertificatePayload = "a5015820e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d02a201010258203d4017c3e843895a92b70aa74d1b7ebc9c982ccf2ec4968cc0cd55f12af4660c0301041a6553f100051a657b7e00"
const goldenNetworkKeyCertificateSignedBytes = "117369646572612f7265636f72642d736967197369646572612f6e6574776f726b2d6b65792d636572742f3159a5015820e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d02a201010258203d4017c3e843895a92b70aa74d1b7ebc9c982ccf2ec4968cc0cd55f12af4660c0301041a6553f100051a657b7e00"

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
	"58200300c202af68893402261cf312f5ffb2e956064e997abfe28ef485cfd179e39d", // payload: h'0300c202af68893402261cf312f5ffb2e956064e997abfe28ef485cfd179e39d' (person ID of RFC 8032 key 3)
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
	"5840ebb3d11b2c6f4b713452cfab2b5f5e24560260264efee5212eda4643077667fd47e571e1b909160bf8ea6e0aabfc3b06380e8ed3e830c9c13a39c92369cb0007", // bstr(64) Ed25519 signature by key 3
)

const goldenDeviceCertificateDigest = "299495928181176f57f10002fc21adaae392277535c69bcff2962fefb0db7cc3"
const goldenDeviceCertificatePayload = "a50158200300c202af68893402261cf312f5ffb2e956064e997abfe28ef485cfd179e39d02a201010258203d4017c3e843895a92b70aa74d1b7ebc9c982ccf2ec4968cc0cd55f12af4660c0302041a6553f100051a65540d20"
const goldenDeviceCertificateSignedBytes = "117369646572612f7265636f72642d736967147369646572612f6465766963652d636572742f3159a50158200300c202af68893402261cf312f5ffb2e956064e997abfe28ef485cfd179e39d02a201010258203d4017c3e843895a92b70aa74d1b7ebc9c982ccf2ec4968cc0cd55f12af4660c0302041a6553f100051a65540d20"

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
	"5820e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d", // payload: h'e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d' (node ID of RFC 8032 key 1)
	"02", // payload: 2: second node
	"582033ece159526afe25ff9f6661c01b7751f0a143821e5b7dd1c051debf37dd13bc", // payload: h'33ece159526afe25ff9f6661c01b7751f0a143821e5b7dd1c051debf37dd13bc' (node ID of RFC 8032 key 2)
	"03", // 3: signatures
	"82", // [ 2 signature(s), sorted by encoded public key
	"a2", // { signature item
	"01", // 1: public key
	"a201010258203d4017c3e843895a92b70aa74d1b7ebc9c982ccf2ec4968cc0cd55f12af4660c", // {1: 1 (Ed25519), 2: h'3d4017c3e843895a92b70aa74d1b7ebc9c982ccf2ec4968cc0cd55f12af4660c'} (RFC 8032 key 2)
	"02", // 2: signature
	"58407b82608d731e78d1484c0e0380fdf09111f5eb35c4ed0d0d15bae66973ac24a6644be53141dadafd72cc86cf46025763d2ca2305d5e747a840912337c97cd508", // bstr(64) Ed25519 signature by key 2
	"a2", // { signature item
	"01", // 1: public key
	"a20101025820d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a", // {1: 1 (Ed25519), 2: h'd75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a'} (RFC 8032 key 1)
	"02", // 2: signature
	"5840b5b00360f95e40b92a70c224a97f164f4435e730623e64015854871e9fbbb586fe760c6dfecbe68fb5d1bd71861b8b987ab74eb00190200afb7ade1773f93d0a", // bstr(64) Ed25519 signature by key 1
)

const goldenPairCompleteDigest = "5e76a9a744879528dd8f241761c59bab805e2835b3f455c67b070d36b3f746fb"
const goldenPairCompletePayload = "a2015820e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d02582033ece159526afe25ff9f6661c01b7751f0a143821e5b7dd1c051debf37dd13bc"
const goldenPairCompleteSignedBytes = "117369646572612f7265636f72642d736967127369646572612f746573742f706169722f3147a2015820e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d02582033ece159526afe25ff9f6661c01b7751f0a143821e5b7dd1c051debf37dd13bc"

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
	"5820e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d", // payload: h'e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d' (node ID of RFC 8032 key 1)
	"02", // payload: 2: second node
	"582033ece159526afe25ff9f6661c01b7751f0a143821e5b7dd1c051debf37dd13bc", // payload: h'33ece159526afe25ff9f6661c01b7751f0a143821e5b7dd1c051debf37dd13bc' (node ID of RFC 8032 key 2)
	"03", // 3: signatures
	"81", // [ 1 signature(s), sorted by encoded public key
	"a2", // { signature item
	"01", // 1: public key
	"a20101025820d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a", // {1: 1 (Ed25519), 2: h'd75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a'} (RFC 8032 key 1)
	"02", // 2: signature
	"5840b5b00360f95e40b92a70c224a97f164f4435e730623e64015854871e9fbbb586fe760c6dfecbe68fb5d1bd71861b8b987ab74eb00190200afb7ade1773f93d0a", // bstr(64) Ed25519 signature by key 1
)

const goldenPairFirstOnlyDigest = "5e76a9a744879528dd8f241761c59bab805e2835b3f455c67b070d36b3f746fb"
const goldenPairFirstOnlyPayload = "a2015820e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d02582033ece159526afe25ff9f6661c01b7751f0a143821e5b7dd1c051debf37dd13bc"
const goldenPairFirstOnlySignedBytes = "117369646572612f7265636f72642d736967127369646572612f746573742f706169722f3147a2015820e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d02582033ece159526afe25ff9f6661c01b7751f0a143821e5b7dd1c051debf37dd13bc"
