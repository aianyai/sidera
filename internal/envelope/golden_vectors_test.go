package envelope

// Golden vectors. Every byte below was written out by hand from the format
// description, and the node identifiers, digests and signatures were produced by
// an independent implementation (Python 3: hashlib for SHA-256, a hand-built
// CBOR writer, and the cryptography package's Ed25519), never by the code under
// test. Keys are the RFC 8032 test key 1 (the identity key of node A) and seeds
// made of one byte repeated 32 times (0x11: A's network key; 0x44: the identity
// key of node B). Endpoint identifiers are one byte repeated 16 times. Times are
// 1700000000 plus an offset in seconds. report_by is accept_before plus 10920
// seconds: the 2 h queue cap, four 15 min leases and the 2 min clock tolerance.
// Signed bytes: 0x11 "sidera/record-sig" uvarint(len(type)) type uvarint(len(payload)) payload.
// Digest: SHA-256 of the same layout with the domain "sidera/record-digest".

const goldenNodeA = "e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d"
const goldenNodeB = "64e53e346d0f70f253eff23689c61f3a14c410aa473c778a95a212e24e131bb7"

// A status report: the initial message of ctl-0001 was delivered to session endpoint 0x03 on node A. Signed by A's network key (seed 0x11).
var goldenReportDelivered = hexcat(
	"a3", // { 3 pairs: the record
	"01", // 1: type
	"767369646572612f7374617475732d7265706f72742f31", // "sidera/status-report/1"
	"02",                                 // 2: payload
	"582b",                               // bstr(43) payload
	"a5",                                 // embedded { 5 pairs: status report payload
	"01",                                 // embedded 1: message
	"6863746c2d30303031",                 // embedded "ctl-0001"
	"02",                                 // embedded 2: recipient
	"5003030303030303030303030303030303", // embedded h'03030303030303030303030303030303'
	"03",                                 // embedded 3: state
	"02",                                 // embedded 2 (delivered)
	"06",                                 // embedded 6: reported_at
	"1a6553f3f8",                         // embedded 1700000760
	"07",                                 // embedded 7: delivered_at
	"1a6553f3bc",                         // embedded 1700000700
	"03",                                 // 3: signatures
	"81",                                 // [ 1 signature, sorted by encoded public key
	"a2",                                 // { 2 pairs: signature item
	"01",                                 // 1: public key
	"a2",                                 // { 2 pairs: public key
	"01",                                 // 1: algorithm
	"01",                                 // 1 (Ed25519)
	"02",                                 // 2: key bytes
	"5820d04ab232742bb4ab3a1368bd4615e4e6d0224ab71a016baf8520a332c9778737", // h'd04ab232742bb4ab3a1368bd4615e4e6d0224ab71a016baf8520a332c9778737' (seed 0x11 x32)
	"02", // 2: signature
	"5840e4322cec8ffc7e4869ed68c010cb54ffcf129e470e48842e2b002984cf27232f46c1ee4d9f4fa6ccc75245ffb42ac8be456c62a9d5e8e6b583d905976c68b407", // bstr(64) Ed25519 signature
)

const goldenReportDeliveredDigest = "9c1b463830dd06cda02758ccb5bc01d586f6bcd69f3d4214bbf5f57b2ad2aaea"
const goldenReportDeliveredSignedBytes = "117369646572612f7265636f72642d736967167369646572612f7374617475732d7265706f72742f312ba5016863746c2d303030310250030303030303030303030303030303030302061a6553f3f8071a6553f3bc"

