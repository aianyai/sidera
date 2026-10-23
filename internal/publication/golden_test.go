package publication

import "encoding/hex"

// Golden vectors. Every byte below was written out by hand from the format
// description, and the identifiers, digests and signatures were produced by an
// independent implementation (Python: SHA-256 from the standard library,
// hand-built CBOR, and the cryptography package's Ed25519), never by the code
// under test. Keys are the RFC 8032 test keys 1, 2 and 3, and seeds made of one
// byte repeated 32 times. Identifiers are SHA-256 of the framed domain, the
// algorithm byte and the key.

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

// goldenDeclaration: node A's declaration, signed by A's network key (seed 0x11).
// Signed bytes: 0x11 "sidera/record-sig" uvarint(len(type)) type uvarint(len(payload)) payload.
// Digest: SHA-256 of the same layout with the domain "sidera/record-digest".
var goldenDeclaration = hexcat(
	"a3", // { 3 pairs: the envelope
	"01", // 1: type
	"78197369646572612f6e6f64652d6465636c61726174696f6e2f31", // "sidera/node-declaration/1"
	"02",   // 2: payload
	"5878", // bstr(120)
	"a8",   // payload: { 8 pairs
	"01",   // payload: 1: node
	"5820e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d", // payload: h'e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d' (node ID of RFC 8032 key 1)
	"02",                                 // payload: 2: counter
	"05",                                 // payload: 5
	"03",                                 // payload: 3: boot nonce
	"50a0a1a2a3a4a5a6a7a8a9aaabacadaeaf", // payload: h'a0a1a2a3a4a5a6a7a8a9aaabacadaeaf'
	"04",                                 // payload: 4: accepts inheritance
	"f5",                                 // payload: true
	"05",                                 // payload: 5: offers relay
	"f4",                                 // payload: false
	"06",                                 // payload: 6: max queue secs
	"191c20",                             // payload: 7200 (the protocol cap)
	"07",                                 // payload: 7: lease renewal secs
	"190384",                             // payload: 900 (the protocol cap)
	"08",                                 // payload: 8: endpoints
	"82",                                 // payload: [ 2 endpoints, ascending by ID
	"a4",                                 // payload: { 4 pairs
	"01",                                 // payload: 1: id
	"5001010101010101010101010101010101", // payload: h'01010101010101010101010101010101'
	"02",                                 // payload: 2: kind
	"01",                                 // payload: 1 (session)
	"03",                                 // payload: 3: name
	"646d61696e",                         // payload: "main"
	"04",                                 // payload: 4: reception
	"01",                                 // payload: 1 (paused)
	"a2",                                 // payload: { 2 pairs
	"01",                                 // payload: 1: id
	"5002020202020202020202020202020202", // payload: h'02020202020202020202020202020202'
	"02",                                 // payload: 2: kind
	"02",                                 // payload: 2 (runtime)
	"03",                                 // 3: signatures
	"81",                                 // [ 1 signature(s), sorted by encoded public key
	"a2",                                 // { signature item
	"01",                                 // 1: public key
	"a2",                                 // { 2 pairs: public key
	"01",                                 // 1: algorithm
	"01",                                 // 1 (Ed25519)
	"02",                                 // 2: key bytes
	"5820d04ab232742bb4ab3a1368bd4615e4e6d0224ab71a016baf8520a332c9778737", // h'd04ab232742bb4ab3a1368bd4615e4e6d0224ab71a016baf8520a332c9778737' (seed 0x11 x32)
	"02", // 2: signature
	"584086afd17129c29745cc8da0d1154750797d523aca38c3cbc0de08cfb41aa86d037b46359753a571f249ffcbdf6d96ca89f9675a4db190ea0dec2416c12fb04a00", // bstr(64) Ed25519 signature by seed 0x11 x32
)

const goldenDeclarationDigest = "dd3911ce789b48339bd055cec9c4e5df3a7191a55eaf797260d86a928d95b659"
const goldenDeclarationSignedBytes = "117369646572612f7265636f72642d736967197369646572612f6e6f64652d6465636c61726174696f6e2f3178a8015820e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d02050350a0a1a2a3a4a5a6a7a8a9aaabacadaeaf04f505f406191c20071903840882a4015001010101010101010101010101010101020103646d61696e0401a20150020202020202020202020202020202020202"

