package credential

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

// goldenCredentialPartial: a credential signed by its initiator (key 1) only; the target co-signs it. The ends are in ascending order of node ID, so the initiator's node is B.
// Signed bytes: 0x11 "sidera/record-sig" uvarint(len(type)) type uvarint(len(payload)) payload.
// Digest: SHA-256 of the same layout with the domain "sidera/record-digest".
var goldenCredentialPartial = hexcat(
	"a3", // { 3 pairs: the envelope
	"01", // 1: type
	"78197369646572612f74727573742d63726564656e7469616c2f31", // "sidera/trust-credential/1"
	"02",   // 2: payload
	"5865", // bstr(101)
	"a5",   // payload: { 5 pairs
	"01",   // payload: 1: end a (the smaller node ID)
	"582033ece159526afe25ff9f6661c01b7751f0a143821e5b7dd1c051debf37dd13bc", // payload: h'33ece159526afe25ff9f6661c01b7751f0a143821e5b7dd1c051debf37dd13bc'
	"02", // payload: 2: end b
	"5820e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d", // payload: h'e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d'
	"03",                                 // payload: 3: created_at
	"1a6553f100",                         // payload: 1700000000
	"04",                                 // payload: 4: not_after
	"1a65554280",                         // payload: 1700086400 (one day later)
	"05",                                 // payload: 5: nonce
	"50000102030405060708090a0b0c0d0e0f", // payload: h'000102030405060708090a0b0c0d0e0f'
	"03",                                 // 3: signatures
	"81",                                 // [ 1 signature(s), sorted by encoded public key
	"a2",                                 // { signature item
	"01",                                 // 1: public key
	"a2",                                 // { 2 pairs: public key
	"01",                                 // 1: algorithm
	"01",                                 // 1 (Ed25519)
	"02",                                 // 2: key bytes
	"5820d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a", // h'd75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a' (RFC 8032 key 1)
	"02", // 2: signature
	"5840a946b6b86aef56d5154bc2e78e2504669d806c1518acccacad4d7f4fc13d54dae30211327ee3bf28acc15884e60eaf9a5a17b27f74b7a7b3ca9de19e4b257d09", // bstr(64) Ed25519 signature by RFC 8032 key 1
)

const goldenCredentialPartialDigest = "c5f781fa6341bc4b09964ae0002244448d0d605dfcae24543f4bbdc336a81d67"
const goldenCredentialPartialSignedBytes = "117369646572612f7265636f72642d736967197369646572612f74727573742d63726564656e7469616c2f3165a501582033ece159526afe25ff9f6661c01b7751f0a143821e5b7dd1c051debf37dd13bc025820e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d031a6553f100041a655542800550000102030405060708090a0b0c0d0e0f"

