package preauth

import (
	"encoding/hex"
)

// Golden vectors of the exchange message. The bytes of the wrapper were written
// by hand from the format description and checked with an independent program
// (Python: hand-built CBOR, and the cryptography package's Ed25519 verifying
// the signature inside each certificate record). The two certificate records
// are the golden vectors of package record, which were produced the same way,
// repeated here so that this test states the whole message.

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

// goldenNodeExchange: kind 1 (node) around the network-key certificate record
// of RFC 8032 key 1 naming RFC 8032 key 2 as the network key.
var goldenNodeExchange = hexcat(
	"a2",   // { 2 pairs: the message
	"01",   // 1: kind
	"01",   // 1: node
	"02",   // 2: record
	"58e6", // bstr(230): the encoded certificate record
	"a3",   // { 3 pairs: the envelope
	"01",   // 1: type
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

// goldenPersonExchange: kind 2 (person) around the device certificate record of
// RFC 8032 key 3 naming RFC 8032 key 2 as the device key.
var goldenPersonExchange = hexcat(
	"a2",   // { 2 pairs: the message
	"01",   // 1: kind
	"02",   // 2: person
	"02",   // 2: record
	"58e0", // bstr(224): the encoded certificate record
	"a3",   // { 3 pairs: the envelope
	"01",   // 1: type
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