// A status report: msg-0001 is accepted but blocked because the adapter is offline.
var goldenReportBlocked = hexcat(
	"a3", // { 3 pairs: the record
	"01", // 1: type
	"767369646572612f7374617475732d7265706f72742f31", // "sidera/status-report/1"
	"02",                                 // 2: payload
	"5827",                               // bstr(39) payload
	"a5",                                 // embedded { 5 pairs: status report payload
	"01",                                 // embedded 1: message
	"686d73672d30303031",                 // embedded "msg-0001"
	"02",                                 // embedded 2: recipient
	"5001010101010101010101010101010101", // embedded h'01010101010101010101010101010101'
	"03",                                 // embedded 3: state
	"01",                                 // embedded 1 (accepted)
	"05",                                 // embedded 5: block cause
	"01",                                 // embedded 1
	"06",                                 // embedded 6: reported_at
	"1a6553f178",                         // embedded 1700000120
	"03",                                 // 3: signatures
	"81",                                 // [ 1 signature, sorted by encoded public key
	"a2",                                 // { 2 pairs: signature item
	"01",                                 // 1: public key
	"a2",                                 // { 2 pairs: public key
	"01",                                 // 1: algorithm
	"01",                                 // 1 (Ed25519)
	"02",                                 // 2: key bytes
	"5820d04ab232742bb4ab3a1368bd4615e4e6d0224ab71a016baf8520a332c9778737", // h'd04ab232742bb4ab3a1368bd4615e4e6d0224ab71a016baf8520a332c9778737' (seed 0x11 x32)
	"02", // 2: signature
	"58400cdbf83742e0a2a45f23dc1bde0923dd5b77d130454a0fe726b90fe57370b471ef98a910df989a90df89dbcd235a24e43c15d2ddb4ca23041b4f2495355f2e00", // bstr(64) Ed25519 signature
)

const goldenReportBlockedDigest = "8c3d5d48b889aa16d9f6db1dfd95785a06422c77b680a5b9a054136052cc22b0"
const goldenReportBlockedSignedBytes = "117369646572612f7265636f72642d736967167369646572612f7374617475732d7265706f72742f3127a501686d73672d3030303102500101010101010101010101010101010103010501061a6553f178"

// A status report: msg-0003 was rejected, backlog expired.
var goldenReportRejected = hexcat(
	"a3", // { 3 pairs: the record
	"01", // 1: type
	"767369646572612f7374617475732d7265706f72742f31", // "sidera/status-report/1"
	"02",                                 // 2: payload
	"5827",                               // bstr(39) payload
	"a5",                                 // embedded { 5 pairs: status report payload
	"01",                                 // embedded 1: message
	"686d73672d30303033",                 // embedded "msg-0003"
	"02",                                 // embedded 2: recipient
	"5001010101010101010101010101010101", // embedded h'01010101010101010101010101010101'
	"03",                                 // embedded 3: state
	"03",                                 // embedded 3 (rejected)
	"04",                                 // embedded 4: reject reason
	"07",                                 // embedded 7
	"06",                                 // embedded 6: reported_at
	"1a655400a0",                         // embedded 1700004000
	"03",                                 // 3: signatures
	"81",                                 // [ 1 signature, sorted by encoded public key
	"a2",                                 // { 2 pairs: signature item
	"01",                                 // 1: public key
	"a2",                                 // { 2 pairs: public key
	"01",                                 // 1: algorithm
	"01",                                 // 1 (Ed25519)
	"02",                                 // 2: key bytes
	"5820d04ab232742bb4ab3a1368bd4615e4e6d0224ab71a016baf8520a332c9778737", // h'd04ab232742bb4ab3a1368bd4615e4e6d0224ab71a016baf8520a332c9778737' (seed 0x11 x32)
	"02", // 2: signature
	"58401ed5f2148bd42c6500dfb6ac34fd821233167d9a2848e91f46cba8aa92deed44129affdc3d6fa8a08bb0f22889b008100d125fa30b0046ec85f5d21196c77f04", // bstr(64) Ed25519 signature
)

const goldenReportRejectedDigest = "b4235b9641ae55d23150a6d9317b818c4f370982d0073a7251e1f5866e77b905"
const goldenReportRejectedSignedBytes = "117369646572612f7265636f72642d736967167369646572612f7374617475732d7265706f72742f3127a501686d73672d3030303302500101010101010101010101010101010103030407061a655400a0"