// goldenCredential: the same credential once the target (key 2) has co-signed. Nonce 00..0f, not_after one day after created_at.
// Signed bytes: 0x11 "sidera/record-sig" uvarint(len(type)) type uvarint(len(payload)) payload.
// Digest: SHA-256 of the same layout with the domain "sidera/record-digest".
var goldenCredential = hexcat(
	"a3", // { 3 pairs: the envelope
	"01", // 1: type
	"78197369646572612f74727573742d63726564656e7469616c2f31", // "sidera/trust-credential/1"
	"02",   // 2: payload
	"5865", // bstr(101)
	"a5",   // payload: { 5 pairs
	"01",   // payload: 1: end a (the smaller node ID)
	"582033ece159526afe25ff9f6661c01b7751f0a143821e5b7dd1c051debf37dd13bc", // payload: h'33ece159526afe25ff9f6661c01b7751f0a143821e5b7dd1c051debf37dd13bc'
	"02", // payload: 2: end b
	"5820e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d", // payload: h'e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d'
	"03",                                 // payload: 3: created_at
	"1a6553f100",                         // payload: 1700000000
	"04",                                 // payload: 4: not_after
	"1a65554280",                         // payload: 1700086400 (one day later)
	"05",                                 // payload: 5: nonce
	"50000102030405060708090a0b0c0d0e0f", // payload: h'000102030405060708090a0b0c0d0e0f'
	"03",                                 // 3: signatures
	"82",                                 // [ 2 signature(s), sorted by encoded public key
	"a2",                                 // { signature item
	"01",                                 // 1: public key
	"a2",                                 // { 2 pairs: public key
	"01",                                 // 1: algorithm
	"01",                                 // 1 (Ed25519)
	"02",                                 // 2: key bytes
	"58203d4017c3e843895a92b70aa74d1b7ebc9c982ccf2ec4968cc0cd55f12af4660c", // h'3d4017c3e843895a92b70aa74d1b7ebc9c982ccf2ec4968cc0cd55f12af4660c' (RFC 8032 key 2)
	"02", // 2: signature
	"5840ae156a9a90e874b62edd37e81ca40a9386a3f4afa60212e81e8b33e4583b9bd9a0687098c73f005afa453d85d6ac6f5a396d20e805a214c5ad5b5edd45195707", // bstr(64) Ed25519 signature by RFC 8032 key 2
	"a2", // { signature item
	"01", // 1: public key
	"a2", // { 2 pairs: public key
	"01", // 1: algorithm
	"01", // 1 (Ed25519)
	"02", // 2: key bytes
	"5820d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a", // h'd75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a' (RFC 8032 key 1)
	"02", // 2: signature
	"5840a946b6b86aef56d5154bc2e78e2504669d806c1518acccacad4d7f4fc13d54dae30211327ee3bf28acc15884e60eaf9a5a17b27f74b7a7b3ca9de19e4b257d09", // bstr(64) Ed25519 signature by RFC 8032 key 1
)

const goldenCredentialDigest = "c5f781fa6341bc4b09964ae0002244448d0d605dfcae24543f4bbdc336a81d67"
const goldenCredentialSignedBytes = "117369646572612f7265636f72642d736967197369646572612f74727573742d63726564656e7469616c2f3165a501582033ece159526afe25ff9f6661c01b7751f0a143821e5b7dd1c051debf37dd13bc025820e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d031a6553f100041a655542800550000102030405060708090a0b0c0d0e0f"

// goldenCredentialNoEnd: a credential without not_after (it never expires): the key is left out. Nonce 10..1f.
// Signed bytes: 0x11 "sidera/record-sig" uvarint(len(type)) type uvarint(len(payload)) payload.
// Digest: SHA-256 of the same layout with the domain "sidera/record-digest".
var goldenCredentialNoEnd = hexcat(
	"a3", // { 3 pairs: the envelope
	"01", // 1: type
	"78197369646572612f74727573742d63726564656e7469616c2f31", // "sidera/trust-credential/1"
	"02",   // 2: payload
	"585f", // bstr(95)
	"a4",   // payload: { 4 pairs
	"01",   // payload: 1: end a (the smaller node ID)
	"582033ece159526afe25ff9f6661c01b7751f0a143821e5b7dd1c051debf37dd13bc", // payload: h'33ece159526afe25ff9f6661c01b7751f0a143821e5b7dd1c051debf37dd13bc'
	"02", // payload: 2: end b
	"5820e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d", // payload: h'e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d'
	"03",                                 // payload: 3: created_at
	"1a6553f100",                         // payload: 1700000000
	"05",                                 // payload: 5: nonce
	"50101112131415161718191a1b1c1d1e1f", // payload: h'101112131415161718191a1b1c1d1e1f'
	"03",                                 // 3: signatures
	"82",                                 // [ 2 signature(s), sorted by encoded public key
	"a2",                                 // { signature item
	"01",                                 // 1: public key
	"a2",                                 // { 2 pairs: public key
	"01",                                 // 1: algorithm
	"01",                                 // 1 (Ed25519)
	"02",                                 // 2: key bytes
	"58203d4017c3e843895a92b70aa74d1b7ebc9c982ccf2ec4968cc0cd55f12af4660c", // h'3d4017c3e843895a92b70aa74d1b7ebc9c982ccf2ec4968cc0cd55f12af4660c' (RFC 8032 key 2)
	"02", // 2: signature
	"5840cf86792d5c70263ecb6615aa9e5304a085fc12b34b314ed76d42d82ddde62227a0b003696d141ac4a5d23f66c07a20c9928e90ca910e053235aab4806b72d20f", // bstr(64) Ed25519 signature by RFC 8032 key 2
	"a2", // { signature item
	"01", // 1: public key
	"a2", // { 2 pairs: public key
	"01", // 1: algorithm
	"01", // 1 (Ed25519)
	"02", // 2: key bytes
	"5820d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a", // h'd75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a' (RFC 8032 key 1)
	"02", // 2: signature
	"5840812f7abdf2f434036bbb6af3b782fd178cc8a546da3364f05cf689a7787d25ae8350b77e923b539923426303665e74dce155d04be89188527c0d4766434af90f", // bstr(64) Ed25519 signature by RFC 8032 key 1
)