// goldenReachability: node A's reachability record, signed by A's network key (seed 0x11), with the certificate embedded.
// Signed bytes: 0x11 "sidera/record-sig" uvarint(len(type)) type uvarint(len(payload)) payload.
// Digest: SHA-256 of the same layout with the domain "sidera/record-digest".
var goldenReachability = hexcat(
	"a3", // { 3 pairs: the envelope
	"01", // 1: type
	"781c7369646572612f72656163686162696c6974792d7265636f72642f31", // "sidera/reachability-record/1"
	"02",         // 2: payload
	"590154",     // bstr(340)
	"a5",         // payload: { 5 pairs
	"01",         // payload: 1: counter
	"03",         // payload: 3
	"02",         // payload: 2: expires_at
	"1a6553ff10", // payload: 1700003600 (one hour later)
	"03",         // payload: 3: network-key certificate
	"58e6",       // payload: bstr(230) the embedded network-key certificate record
	"a3",         // payload: embedded { 3 pairs: the envelope
	"01",         // payload: embedded 1: type
	"78197369646572612f6e6574776f726b2d6b65792d636572742f31", // payload: embedded "sidera/network-key-cert/1"
	"02",   // payload: embedded 2: payload
	"5859", // payload: embedded bstr(89)
	"a5",   // payload: embedded payload: { 5 pairs
	"01",   // payload: embedded payload: 1: node
	"5820e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d", // payload: embedded payload: h'e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d' (node ID of RFC 8032 key 1)
	"02", // payload: embedded payload: 2: network key
	"a2", // payload: embedded payload: { 2 pairs: public key
	"01", // payload: embedded payload: 1: algorithm
	"01", // payload: embedded payload: 1 (Ed25519)
	"02", // payload: embedded payload: 2: key bytes
	"5820d04ab232742bb4ab3a1368bd4615e4e6d0224ab71a016baf8520a332c9778737", // payload: embedded payload: h'd04ab232742bb4ab3a1368bd4615e4e6d0224ab71a016baf8520a332c9778737' (seed 0x11 x32)
	"03",         // payload: embedded payload: 3: serial
	"01",         // payload: embedded payload: 1
	"04",         // payload: embedded payload: 4: not_before
	"1a6553f100", // payload: embedded payload: 1700000000
	"05",         // payload: embedded payload: 5: not_after
	"1a657b7e00", // payload: embedded payload: 1702592000 (30 days later)
	"03",         // payload: embedded 3: signatures
	"81",         // payload: embedded [ 1 signature(s), sorted by encoded public key
	"a2",         // payload: embedded { signature item
	"01",         // payload: embedded 1: public key
	"a2",         // payload: embedded { 2 pairs: public key
	"01",         // payload: embedded 1: algorithm
	"01",         // payload: embedded 1 (Ed25519)
	"02",         // payload: embedded 2: key bytes
	"5820d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a", // payload: embedded h'd75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a' (RFC 8032 key 1)
	"02", // payload: embedded 2: signature
	"58401a5edd7469659ee737b67cd63f286ef547ee1acfe571b7b83c8b17465bbf5b3ca3346215be328dd7e28cc29fc4038b5763b0be43e424a6ea98554cfa870e6703", // payload: embedded bstr(64) Ed25519 signature by RFC 8032 key 1
	"04", // payload: 4: addresses
	"82", // payload: [ 2 addresses, most preferred first
	"581f2f6970342f3139322e302e322e312f7564702f343030312f717569632d7631", // payload: h'2f6970342f3139322e302e322e312f7564702f343030312f717569632d7631' "/ip4/192.0.2.1/udp/4001/quic-v1"
	"58192f6970362f323030313a6462383a3a312f7463702f34303031",             // payload: h'2f6970362f323030313a6462383a3a312f7463702f34303031' "/ip6/2001:db8::1/tcp/4001"
	"05", // payload: 5: relays
	"81", // payload: [ 1 reserved relays
	"582040a7fc60420d2a1d266e61f7613bc43e79ffaba9dc7324c2e14c1d4d55e8b99d", // payload: h'40a7fc60420d2a1d266e61f7613bc43e79ffaba9dc7324c2e14c1d4d55e8b99d' (node ID of RFC 8032 key 3)
	"03", // 3: signatures
	"81", // [ 1 signature(s), sorted by encoded public key
	"a2", // { signature item
	"01", // 1: public key
	"a2", // { 2 pairs: public key
	"01", // 1: algorithm
	"01", // 1 (Ed25519)
	"02", // 2: key bytes
	"5820d04ab232742bb4ab3a1368bd4615e4e6d0224ab71a016baf8520a332c9778737", // h'd04ab232742bb4ab3a1368bd4615e4e6d0224ab71a016baf8520a332c9778737' (seed 0x11 x32)
	"02", // 2: signature
	"58405eb19529314f2fa55d7817710b4d41e8cca1bbc7e8813c7021155f217304daec25760ec83dca54dad6c0e0e8cdf01deba0a8d8a25026aed62c98515136b3d50b", // bstr(64) Ed25519 signature by seed 0x11 x32
)