// A control reply: ctl-0001 created session endpoint 0x03.
var goldenReplyCreated = hexcat(
	"a3", // { 3 pairs: the record
	"01", // 1: type
	"767369646572612f636f6e74726f6c2d7265706c792f31", // "sidera/control-reply/1"
	"02",                                 // 2: payload
	"5839",                               // bstr(57) payload
	"a4",                                 // embedded { 4 pairs: control reply payload
	"01",                                 // embedded 1: message
	"6863746c2d30303031",                 // embedded "ctl-0001"
	"02",                                 // embedded 2: recipient
	"5002020202020202020202020202020202", // embedded h'02020202020202020202020202020202'
	"03",                                 // embedded 3: issued_at
	"1a6553f38a",                         // embedded 1700000650
	"05",                                 // embedded 5: create result
	"a2",                                 // embedded { 2 pairs: create result
	"01",                                 // embedded 1: outcome
	"01",                                 // embedded 1 (created)
	"02",                                 // embedded 2: session_endpoint
	"5003030303030303030303030303030303", // embedded h'03030303030303030303030303030303'
	"03",                                 // 3: signatures
	"81",                                 // [ 1 signature, sorted by encoded public key
	"a2",                                 // { 2 pairs: signature item
	"01",                                 // 1: public key
	"a2",                                 // { 2 pairs: public key
	"01",                                 // 1: algorithm
	"01",                                 // 1 (Ed25519)
	"02",                                 // 2: key bytes
	"5820d04ab232742bb4ab3a1368bd4615e4e6d0224ab71a016baf8520a332c9778737", // h'd04ab232742bb4ab3a1368bd4615e4e6d0224ab71a016baf8520a332c9778737' (seed 0x11 x32)
	"02", // 2: signature
	"5840459dd48748ca5e7f5d40f8fc9d433c12b498ea25b97c7a64826f001a7ba30d96355ea0ecf55d40f9fa7c3361d91788dc94495a6c964dd173ef8bb39ea915d601", // bstr(64) Ed25519 signature
)

const goldenReplyCreatedDigest = "38a8fd8afce59a7398fe3d6f6df01962e8824ac0ad57d63dde0694cf13076f03"
const goldenReplyCreatedSignedBytes = "117369646572612f7265636f72642d736967167369646572612f636f6e74726f6c2d7265706c792f3139a4016863746c2d30303031025002020202020202020202020202020202031a6553f38a05a20101025003030303030303030303030303030303"

// A control reply: ctl-0002 failed, preset unavailable.
var goldenReplyFailed = hexcat(
	"a3", // { 3 pairs: the record
	"01", // 1: type
	"767369646572612f636f6e74726f6c2d7265706c792f31", // "sidera/control-reply/1"
	"02",                                 // 2: payload
	"5829",                               // bstr(41) payload
	"a4",                                 // embedded { 4 pairs: control reply payload
	"01",                                 // embedded 1: message
	"6863746c2d30303032",                 // embedded "ctl-0002"
	"02",                                 // embedded 2: recipient
	"5002020202020202020202020202020202", // embedded h'02020202020202020202020202020202'
	"03",                                 // embedded 3: issued_at
	"1a6553f11e",                         // embedded 1700000030
	"05",                                 // embedded 5: create result
	"a2",                                 // embedded { 2 pairs: create result
	"01",                                 // embedded 1: outcome
	"03",                                 // embedded 3 (failed)
	"03",                                 // embedded 3: reason
	"02",                                 // embedded 2 (preset_unavailable)
	"03",                                 // 3: signatures
	"81",                                 // [ 1 signature, sorted by encoded public key
	"a2",                                 // { 2 pairs: signature item
	"01",                                 // 1: public key
	"a2",                                 // { 2 pairs: public key
	"01",                                 // 1: algorithm
	"01",                                 // 1 (Ed25519)
	"02",                                 // 2: key bytes
	"5820d04ab232742bb4ab3a1368bd4615e4e6d0224ab71a016baf8520a332c9778737", // h'd04ab232742bb4ab3a1368bd4615e4e6d0224ab71a016baf8520a332c9778737' (seed 0x11 x32)
	"02", // 2: signature
	"58404672254cb0b0f782ba1c4e277d090f58976d26518c530e249178ba76f426a3d12e34f9bc9629859edcc5cd04b8864293fc684a40178cbd401d7548a8e166db00", // bstr(64) Ed25519 signature
)