const goldenCredentialNoEndDigest = "91952d20ab47214ab5b7cc2a47e5c7ad2077e1425965fdb413388c3989d53121"
const goldenCredentialNoEndSignedBytes = "117369646572612f7265636f72642d736967197369646572612f74727573742d63726564656e7469616c2f315fa401582033ece159526afe25ff9f6661c01b7751f0a143821e5b7dd1c051debf37dd13bc025820e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d031a6553f1000550101112131415161718191a1b1c1d1e1f"

// goldenRevocation: a revocation of goldenCredential, signed by end b (key 2).
// Signed bytes: 0x11 "sidera/record-sig" uvarint(len(type)) type uvarint(len(payload)) payload.
// Digest: SHA-256 of the same layout with the domain "sidera/record-digest".
var goldenRevocation = hexcat(
	"a3", // { 3 pairs: the envelope
	"01", // 1: type
	"78247369646572612f74727573742d63726564656e7469616c2d7265766f636174696f6e2f31", // "sidera/trust-credential-revocation/1"
	"02",   // 2: payload
	"5869", // bstr(105)
	"a1",   // payload: { 1 pairs
	"01",   // payload: 1: credential payload
	"5865", // payload: bstr(101) the revoked credential's payload
	"a5",   // payload: embedded { 5 pairs
	"01",   // payload: embedded 1: end a (the smaller node ID)
	"582033ece159526afe25ff9f6661c01b7751f0a143821e5b7dd1c051debf37dd13bc", // payload: embedded h'33ece159526afe25ff9f6661c01b7751f0a143821e5b7dd1c051debf37dd13bc'
	"02", // payload: embedded 2: end b
	"5820e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d", // payload: embedded h'e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d'
	"03",                                 // payload: embedded 3: created_at
	"1a6553f100",                         // payload: embedded 1700000000
	"04",                                 // payload: embedded 4: not_after
	"1a65554280",                         // payload: embedded 1700086400 (one day later)
	"05",                                 // payload: embedded 5: nonce
	"50000102030405060708090a0b0c0d0e0f", // payload: embedded h'000102030405060708090a0b0c0d0e0f'
	"03",                                 // 3: signatures
	"81",                                 // [ 1 signature(s), sorted by encoded public key
	"a2",                                 // { signature item
	"01",                                 // 1: public key
	"a2",                                 // { 2 pairs: public key
	"01",                                 // 1: algorithm
	"01",                                 // 1 (Ed25519)
	"02",                                 // 2: key bytes
	"58203d4017c3e843895a92b70aa74d1b7ebc9c982ccf2ec4968cc0cd55f12af4660c", // h'3d4017c3e843895a92b70aa74d1b7ebc9c982ccf2ec4968cc0cd55f12af4660c' (RFC 8032 key 2)
	"02", // 2: signature
	"58403057c0011a38fc8ba3f3cd9126c8ad72ca107435d964373a66dfcb50a23dca7f1891db5261893879e6bcaf09ae77f05227dd020c244274758ddb8764d3156f0a", // bstr(64) Ed25519 signature by RFC 8032 key 2
)