const goldenReachabilityDigest = "5ff707141c3565458926fbdf3bab68c16839bb3adc4f27ed105a17db77062e3e"
const goldenReachabilitySignedBytes = "117369646572612f7265636f72642d7369671c7369646572612f72656163686162696c6974792d7265636f72642f31d402a50103021a6553ff100358e6a30178197369646572612f6e6574776f726b2d6b65792d636572742f31025859a5015820e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d02a20101025820d04ab232742bb4ab3a1368bd4615e4e6d0224ab71a016baf8520a332c97787370301041a6553f100051a657b7e000381a201a20101025820d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a0258401a5edd7469659ee737b67cd63f286ef547ee1acfe571b7b83c8b17465bbf5b3ca3346215be328dd7e28cc29fc4038b5763b0be43e424a6ea98554cfa870e67030482581f2f6970342f3139322e302e322e312f7564702f343030312f717569632d763158192f6970362f323030313a6462383a3a312f7463702f343030310581582040a7fc60420d2a1d266e61f7613bc43e79ffaba9dc7324c2e14c1d4d55e8b99d"

// goldenNetworkKeyCertificate: the network-key certificate record embedded in goldenReachability, signed by key 1.
// Signed bytes: 0x11 "sidera/record-sig" uvarint(len(type)) type uvarint(len(payload)) payload.
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
	"a2", // payload: { 2 pairs: public key
	"01", // payload: 1: algorithm
	"01", // payload: 1 (Ed25519)
	"02", // payload: 2: key bytes
	"5820d04ab232742bb4ab3a1368bd4615e4e6d0224ab71a016baf8520a332c9778737", // payload: h'd04ab232742bb4ab3a1368bd4615e4e6d0224ab71a016baf8520a332c9778737' (seed 0x11 x32)
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
	"a2",         // { 2 pairs: public key
	"01",         // 1: algorithm
	"01",         // 1 (Ed25519)
	"02",         // 2: key bytes
	"5820d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a", // h'd75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a' (RFC 8032 key 1)
	"02", // 2: signature
	"58401a5edd7469659ee737b67cd63f286ef547ee1acfe571b7b83c8b17465bbf5b3ca3346215be328dd7e28cc29fc4038b5763b0be43e424a6ea98554cfa870e6703", // bstr(64) Ed25519 signature by RFC 8032 key 1
)

const goldenNetworkKeyCertificateDigest = "100b260f4c511364cbde6a2bd3d67b6b003053473c703d728ddef11b705b5b49"
const goldenNetworkKeyCertificateSignedBytes = "117369646572612f7265636f72642d736967197369646572612f6e6574776f726b2d6b65792d636572742f3159a5015820e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d02a20101025820d04ab232742bb4ab3a1368bd4615e4e6d0224ab71a016baf8520a332c97787370301041a6553f100051a657b7e00"

// goldenProof: a reachability proof for the person of key 3 on node A, signed by A's identity key (key 1).
// Signed bytes: 0x11 "sidera/record-sig" uvarint(len(type)) type uvarint(len(payload)) payload.
// Digest: SHA-256 of the same layout with the domain "sidera/record-digest".
var goldenProof = hexcat(
	"a3", // { 3 pairs: the envelope
	"01", // 1: type
	"781b7369646572612f72656163686162696c6974792d70726f6f662f31", // "sidera/reachability-proof/1"
	"02",   // 2: payload
	"584d", // bstr(77)
	"a3",   // payload: { 3 pairs
	"01",   // payload: 1: person
	"58200300c202af68893402261cf312f5ffb2e956064e997abfe28ef485cfd179e39d", // payload: h'0300c202af68893402261cf312f5ffb2e956064e997abfe28ef485cfd179e39d' (person ID of RFC 8032 key 3)
	"02", // payload: 2: node
	"5820e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d", // payload: h'e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d' (node ID of RFC 8032 key 1)
	"03",         // payload: 3: not_after
	"1a65554280", // payload: 1700086400 (one day later)
	"03",         // 3: signatures
	"81",         // [ 1 signature(s), sorted by encoded public key
	"a2",         // { signature item
	"01",         // 1: public key
	"a2",         // { 2 pairs: public key
	"01",         // 1: algorithm
	"01",         // 1 (Ed25519)
	"02",         // 2: key bytes
	"5820d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a", // h'd75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a' (RFC 8032 key 1)
	"02", // 2: signature
	"584093b3121696203e8c9c14abd4c4840ac14eaae02f0c4143552480e6d56e36de3450accfd2695c6c6e8903bee4b2b75399e4e50a96be701517aa3e03d9a25b450f", // bstr(64) Ed25519 signature by RFC 8032 key 1
)

const goldenProofDigest = "ba6dfc6b1e92500db3c1b81150ff90d378caef0c5144306dd887c46c844b8f1d"
const goldenProofSignedBytes = "117369646572612f7265636f72642d7369671b7369646572612f72656163686162696c6974792d70726f6f662f314da30158200300c202af68893402261cf312f5ffb2e956064e997abfe28ef485cfd179e39d025820e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d031a65554280"