const goldenReplyFailedDigest = "57be6a4a3570306d8ae9f3a7db5e09cabfdd2a47a08422d649fbe734ae878137"
const goldenReplyFailedSignedBytes = "117369646572612f7265636f72642d736967167369646572612f636f6e74726f6c2d7265706c792f3129a4016863746c2d30303032025002020202020202020202020202020202031a6553f11e05a201030302"

// A control reply: ctl-0003 close not confirmed, adapter offline.
var goldenReplyClose = hexcat(
	"a3", // { 3 pairs: the record
	"01", // 1: type
	"767369646572612f636f6e74726f6c2d7265706c792f31", // "sidera/control-reply/1"
	"02",                                 // 2: payload
	"5829",                               // bstr(41) payload
	"a4",                                 // embedded { 4 pairs: control reply payload
	"01",                                 // embedded 1: message
	"6863746c2d30303033",                 // embedded "ctl-0003"
	"02",                                 // embedded 2: recipient
	"5003030303030303030303030303030303", // embedded h'03030303030303030303030303030303'
	"03",                                 // embedded 3: issued_at
	"1a6553f128",                         // embedded 1700000040
	"06",                                 // embedded 6: close result
	"a2",                                 // embedded { 2 pairs: close result
	"01",                                 // embedded 1: outcome
	"02",                                 // embedded 2 (close_unconfirmed)
	"02",                                 // embedded 2: reason
	"02",                                 // embedded 2 (adapter_offline)
	"03",                                 // 3: signatures
	"81",                                 // [ 1 signature, sorted by encoded public key
	"a2",                                 // { 2 pairs: signature item
	"01",                                 // 1: public key
	"a2",                                 // { 2 pairs: public key
	"01",                                 // 1: algorithm
	"01",                                 // 1 (Ed25519)
	"02",                                 // 2: key bytes
	"5820d04ab232742bb4ab3a1368bd4615e4e6d0224ab71a016baf8520a332c9778737", // h'd04ab232742bb4ab3a1368bd4615e4e6d0224ab71a016baf8520a332c9778737' (seed 0x11 x32)
	"02", // 2: signature
	"5840e8264900e23a0c5deb1379da73ba7feb6eda17394393ba99a71af6bd4e391c924834e4f143071b4b88a26bf603c46115d70c6a158c60201c8fb476137e8f690e", // bstr(64) Ed25519 signature
)

const goldenReplyCloseDigest = "c708595bf5657c8e92e40bd33a9d65acd22749378c6d35371fd82b60ff728eb8"
const goldenReplyCloseSignedBytes = "117369646572612f7265636f72642d736967167369646572612f636f6e74726f6c2d7265706c792f3129a4016863746c2d30303033025003030303030303030303030303030303031a6553f12806a201020202"