const goldenRevocationDigest = "d94b68bfd5a3c67054292cf41707b6a9f33510e2a4d19f42a1a13744de026d26"
const goldenRevocationSignedBytes = "117369646572612f7265636f72642d736967247369646572612f74727573742d63726564656e7469616c2d7265766f636174696f6e2f3169a1015865a501582033ece159526afe25ff9f6661c01b7751f0a143821e5b7dd1c051debf37dd13bc025820e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d031a6553f100041a655542800550000102030405060708090a0b0c0d0e0f"

// goldenVoucher: node A's voucher for goldenCredential, signed by A's network key (seed 0x11).
// Signed bytes: 0x11 "sidera/record-sig" uvarint(len(type)) type uvarint(len(payload)) payload.
// Digest: SHA-256 of the same layout with the domain "sidera/record-digest".
var goldenVoucher = hexcat(
	"a3", // { 3 pairs: the envelope
	"01", // 1: type
	"777369646572612f6f6e6c696e652d766f75636865722f31", // "sidera/online-voucher/1"
	"02",   // 2: payload
	"584f", // bstr(79)
	"a4",   // payload: { 4 pairs
	"01",   // payload: 1: issuer
	"5820e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d", // payload: h'e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d' (node ID of RFC 8032 key 1)
	"02", // payload: 2: edge
	"5820c5f781fa6341bc4b09964ae0002244448d0d605dfcae24543f4bbdc336a81d67", // payload: h'c5f781fa6341bc4b09964ae0002244448d0d605dfcae24543f4bbdc336a81d67' (digest of the credential above)
	"03",         // payload: 3: issued_at
	"1a6553f358", // payload: 1700000600
	"04",         // payload: 4: counter
	"07",         // payload: 7
	"03",         // 3: signatures
	"81",         // [ 1 signature(s), sorted by encoded public key
	"a2",         // { signature item
	"01",         // 1: public key
	"a2",         // { 2 pairs: public key
	"01",         // 1: algorithm
	"01",         // 1 (Ed25519)
	"02",         // 2: key bytes
	"5820d04ab232742bb4ab3a1368bd4615e4e6d0224ab71a016baf8520a332c9778737", // h'd04ab232742bb4ab3a1368bd4615e4e6d0224ab71a016baf8520a332c9778737' (seed 0x11 x32)
	"02", // 2: signature
	"584086eaaaee6579ac4b1953e95a288ab22e4dfdfeb3119dd153ac7162cf3808eb2be07035bb161823965bdcc318f31c182d5bf5ae284c9086e340a15e2d02012600", // bstr(64) Ed25519 signature by seed 0x11 x32
)

const goldenVoucherDigest = "4c408f3f1472acbd588b30815736a6e68f3c12b17c52c122028a368b8e73d27d"
const goldenVoucherSignedBytes = "117369646572612f7265636f72642d736967177369646572612f6f6e6c696e652d766f75636865722f314fa4015820e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d025820c5f781fa6341bc4b09964ae0002244448d0d605dfcae24543f4bbdc336a81d67031a6553f3580407"

