package store_test

import (
	"encoding/hex"
	"testing"

	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/pairing"
)

// The pairing records are persisted forms only the pairing package makes: the
// request records are the ones a run of its protocol between two nodes leaves
// behind, the counters and the expectations are its golden vectors. The store
// reads them through the pairing package's own decoding.

const pairingTargetHex = ("a201781e7369646572612f70616972696e672d7461726765742d7265636f72642f3102a501a401a401752f7369646572612f" +
	"70616972696e672f312e302e3002a301a20101025820d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68" +
	"f707511a02a201010258203d4017c3e843895a92b70aa74d1b7ebc9c982ccf2ec4968cc0cd55f12af4660c030103a301a201" +
	"01025820fc51cd8e6218a1a38da47ed00230f0580816ed13ba3303ac5deb91154890802502a20101025820ec172b93ad5e56" +
	"3bf4932c70e1245034c35467ef2efd4d64ebf819683467e2bf0307041a6b36ec800258206f0a7479127217bd97483de8585f" +
	"abfa763496ead5d7eda7ed66788cc01ade72035820e2aadcd868ce028477f86e430140149b0300a9a5467a8dca66ef5ff344" +
	"e2836a045820210fc7bb818639ac48a4c6afa2f1581a8b9525e20fda68927f2b2ff836f73578021a6b36ec80031a6b36fa90" +
	"06010858eca30178197369646572612f74727573742d63726564656e7469616c2f3102585fa401582040a7fc60420d2a1d26" +
	"6e61f7613bc43e79ffaba9dc7324c2e14c1d4d55e8b99d025820e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20" +
	"f9f76075561af01d031a6b36ec800550307b5e5ff81a3cbf3c51cf34e9d48f7e0381a201a20101025820d75a980182b10ab7" +
	"d54bfed3c964073a0ee172f3daa62325af021a68f707511a025840ccd62a1acc911af9388619dcf7ea5a6da4ffd7abcb1a55" +
	"a8f0e9f97eaeee9e93eafd960d74137b5be3b4edd8f05a7fd78e21e332a67970e2f2c42cd2eff99208")

const pairingInitiatorHex = ("a20178217369646572612f70616972696e672d696e69746961746f722d7265636f72642f3102a701a401a401752f73696465" +
	"72612f70616972696e672f312e302e3002a301a20101025820d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af" +
	"021a68f707511a02a201010258203d4017c3e843895a92b70aa74d1b7ebc9c982ccf2ec4968cc0cd55f12af4660c030103a3" +
	"01a20101025820fc51cd8e6218a1a38da47ed00230f0580816ed13ba3303ac5deb91154890802502a20101025820ec172b93" +
	"ad5e563bf4932c70e1245034c35467ef2efd4d64ebf819683467e2bf0307041a6b36ec800258206f0a7479127217bd97483d" +
	"e8585fabfa763496ead5d7eda7ed66788cc01ade72035820e2aadcd868ce028477f86e430140149b0300a9a5467a8dca66ef" +
	"5ff344e2836a045820210fc7bb818639ac48a4c6afa2f1581a8b9525e20fda68927f2b2ff836f73578021a6b36ec80031a6b" +
	"36fa90060107a4010102a10101035041000000000000000000000000000002041a6b36ec800858eca3017819736964657261" +
	"2f74727573742d63726564656e7469616c2f3102585fa401582040a7fc60420d2a1d266e61f7613bc43e79ffaba9dc7324c2" +
	"e14c1d4d55e8b99d025820e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d031a6b36ec8005" +
	"50307b5e5ff81a3cbf3c51cf34e9d48f7e0381a201a20101025820d75a980182b10ab7d54bfed3c964073a0ee172f3daa623" +
	"25af021a68f707511a025840ccd62a1acc911af9388619dcf7ea5a6da4ffd7abcb1a55a8f0e9f97eaeee9e93eafd960d7413" +
	"7b5be3b4edd8f05a7fd78e21e332a67970e2f2c42cd2eff9920809f5")

const pairingLimitsHex = "a201777369646572612f70616972696e672d6c696d6974732f3102a201811a6b36ec8002821a6b36ece41a6b36ed48"

const pairingExpectationsHex = "a201781d7369646572612f70616972696e672d6578706563746174696f6e732f3102a10182a4015820e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d02a1010103500102030405060708090a0b0c0d0e0f10041a6b36ec80a401582040a7fc60420d2a1d266e61f7613bc43e79ffaba9dc7324c2e14c1d4d55e8b99d02a201020258204142434445464748494a4b4c4d4e4f505152535455565758595a5b5c5d5e5f6003501112131415161718191a1b1c1d1e1f20041a6b36ecbc"

func pairingBytes(t testing.TB, h string) []byte {
	t.Helper()
	b, err := hex.DecodeString(h)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func pairingTargetOf(t testing.TB) pairing.TargetRecord {
	t.Helper()
	r, err := pairing.DecodeTargetRecord(pairingBytes(t, pairingTargetHex))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func pairingInitiatorOf(t testing.TB) pairing.InitiatorRecord {
	t.Helper()
	r, err := pairing.DecodeInitiatorRecord(pairingBytes(t, pairingInitiatorHex))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func pairingLimitsOf(t testing.TB) pairing.Limits {
	t.Helper()
	l, err := pairing.DecodeLimits(pairingBytes(t, pairingLimitsHex))
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func pairingExpectationsOf(t testing.TB) []pairing.Expectation {
	t.Helper()
	list, err := pairing.DecodeExpectations(pairingBytes(t, pairingExpectationsHex))
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func pairingRecords(t testing.TB) []store.Record {
	return []store.Record{
		store.PairingTargetRecord{Record: pairingTargetOf(t)}, store.PairingInitiatorRecord{Record: pairingInitiatorOf(t)},
		store.PairingLimitsRecord{Limits: pairingLimitsOf(t)}, store.PairingExpectationsRecord{List: pairingExpectationsOf(t)},
	}
}