// A control reply: ctl-0005 interrupt failed, native error.
var goldenReplyInterrupt = hexcat(
	"a3", // { 3 pairs: the record
	"01", // 1: type
	"767369646572612f636f6e74726f6c2d7265706c792f31", // "sidera/control-reply/1"
	"02",                                 // 2: payload
	"5829",                               // bstr(41) payload
	"a4",                                 // embedded { 4 pairs: control reply payload
	"01",                                 // embedded 1: message
	"6863746c2d30303035",                 // embedded "ctl-0005"
	"02",                                 // embedded 2: recipient
	"5003030303030303030303030303030303", // embedded h'03030303030303030303030303030303'
	"03",                                 // embedded 3: issued_at
	"1a6553f132",                         // embedded 1700000050
	"07",                                 // embedded 7: interrupt result
	"a2",                                 // embedded { 2 pairs: interrupt result
	"01",                                 // embedded 1: outcome
	"04",                                 // embedded 4 (failed)
	"02",                                 // embedded 2: reason
	"03",                                 // embedded 3 (native_error)
	"03",                                 // 3: signatures
	"81",                                 // [ 1 signature, sorted by encoded public key
	"a2",                                 // { 2 pairs: signature item
	"01",                                 // 1: public key
	"a2",                                 // { 2 pairs: public key
	"01",                                 // 1: algorithm
	"01",                                 // 1 (Ed25519)
	"02",                                 // 2: key bytes
	"5820d04ab232742bb4ab3a1368bd4615e4e6d0224ab71a016baf8520a332c9778737", // h'd04ab232742bb4ab3a1368bd4615e4e6d0224ab71a016baf8520a332c9778737' (seed 0x11 x32)
	"02", // 2: signature
	"5840a10d76e6c2b65cb7c7cd9eb4ad5614b561b749599198199b40cb0ca3e1ca78d3ee63c0b67e627990d3e4eba89337e94045705b5becdfc3eb2e5c135dd14ba10d", // bstr(64) Ed25519 signature
)

const goldenReplyInterruptDigest = "a3496a339cbfd0e56fad89f3a12e5609d6a2734629547e8c5a927b2914b5fa4a"
const goldenReplyInterruptSignedBytes = "117369646572612f7265636f72642d736967167369646572612f636f6e74726f6c2d7265706c792f3129a4016863746c2d30303035025003030303030303030303030303030303031a6553f13207a201040203"

// A control reply: endpoint.status of endpoint 0x01.
var goldenReplyStatus = hexcat(
	"a3", // { 3 pairs: the record
	"01", // 1: type
	"767369646572612f636f6e74726f6c2d7265706c792f31", // "sidera/control-reply/1"
	"02",                                 // 2: payload
	"584a",                               // bstr(74) payload
	"a4",                                 // embedded { 4 pairs: control reply payload
	"01",                                 // embedded 1: message
	"6863746c2d30303034",                 // embedded "ctl-0004"
	"02",                                 // embedded 2: recipient
	"5001010101010101010101010101010101", // embedded h'01010101010101010101010101010101'
	"03",                                 // embedded 3: issued_at
	"1a6553f15a",                         // embedded 1700000090
	"08",                                 // embedded 8: status result
	"a5",                                 // embedded { 5 pairs: status result
	"01",                                 // embedded 1: activity
	"02",                                 // embedded 2 (busy)
	"02",                                 // embedded 2: queued
	"03",                                 // embedded 3
	"03",                                 // embedded 3: conditions
	"82",                                 // embedded [ conditions
	"01",                                 // embedded 1 (adapter_offline)
	"04",                                 // embedded 4 (server_request_rejected)
	"04",                                 // embedded 4: presets
	"82",                                 // embedded [ presets sorted by name
	"a2",                                 // embedded { 2 pairs: preset
	"01",                                 // embedded 1: name
	"6764656661756c74",                   // embedded "default"
	"02",                                 // embedded 2: available
	"f5",                                 // embedded true
	"a2",                                 // embedded { 2 pairs: preset
	"01",                                 // embedded 1: name
	"6466617374",                         // embedded "fast"
	"02",                                 // embedded 2: available
	"f4",                                 // embedded false
	"05",                                 // embedded 5: as_of
	"1a6553f15a",                         // embedded 1700000090
	"03",                                 // 3: signatures
	"81",                                 // [ 1 signature, sorted by encoded public key
	"a2",                                 // { 2 pairs: signature item
	"01",                                 // 1: public key
	"a2",                                 // { 2 pairs: public key
	"01",                                 // 1: algorithm
	"01",                                 // 1 (Ed25519)
	"02",                                 // 2: key bytes
	"5820d04ab232742bb4ab3a1368bd4615e4e6d0224ab71a016baf8520a332c9778737", // h'd04ab232742bb4ab3a1368bd4615e4e6d0224ab71a016baf8520a332c9778737' (seed 0x11 x32)
	"02", // 2: signature
	"5840c61ec12332636a18cccd7cc68d48dd0390d5f57c3ad354c46f770ac030413f2b91a913561c1724458a6a0fb13b3030d4a8850221dcc2d65f2af494c0f3af4701", // bstr(64) Ed25519 signature
)