// goldenDirective: an offline directive revoking goldenCredential on node A, signed by the device key (seed 0x33) of the embedded certificate.
// Signed bytes: 0x11 "sidera/record-sig" uvarint(len(type)) type uvarint(len(payload)) payload.
// Digest: SHA-256 of the same layout with the domain "sidera/record-digest".
var goldenDirective = hexcat(
	"a3", // { 3 pairs: the envelope
	"01", // 1: type
	"781a7369646572612f6f66666c696e652d6469726563746976652f31", // "sidera/offline-directive/1"
	"02",     // 2: payload
	"590176", // bstr(374)
	"a6",     // payload: { 6 pairs
	"01",     // payload: 1: node
	"5820e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d", // payload: h'e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d' (node ID of RFC 8032 key 1)
	"02", // payload: 2: credential
	"5820c5f781fa6341bc4b09964ae0002244448d0d605dfcae24543f4bbdc336a81d67", // payload: h'c5f781fa6341bc4b09964ae0002244448d0d605dfcae24543f4bbdc336a81d67' (digest of the credential)
	"03", // payload: 3: end a
	"582033ece159526afe25ff9f6661c01b7751f0a143821e5b7dd1c051debf37dd13bc", // payload: h'33ece159526afe25ff9f6661c01b7751f0a143821e5b7dd1c051debf37dd13bc'
	"04", // payload: 4: end b
	"5820e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d", // payload: h'e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d'
	"05",         // payload: 5: signed_at
	"1a6553f4e8", // payload: 1700001000
	"06",         // payload: 6: device certificate
	"58e0",       // payload: bstr(224) the embedded device-certificate record
	"a3",         // payload: embedded { 3 pairs: the envelope
	"01",         // payload: embedded 1: type
	"747369646572612f6465766963652d636572742f31", // payload: embedded "sidera/device-cert/1"
	"02",   // payload: embedded 2: payload
	"5859", // payload: embedded bstr(89)
	"a5",   // payload: embedded payload: { 5 pairs
	"01",   // payload: embedded payload: 1: person
	"58200300c202af68893402261cf312f5ffb2e956064e997abfe28ef485cfd179e39d", // payload: embedded payload: h'0300c202af68893402261cf312f5ffb2e956064e997abfe28ef485cfd179e39d' (person ID of RFC 8032 key 3)
	"02", // payload: embedded payload: 2: device key
	"a2", // payload: embedded payload: { 2 pairs: public key
	"01", // payload: embedded payload: 1: algorithm
	"01", // payload: embedded payload: 1 (Ed25519)
	"02", // payload: embedded payload: 2: key bytes
	"582017cb79fb2b4120f2b1ec65e4198d6e08b28e813feb01e4a400839b85e18080ce", // payload: embedded payload: h'17cb79fb2b4120f2b1ec65e4198d6e08b28e813feb01e4a400839b85e18080ce' (seed 0x33 x32)
	"03",         // payload: embedded payload: 3: max level
	"02",         // payload: embedded payload: 2 (manage)
	"04",         // payload: embedded payload: 4: not_before
	"1a6553f100", // payload: embedded payload: 1700000000
	"05",         // payload: embedded payload: 5: not_after
	"1a65540d20", // payload: embedded payload: 1700007200 (2 hours later)
	"03",         // payload: embedded 3: signatures
	"81",         // payload: embedded [ 1 signature(s), sorted by encoded public key
	"a2",         // payload: embedded { signature item
	"01",         // payload: embedded 1: public key
	"a2",         // payload: embedded { 2 pairs: public key
	"01",         // payload: embedded 1: algorithm
	"01",         // payload: embedded 1 (Ed25519)
	"02",         // payload: embedded 2: key bytes
	"5820fc51cd8e6218a1a38da47ed00230f0580816ed13ba3303ac5deb911548908025", // payload: embedded h'fc51cd8e6218a1a38da47ed00230f0580816ed13ba3303ac5deb911548908025' (RFC 8032 key 3)
	"02", // payload: embedded 2: signature
	"584001b3bec18cc2b4e7a70173a85deb32a2e46fad4930c0a66c425eadf3fc3e681c7fd1fd8d019d9e76c29e535bf8f8918bf937ebeed718ef7303cc3445b2a69303", // payload: embedded bstr(64) Ed25519 signature by RFC 8032 key 3
	"03", // 3: signatures
	"81", // [ 1 signature(s), sorted by encoded public key
	"a2", // { signature item
	"01", // 1: public key
	"a2", // { 2 pairs: public key
	"01", // 1: algorithm
	"01", // 1 (Ed25519)
	"02", // 2: key bytes
	"582017cb79fb2b4120f2b1ec65e4198d6e08b28e813feb01e4a400839b85e18080ce", // h'17cb79fb2b4120f2b1ec65e4198d6e08b28e813feb01e4a400839b85e18080ce' (seed 0x33 x32)
	"02", // 2: signature
	"5840d5a1129e03cf7e0d758ac8031db6ee89d355deb2306836a6cf1ec5d0f152296ab33b31cb921c84ed3cda7f0d4995e209c4eb53a6e2160875c81b40d1f7c83a06", // bstr(64) Ed25519 signature by seed 0x33 x32
)

const goldenDirectiveDigest = "b85fd88427a8b96f18e7796cb72b80fcc3b25780b01af1292d4dfdb2f2169920"
const goldenDirectiveSignedBytes = "117369646572612f7265636f72642d7369671a7369646572612f6f66666c696e652d6469726563746976652f31f602a6015820e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d025820c5f781fa6341bc4b09964ae0002244448d0d605dfcae24543f4bbdc336a81d6703582033ece159526afe25ff9f6661c01b7751f0a143821e5b7dd1c051debf37dd13bc045820e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d051a6553f4e80658e0a301747369646572612f6465766963652d636572742f31025859a50158200300c202af68893402261cf312f5ffb2e956064e997abfe28ef485cfd179e39d02a2010102582017cb79fb2b4120f2b1ec65e4198d6e08b28e813feb01e4a400839b85e18080ce0302041a6553f100051a65540d200381a201a20101025820fc51cd8e6218a1a38da47ed00230f0580816ed13ba3303ac5deb91154890802502584001b3bec18cc2b4e7a70173a85deb32a2e46fad4930c0a66c425eadf3fc3e681c7fd1fd8d019d9e76c29e535bf8f8918bf937ebeed718ef7303cc3445b2a69303"

// goldenDeviceCertificate: the device certificate record embedded in goldenDirective, signed by key 3 (the root key of the person).
// Signed bytes: 0x11 "sidera/record-sig" uvarint(len(type)) type uvarint(len(payload)) payload.
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
	"a2", // payload: { 2 pairs: public key
	"01", // payload: 1: algorithm
	"01", // payload: 1 (Ed25519)
	"02", // payload: 2: key bytes
	"582017cb79fb2b4120f2b1ec65e4198d6e08b28e813feb01e4a400839b85e18080ce", // payload: h'17cb79fb2b4120f2b1ec65e4198d6e08b28e813feb01e4a400839b85e18080ce' (seed 0x33 x32)
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
	"a2",         // { 2 pairs: public key
	"01",         // 1: algorithm
	"01",         // 1 (Ed25519)
	"02",         // 2: key bytes
	"5820fc51cd8e6218a1a38da47ed00230f0580816ed13ba3303ac5deb911548908025", // h'fc51cd8e6218a1a38da47ed00230f0580816ed13ba3303ac5deb911548908025' (RFC 8032 key 3)
	"02", // 2: signature
	"584001b3bec18cc2b4e7a70173a85deb32a2e46fad4930c0a66c425eadf3fc3e681c7fd1fd8d019d9e76c29e535bf8f8918bf937ebeed718ef7303cc3445b2a69303", // bstr(64) Ed25519 signature by RFC 8032 key 3
)

const goldenDeviceCertificateDigest = "61e44e911a5c17224bb8dc01c30c63cf571e4e67cd51959992c6b80a1fd65e90"
const goldenDeviceCertificateSignedBytes = "117369646572612f7265636f72642d736967147369646572612f6465766963652d636572742f3159a50158200300c202af68893402261cf312f5ffb2e956064e997abfe28ef485cfd179e39d02a2010102582017cb79fb2b4120f2b1ec65e4198d6e08b28e813feb01e4a400839b85e18080ce0302041a6553f100051a65540d20"