const goldenReplyStatusDigest = "a73cc086d8cacabc061b8266dc4655af0e6758c404daf5fa5d218231de0a7d56"
const goldenReplyStatusSignedBytes = "117369646572612f7265636f72642d736967167369646572612f636f6e74726f6c2d7265706c792f314aa4016863746c2d30303034025001010101010101010101010101010101031a6553f15a08a501020203038201040482a2016764656661756c7402f5a201646661737402f4051a6553f15a"

// A message from a sender on node B to endpoint 0x01 on node A, as it arrives (the sender is the connection's).
var goldenEnvelopeMessage = hexcat(
	"ac",                                   // { 12 pairs: the envelope
	"01",                                   // 1: type
	"717369646572612f656e76656c6f70652f31", // "sidera/envelope/1"
	"02",                                   // 2: id
	"686d73672d30303031",                   // "msg-0001"
	"03",                                   // 3: to
	"81",                                   // [ recipients
	"a2",                                   // { 2 pairs: address
	"01",                                   // 1: node
	"5820e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d", // h'e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d'
	"02",                                 // 2: endpoint
	"5001010101010101010101010101010101", // h'01010101010101010101010101010101'
	"04",                                 // 4: multi-recipient
	"f4",                                 // false
	"05",                                 // 5: disclose recipients
	"f4",                                 // false
	"06",                                 // 6: kind
	"01",                                 // 1 (message)
	"0a",                                 // 10: seq
	"81",                                 // [ sequence numbers sorted by endpoint
	"a2",                                 // { 2 pairs: sequence entry
	"01",                                 // 1: endpoint
	"5001010101010101010101010101010101", // h'01010101010101010101010101010101'
	"02",                                 // 2: seq
	"01",                                 // 1
	"0b",                                 // 11: sent_at
	"1a6553f100",                         // 1700000000
	"0c",                                 // 12: accept_before
	"1a6553f358",                         // 1700000600
	"0d",                                 // 13: report_by
	"1a65541e00",                         // 1700011520
	"0e",                                 // 14: content type
	"6a746578742f706c61696e",             // "text/plain"
	"0f",                                 // 15: body
	"4568656c6c6f",                       // "hello"
)

// A control.session.create for runtime endpoint 0x02 on node A with an initial message.
var goldenEnvelopeSessionCreate = hexcat(
	"ad",                                   // { 13 pairs: the envelope
	"01",                                   // 1: type
	"717369646572612f656e76656c6f70652f31", // "sidera/envelope/1"
	"02",                                   // 2: id
	"6863746c2d30303031",                   // "ctl-0001"
	"03",                                   // 3: to
	"81",                                   // [ recipients
	"a2",                                   // { 2 pairs: address
	"01",                                   // 1: node
	"5820e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d", // h'e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d'
	"02",                                 // 2: endpoint
	"5002020202020202020202020202020202", // h'02020202020202020202020202020202'
	"04",                                 // 4: multi-recipient
	"f4",                                 // false
	"05",                                 // 5: disclose recipients
	"f4",                                 // false
	"06",                                 // 6: kind
	"02",                                 // 2 (control.session.create)
	"0a",                                 // 10: seq
	"81",                                 // [ sequence numbers sorted by endpoint
	"a2",                                 // { 2 pairs: sequence entry
	"01",                                 // 1: endpoint
	"5002020202020202020202020202020202", // h'02020202020202020202020202020202'
	"02",                                 // 2: seq
	"07",                                 // 7
	"0b",                                 // 11: sent_at
	"1a6553f100",                         // 1700000000
	"0c",                                 // 12: accept_before
	"1a6553f358",                         // 1700000600
	"0d",                                 // 13: report_by
	"1a65541e00",                         // 1700011520
	"0e",                                 // 14: content type
	"7819746578742f706c61696e3b20636861727365743d7574662d38", // "text/plain; charset=utf-8"
	"0f",                     // 15: body
	"4a73746172742068657265", // "start here"
	"10",                     // 16: params
	"a1",                     // { 1 pairs: params
	"01",                     // 1: preset
	"6764656661756c74",       // "default"
)

// A reply from session endpoint 0x03 on node A to two disclosed recipients on node B, with evidence.
var goldenEnvelopeReply = hexcat(
	"af",                                   // { 15 pairs: the envelope
	"01",                                   // 1: type
	"717369646572612f656e76656c6f70652f31", // "sidera/envelope/1"
	"02",                                   // 2: id
	"686d73672d30303032",                   // "msg-0002"
	"03",                                   // 3: to
	"82",                                   // [ recipients
	"a2",                                   // { 2 pairs: address
	"01",                                   // 1: node
	"582064e53e346d0f70f253eff23689c61f3a14c410aa473c778a95a212e24e131bb7", // h'64e53e346d0f70f253eff23689c61f3a14c410aa473c778a95a212e24e131bb7'
	"02",                                 // 2: endpoint
	"500a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a", // h'0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a'
	"a2",                                 // { 2 pairs: address
	"01",                                 // 1: node
	"582064e53e346d0f70f253eff23689c61f3a14c410aa473c778a95a212e24e131bb7", // h'64e53e346d0f70f253eff23689c61f3a14c410aa473c778a95a212e24e131bb7'
	"02",                                 // 2: endpoint
	"500b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b", // h'0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b'
	"04",                                 // 4: multi-recipient
	"f5",                                 // true
	"05",                                 // 5: disclose recipients
	"f5",                                 // true
	"06",                                 // 6: kind
	"01",                                 // 1 (message)
	"07",                                 // 7: reply_to
	"6863746c2d30303031",                 // "ctl-0001"
	"08",                                 // 8: thread
	"627431",                             // "t1"
	"09",                                 // 9: evidence
	"82",                                 // [ evidence: control reply, then status report
	"58c2",                               // bstr(194) evidence 0: the control reply
	"a3",                                 // embedded { 3 pairs: the record
	"01",                                 // embedded 1: type
	"767369646572612f636f6e74726f6c2d7265706c792f31", // embedded "sidera/control-reply/1"
	"02",                                 // embedded 2: payload
	"5839",                               // embedded bstr(57) payload
	"a4",                                 // embedded embedded { 4 pairs: control reply payload
	"01",                                 // embedded embedded 1: message
	"6863746c2d30303031",                 // embedded embedded "ctl-0001"
	"02",                                 // embedded embedded 2: recipient
	"5002020202020202020202020202020202", // embedded embedded h'02020202020202020202020202020202'
	"03",                                 // embedded embedded 3: issued_at
	"1a6553f38a",                         // embedded embedded 1700000650
	"05",                                 // embedded embedded 5: create result
	"a2",                                 // embedded embedded { 2 pairs: create result
	"01",                                 // embedded embedded 1: outcome
	"01",                                 // embedded embedded 1 (created)
	"02",                                 // embedded embedded 2: session_endpoint
	"5003030303030303030303030303030303", // embedded embedded h'03030303030303030303030303030303'
	"03",                                 // embedded 3: signatures
	"81",                                 // embedded [ 1 signature, sorted by encoded public key
	"a2",                                 // embedded { 2 pairs: signature item
	"01",                                 // embedded 1: public key
	"a2",                                 // embedded { 2 pairs: public key
	"01",                                 // embedded 1: algorithm
	"01",                                 // embedded 1 (Ed25519)
	"02",                                 // embedded 2: key bytes
	"5820d04ab232742bb4ab3a1368bd4615e4e6d0224ab71a016baf8520a332c9778737", // embedded h'd04ab232742bb4ab3a1368bd4615e4e6d0224ab71a016baf8520a332c9778737' (seed 0x11 x32)
	"02", // embedded 2: signature
	"5840459dd48748ca5e7f5d40f8fc9d433c12b498ea25b97c7a64826f001a7ba30d96355ea0ecf55d40f9fa7c3361d91788dc94495a6c964dd173ef8bb39ea915d601", // embedded bstr(64) Ed25519 signature
	"58b4", // bstr(180) evidence 1: the status report
	"a3",   // embedded { 3 pairs: the record
	"01",   // embedded 1: type
	"767369646572612f7374617475732d7265706f72742f31", // embedded "sidera/status-report/1"
	"02",                                 // embedded 2: payload
	"582b",                               // embedded bstr(43) payload
	"a5",                                 // embedded embedded { 5 pairs: status report payload
	"01",                                 // embedded embedded 1: message
	"6863746c2d30303031",                 // embedded embedded "ctl-0001"
	"02",                                 // embedded embedded 2: recipient
	"5003030303030303030303030303030303", // embedded embedded h'03030303030303030303030303030303'
	"03",                                 // embedded embedded 3: state
	"02",                                 // embedded embedded 2 (delivered)
	"06",                                 // embedded embedded 6: reported_at
	"1a6553f3f8",                         // embedded embedded 1700000760
	"07",                                 // embedded embedded 7: delivered_at
	"1a6553f3bc",                         // embedded embedded 1700000700
	"03",                                 // embedded 3: signatures
	"81",                                 // embedded [ 1 signature, sorted by encoded public key
	"a2",                                 // embedded { 2 pairs: signature item
	"01",                                 // embedded 1: public key
	"a2",                                 // embedded { 2 pairs: public key
	"01",                                 // embedded 1: algorithm
	"01",                                 // embedded 1 (Ed25519)
	"02",                                 // embedded 2: key bytes
	"5820d04ab232742bb4ab3a1368bd4615e4e6d0224ab71a016baf8520a332c9778737", // embedded h'd04ab232742bb4ab3a1368bd4615e4e6d0224ab71a016baf8520a332c9778737' (seed 0x11 x32)
	"02", // embedded 2: signature
	"5840e4322cec8ffc7e4869ed68c010cb54ffcf129e470e48842e2b002984cf27232f46c1ee4d9f4fa6ccc75245ffb42ac8be456c62a9d5e8e6b583d905976c68b407", // embedded bstr(64) Ed25519 signature
	"0a",                                 // 10: seq
	"82",                                 // [ sequence numbers sorted by endpoint
	"a2",                                 // { 2 pairs: sequence entry
	"01",                                 // 1: endpoint
	"500a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a", // h'0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a'
	"02",                                 // 2: seq
	"03",                                 // 3
	"a2",                                 // { 2 pairs: sequence entry
	"01",                                 // 1: endpoint
	"500b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b", // h'0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b'
	"02",                                 // 2: seq
	"04",                                 // 4
	"0b",                                 // 11: sent_at
	"1a6553f100",                         // 1700000000
	"0c",                                 // 12: accept_before
	"1a6553f358",                         // 1700000600
	"0d",                                 // 13: report_by
	"1a65541e00",                         // 1700011520
	"0e",                                 // 14: content type
	"6a746578742f706c61696e",             // "text/plain"
	"0f",                                 // 15: body
	"44646f6e65",                         // "done"
)

const goldenSessionCreateRequestDigest = "20b7740e7fa84ca353d4a58d4549abcb4d665486f07f0dc8e59a9dac8bf7cc90"
