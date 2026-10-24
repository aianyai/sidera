package envelope

// Golden vectors. Every byte below was written out by hand from the format
// description, and the node identifiers, digests and signatures were produced by
// an independent implementation (Python 3: hashlib for SHA-256, a hand-built
// CBOR writer, and the cryptography package's Ed25519), never by the code under
// test. Keys are the RFC 8032 test key 1 (the identity key of node A) and seeds
// made of one byte repeated 32 times (0x11: A's network key; 0x44: the identity
// key of node B). Every status report and control reply names node B as the
// sending node. Endpoint identifiers are one byte repeated 16 times. Times are
// 1700000000 plus an offset in seconds. report_by is accept_before plus 10920
// seconds: the 2 h queue cap, four 15 min leases and the 2 min clock tolerance.
// Signed bytes: 0x11 "sidera/record-sig" uvarint(len(type)) type uvarint(len(payload)) payload.
// Digest: SHA-256 of the same layout with the domain "sidera/record-digest".
// Request digest: SHA-256 of the fields kind (1 byte), preset, content type, body, reply_to,
// thread, flags (1 byte: bit 0 multi-recipient, bit 1 disclose_recipients), the disclosed
// recipients (each address as 32 node bytes then 16 endpoint bytes, in the order of "to"; empty when
// the recipients are not disclosed), accept_before and report_by (8 bytes big-endian each), each
// framed with a uvarint length, under the domain "sidera/request-digest/1".

const goldenNodeA = "e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d"
const goldenNodeB = "64e53e346d0f70f253eff23689c61f3a14c410aa473c778a95a212e24e131bb7"

// A status report: the initial message of ctl-0001 was delivered to session endpoint 0x03 on node A. Signed by A's network key (seed 0x11).
var goldenReportDelivered = hexcat(
	"a3", // { 3 pairs: the record
	"01", // 1: type
	"767369646572612f7374617475732d7265706f72742f31", // "sidera/status-report/1"
	"02",                                 // 2: payload
	"584e",                               // bstr(78) payload
	"a6",                                 // embedded { 6 pairs: status report payload
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
	"08",                                 // embedded 8: sender
	"582064e53e346d0f70f253eff23689c61f3a14c410aa473c778a95a212e24e131bb7", // embedded h'64e53e346d0f70f253eff23689c61f3a14c410aa473c778a95a212e24e131bb7' (node B, the sending node)
	"03", // 3: signatures
	"81", // [ 1 signature, sorted by encoded public key
	"a2", // { 2 pairs: signature item
	"01", // 1: public key
	"a2", // { 2 pairs: public key
	"01", // 1: algorithm
	"01", // 1 (Ed25519)
	"02", // 2: key bytes
	"5820d04ab232742bb4ab3a1368bd4615e4e6d0224ab71a016baf8520a332c9778737", // h'd04ab232742bb4ab3a1368bd4615e4e6d0224ab71a016baf8520a332c9778737' (seed 0x11 x32)
	"02", // 2: signature
	"5840444e2a632066028f9064bdf6add113e9b6978c8195273ac70bcf3c1e533e01fa964e2503931dfc20473e2f278da9590391b512ffaf4cda6dfe11252ba0856c08", // bstr(64) Ed25519 signature
)

const goldenReportDeliveredDigest = "f88e45051da6480b7468c571611f6b8e4bbcc0549fb4deb3277800075267cd88"
const goldenReportDeliveredSignedBytes = "117369646572612f7265636f72642d736967167369646572612f7374617475732d7265706f72742f314ea6016863746c2d303030310250030303030303030303030303030303030302061a6553f3f8071a6553f3bc08582064e53e346d0f70f253eff23689c61f3a14c410aa473c778a95a212e24e131bb7"

// A status report: msg-0001 is accepted but blocked because the adapter is offline.
var goldenReportBlocked = hexcat(
	"a3", // { 3 pairs: the record
	"01", // 1: type
	"767369646572612f7374617475732d7265706f72742f31", // "sidera/status-report/1"
	"02",                                 // 2: payload
	"584a",                               // bstr(74) payload
	"a6",                                 // embedded { 6 pairs: status report payload
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
	"08",                                 // embedded 8: sender
	"582064e53e346d0f70f253eff23689c61f3a14c410aa473c778a95a212e24e131bb7", // embedded h'64e53e346d0f70f253eff23689c61f3a14c410aa473c778a95a212e24e131bb7' (node B, the sending node)
	"03", // 3: signatures
	"81", // [ 1 signature, sorted by encoded public key
	"a2", // { 2 pairs: signature item
	"01", // 1: public key
	"a2", // { 2 pairs: public key
	"01", // 1: algorithm
	"01", // 1 (Ed25519)
	"02", // 2: key bytes
	"5820d04ab232742bb4ab3a1368bd4615e4e6d0224ab71a016baf8520a332c9778737", // h'd04ab232742bb4ab3a1368bd4615e4e6d0224ab71a016baf8520a332c9778737' (seed 0x11 x32)
	"02", // 2: signature
	"5840b325f4902373457d5bfc4774497762b70012dfc3568ec7f9daa25cfde2cc8a1a2d0e338e7ef548691ea8f8212e3e48e648fbea300911f7cb1723fffbf990d704", // bstr(64) Ed25519 signature
)

const goldenReportBlockedDigest = "efeaa912f481cc677810b633dfa68719396df0bfaeba1cc31273d19c0c3454b9"
const goldenReportBlockedSignedBytes = "117369646572612f7265636f72642d736967167369646572612f7374617475732d7265706f72742f314aa601686d73672d3030303102500101010101010101010101010101010103010501061a6553f17808582064e53e346d0f70f253eff23689c61f3a14c410aa473c778a95a212e24e131bb7"

// A status report: msg-0003 was rejected, backlog expired.
var goldenReportRejected = hexcat(
	"a3", // { 3 pairs: the record
	"01", // 1: type
	"767369646572612f7374617475732d7265706f72742f31", // "sidera/status-report/1"
	"02",                                 // 2: payload
	"584a",                               // bstr(74) payload
	"a6",                                 // embedded { 6 pairs: status report payload
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
	"08",                                 // embedded 8: sender
	"582064e53e346d0f70f253eff23689c61f3a14c410aa473c778a95a212e24e131bb7", // embedded h'64e53e346d0f70f253eff23689c61f3a14c410aa473c778a95a212e24e131bb7' (node B, the sending node)
	"03", // 3: signatures
	"81", // [ 1 signature, sorted by encoded public key
	"a2", // { 2 pairs: signature item
	"01", // 1: public key
	"a2", // { 2 pairs: public key
	"01", // 1: algorithm
	"01", // 1 (Ed25519)
	"02", // 2: key bytes
	"5820d04ab232742bb4ab3a1368bd4615e4e6d0224ab71a016baf8520a332c9778737", // h'd04ab232742bb4ab3a1368bd4615e4e6d0224ab71a016baf8520a332c9778737' (seed 0x11 x32)
	"02", // 2: signature
	"5840182a24e1d6959a77bbbdcdbd06e7ff202dc6078f1444509baee0f247c145cf232518924e5eb21bf4010bb99aa6029ca303e925fe492dbab89eba5be04f9dc407", // bstr(64) Ed25519 signature
)

const goldenReportRejectedDigest = "4a7a4000a5059f66cd4201bc4596d812a87a3d1e08e09f394a592b335905c095"
const goldenReportRejectedSignedBytes = "117369646572612f7265636f72642d736967167369646572612f7374617475732d7265706f72742f314aa601686d73672d3030303302500101010101010101010101010101010103030407061a655400a008582064e53e346d0f70f253eff23689c61f3a14c410aa473c778a95a212e24e131bb7"

// A status report: msg-0004 is in an unknown state because the target lost what it had stored about it (key 9, unknown_cause, 1 = lost_at_target).
var goldenReportLostAtTarget = hexcat(
	"a3", // { 3 pairs: the record
	"01", // 1: type
	"767369646572612f7374617475732d7265706f72742f31", // "sidera/status-report/1"
	"02",                                 // 2: payload
	"584a",                               // bstr(74) payload
	"a6",                                 // embedded { 6 pairs: status report payload
	"01",                                 // embedded 1: message
	"686d73672d30303034",                 // embedded "msg-0004"
	"02",                                 // embedded 2: recipient
	"5001010101010101010101010101010101", // embedded h'01010101010101010101010101010101'
	"03",                                 // embedded 3: state
	"04",                                 // embedded 4 (delivery_unknown)
	"06",                                 // embedded 6: reported_at
	"1a6553f484",                         // embedded 1700000900
	"08",                                 // embedded 8: sender
	"582064e53e346d0f70f253eff23689c61f3a14c410aa473c778a95a212e24e131bb7", // embedded h'64e53e346d0f70f253eff23689c61f3a14c410aa473c778a95a212e24e131bb7' (node B, the sending node)
	"09", // embedded 9: unknown_cause
	"01", // embedded 1 (lost_at_target)
	"03", // 3: signatures
	"81", // [ 1 signature, sorted by encoded public key
	"a2", // { 2 pairs: signature item
	"01", // 1: public key
	"a2", // { 2 pairs: public key
	"01", // 1: algorithm
	"01", // 1 (Ed25519)
	"02", // 2: key bytes
	"5820d04ab232742bb4ab3a1368bd4615e4e6d0224ab71a016baf8520a332c9778737", // h'd04ab232742bb4ab3a1368bd4615e4e6d0224ab71a016baf8520a332c9778737' (seed 0x11 x32)
	"02", // 2: signature
	"5840276a70a4f02475887a8069f30fa01a54793e1786085fd873897470bb57c806ab6bd8434ed27385c196b26ec56744b1d38ca5024c5d6359d674520486f2ab590e", // bstr(64) Ed25519 signature
)

const goldenReportLostAtTargetDigest = "a48e26a20e819113fb245bf0688c102843b495a00424ab904b76173d3bedbd39"
const goldenReportLostAtTargetSignedBytes = "117369646572612f7265636f72642d736967167369646572612f7374617475732d7265706f72742f314aa601686d73672d303030340250010101010101010101010101010101010304061a6553f48408582064e53e346d0f70f253eff23689c61f3a14c410aa473c778a95a212e24e131bb70901"

// A status report: msg-0005 is in an unknown state after the target handed it over; key 9 is absent.
var goldenReportHandedOver = hexcat(
	"a3", // { 3 pairs: the record
	"01", // 1: type
	"767369646572612f7374617475732d7265706f72742f31", // "sidera/status-report/1"
	"02",                                 // 2: payload
	"5848",                               // bstr(72) payload
	"a5",                                 // embedded { 5 pairs: status report payload
	"01",                                 // embedded 1: message
	"686d73672d30303035",                 // embedded "msg-0005"
	"02",                                 // embedded 2: recipient
	"5001010101010101010101010101010101", // embedded h'01010101010101010101010101010101'
	"03",                                 // embedded 3: state
	"04",                                 // embedded 4 (delivery_unknown)
	"06",                                 // embedded 6: reported_at
	"1a6553f4b6",                         // embedded 1700000950
	"08",                                 // embedded 8: sender
	"582064e53e346d0f70f253eff23689c61f3a14c410aa473c778a95a212e24e131bb7", // embedded h'64e53e346d0f70f253eff23689c61f3a14c410aa473c778a95a212e24e131bb7' (node B, the sending node)
	"03", // 3: signatures
	"81", // [ 1 signature, sorted by encoded public key
	"a2", // { 2 pairs: signature item
	"01", // 1: public key
	"a2", // { 2 pairs: public key
	"01", // 1: algorithm
	"01", // 1 (Ed25519)
	"02", // 2: key bytes
	"5820d04ab232742bb4ab3a1368bd4615e4e6d0224ab71a016baf8520a332c9778737", // h'd04ab232742bb4ab3a1368bd4615e4e6d0224ab71a016baf8520a332c9778737' (seed 0x11 x32)
	"02", // 2: signature
	"584028bc8f70e1b1fca6edd3b9e1847625c9a46b5939131831a42c685e8318638cc7580b21e4c108e1e4f2f7a6195ab62c596c3fb3add454c778b2232bb37ba4290f", // bstr(64) Ed25519 signature
)

const goldenReportHandedOverDigest = "d0c5d80182e91d18e2defb4b9adc9acf2ba3baf0080825a4c395f8286826c650"
const goldenReportHandedOverSignedBytes = "117369646572612f7265636f72642d736967167369646572612f7374617475732d7265706f72742f3148a501686d73672d303030350250010101010101010101010101010101010304061a6553f4b608582064e53e346d0f70f253eff23689c61f3a14c410aa473c778a95a212e24e131bb7"

// A control reply: ctl-0001 created session endpoint 0x03.
var goldenReplyCreated = hexcat(
	"a3", // { 3 pairs: the record
	"01", // 1: type
	"767369646572612f636f6e74726f6c2d7265706c792f31", // "sidera/control-reply/1"
	"02",                                 // 2: payload
	"585c",                               // bstr(92) payload
	"a5",                                 // embedded { 5 pairs: control reply payload
	"01",                                 // embedded 1: message
	"6863746c2d30303031",                 // embedded "ctl-0001"
	"02",                                 // embedded 2: recipient
	"5002020202020202020202020202020202", // embedded h'02020202020202020202020202020202'
	"03",                                 // embedded 3: issued_at
	"1a6553f38a",                         // embedded 1700000650
	"04",                                 // embedded 4: sender
	"582064e53e346d0f70f253eff23689c61f3a14c410aa473c778a95a212e24e131bb7", // embedded h'64e53e346d0f70f253eff23689c61f3a14c410aa473c778a95a212e24e131bb7' (node B, the sending node)
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
	"58400f8d514a7bd27e38ca8c7f1a18e9517eb21fbde107c55fd9872cefb25e660ac36e5f13c8f3cd0bbec1ca86c8cf064a010429ec34b88de9bba23878f771b49304", // bstr(64) Ed25519 signature
)

const goldenReplyCreatedDigest = "08797425c81793ef479657241b68e21f495eb9c7e9a7d7571769cf3fda81069a"
const goldenReplyCreatedSignedBytes = "117369646572612f7265636f72642d736967167369646572612f636f6e74726f6c2d7265706c792f315ca5016863746c2d30303031025002020202020202020202020202020202031a6553f38a04582064e53e346d0f70f253eff23689c61f3a14c410aa473c778a95a212e24e131bb705a20101025003030303030303030303030303030303"

// A control reply: ctl-0002 failed, preset unavailable.
var goldenReplyFailed = hexcat(
	"a3", // { 3 pairs: the record
	"01", // 1: type
	"767369646572612f636f6e74726f6c2d7265706c792f31", // "sidera/control-reply/1"
	"02",                                 // 2: payload
	"584c",                               // bstr(76) payload
	"a5",                                 // embedded { 5 pairs: control reply payload
	"01",                                 // embedded 1: message
	"6863746c2d30303032",                 // embedded "ctl-0002"
	"02",                                 // embedded 2: recipient
	"5002020202020202020202020202020202", // embedded h'02020202020202020202020202020202'
	"03",                                 // embedded 3: issued_at
	"1a6553f11e",                         // embedded 1700000030
	"04",                                 // embedded 4: sender
	"582064e53e346d0f70f253eff23689c61f3a14c410aa473c778a95a212e24e131bb7", // embedded h'64e53e346d0f70f253eff23689c61f3a14c410aa473c778a95a212e24e131bb7' (node B, the sending node)
	"05", // embedded 5: create result
	"a2", // embedded { 2 pairs: create result
	"01", // embedded 1: outcome
	"03", // embedded 3 (failed)
	"03", // embedded 3: reason
	"02", // embedded 2 (preset_unavailable)
	"03", // 3: signatures
	"81", // [ 1 signature, sorted by encoded public key
	"a2", // { 2 pairs: signature item
	"01", // 1: public key
	"a2", // { 2 pairs: public key
	"01", // 1: algorithm
	"01", // 1 (Ed25519)
	"02", // 2: key bytes
	"5820d04ab232742bb4ab3a1368bd4615e4e6d0224ab71a016baf8520a332c9778737", // h'd04ab232742bb4ab3a1368bd4615e4e6d0224ab71a016baf8520a332c9778737' (seed 0x11 x32)
	"02", // 2: signature
	"5840e3a6b9479976ac60e61bdde2f98d0a64960d828cfeb493f3427a2cf1b2561a126f07d6ade87737c0a4a13abea6927431e7d4e35519f949ea7749672273e13402", // bstr(64) Ed25519 signature
)

const goldenReplyFailedDigest = "6bdd5e78f399a5b2847ab9d2b6d1d0a08ddb9b69ee0290242145ef7b28cc5e0e"
const goldenReplyFailedSignedBytes = "117369646572612f7265636f72642d736967167369646572612f636f6e74726f6c2d7265706c792f314ca5016863746c2d30303032025002020202020202020202020202020202031a6553f11e04582064e53e346d0f70f253eff23689c61f3a14c410aa473c778a95a212e24e131bb705a201030302"

// A control reply: ctl-0003 close not confirmed, adapter offline.
var goldenReplyClose = hexcat(
	"a3", // { 3 pairs: the record
	"01", // 1: type
	"767369646572612f636f6e74726f6c2d7265706c792f31", // "sidera/control-reply/1"
	"02",                                 // 2: payload
	"584c",                               // bstr(76) payload
	"a5",                                 // embedded { 5 pairs: control reply payload
	"01",                                 // embedded 1: message
	"6863746c2d30303033",                 // embedded "ctl-0003"
	"02",                                 // embedded 2: recipient
	"5003030303030303030303030303030303", // embedded h'03030303030303030303030303030303'
	"03",                                 // embedded 3: issued_at
	"1a6553f128",                         // embedded 1700000040
	"04",                                 // embedded 4: sender
	"582064e53e346d0f70f253eff23689c61f3a14c410aa473c778a95a212e24e131bb7", // embedded h'64e53e346d0f70f253eff23689c61f3a14c410aa473c778a95a212e24e131bb7' (node B, the sending node)
	"06", // embedded 6: close result
	"a2", // embedded { 2 pairs: close result
	"01", // embedded 1: outcome
	"02", // embedded 2 (close_unconfirmed)
	"02", // embedded 2: reason
	"02", // embedded 2 (adapter_offline)
	"03", // 3: signatures
	"81", // [ 1 signature, sorted by encoded public key
	"a2", // { 2 pairs: signature item
	"01", // 1: public key
	"a2", // { 2 pairs: public key
	"01", // 1: algorithm
	"01", // 1 (Ed25519)
	"02", // 2: key bytes
	"5820d04ab232742bb4ab3a1368bd4615e4e6d0224ab71a016baf8520a332c9778737", // h'd04ab232742bb4ab3a1368bd4615e4e6d0224ab71a016baf8520a332c9778737' (seed 0x11 x32)
	"02", // 2: signature
	"5840e482cb9039efa9a7d759a6c53bfac3b35e68978a64c35eb8e1c2de03ecd656726b8fe713c9df4fe42b757bae42839d60928425d7e3b0994db8a744e4db939c01", // bstr(64) Ed25519 signature
)

const goldenReplyCloseDigest = "ce5b1569a09db2bd287b88ca65d265b3dbbb243f968c108775b442a91a10ea6a"
const goldenReplyCloseSignedBytes = "117369646572612f7265636f72642d736967167369646572612f636f6e74726f6c2d7265706c792f314ca5016863746c2d30303033025003030303030303030303030303030303031a6553f12804582064e53e346d0f70f253eff23689c61f3a14c410aa473c778a95a212e24e131bb706a201020202"

// A control reply: ctl-0005 interrupt failed, native error.
var goldenReplyInterrupt = hexcat(
	"a3", // { 3 pairs: the record
	"01", // 1: type
	"767369646572612f636f6e74726f6c2d7265706c792f31", // "sidera/control-reply/1"
	"02",                                 // 2: payload
	"584c",                               // bstr(76) payload
	"a5",                                 // embedded { 5 pairs: control reply payload
	"01",                                 // embedded 1: message
	"6863746c2d30303035",                 // embedded "ctl-0005"
	"02",                                 // embedded 2: recipient
	"5003030303030303030303030303030303", // embedded h'03030303030303030303030303030303'
	"03",                                 // embedded 3: issued_at
	"1a6553f132",                         // embedded 1700000050
	"04",                                 // embedded 4: sender
	"582064e53e346d0f70f253eff23689c61f3a14c410aa473c778a95a212e24e131bb7", // embedded h'64e53e346d0f70f253eff23689c61f3a14c410aa473c778a95a212e24e131bb7' (node B, the sending node)
	"07", // embedded 7: interrupt result
	"a2", // embedded { 2 pairs: interrupt result
	"01", // embedded 1: outcome
	"04", // embedded 4 (failed)
	"02", // embedded 2: reason
	"03", // embedded 3 (native_error)
	"03", // 3: signatures
	"81", // [ 1 signature, sorted by encoded public key
	"a2", // { 2 pairs: signature item
	"01", // 1: public key
	"a2", // { 2 pairs: public key
	"01", // 1: algorithm
	"01", // 1 (Ed25519)
	"02", // 2: key bytes
	"5820d04ab232742bb4ab3a1368bd4615e4e6d0224ab71a016baf8520a332c9778737", // h'd04ab232742bb4ab3a1368bd4615e4e6d0224ab71a016baf8520a332c9778737' (seed 0x11 x32)
	"02", // 2: signature
	"58408f14715c6025004c9b29b81bbd4f086922e393fc2ad39ee3c6e6c101dff34d99ea254995f361fea119b5374176215626f5a8f00e5c1523ba5ac5860399324403", // bstr(64) Ed25519 signature
)

const goldenReplyInterruptDigest = "06b6c0818d0e81e273a36b8dbbf62356fab78ad3fee09880b5f4236bbc244b22"
const goldenReplyInterruptSignedBytes = "117369646572612f7265636f72642d736967167369646572612f636f6e74726f6c2d7265706c792f314ca5016863746c2d30303035025003030303030303030303030303030303031a6553f13204582064e53e346d0f70f253eff23689c61f3a14c410aa473c778a95a212e24e131bb707a201040203"

// A control reply: endpoint.status of endpoint 0x01.
var goldenReplyStatus = hexcat(
	"a3", // { 3 pairs: the record
	"01", // 1: type
	"767369646572612f636f6e74726f6c2d7265706c792f31", // "sidera/control-reply/1"
	"02",                                 // 2: payload
	"586d",                               // bstr(109) payload
	"a5",                                 // embedded { 5 pairs: control reply payload
	"01",                                 // embedded 1: message
	"6863746c2d30303034",                 // embedded "ctl-0004"
	"02",                                 // embedded 2: recipient
	"5001010101010101010101010101010101", // embedded h'01010101010101010101010101010101'
	"03",                                 // embedded 3: issued_at
	"1a6553f15a",                         // embedded 1700000090
	"04",                                 // embedded 4: sender
	"582064e53e346d0f70f253eff23689c61f3a14c410aa473c778a95a212e24e131bb7", // embedded h'64e53e346d0f70f253eff23689c61f3a14c410aa473c778a95a212e24e131bb7' (node B, the sending node)
	"08",               // embedded 8: status result
	"a5",               // embedded { 5 pairs: status result
	"01",               // embedded 1: activity
	"02",               // embedded 2 (busy)
	"02",               // embedded 2: queued
	"03",               // embedded 3
	"03",               // embedded 3: conditions
	"82",               // embedded [ conditions
	"01",               // embedded 1 (adapter_offline)
	"04",               // embedded 4 (server_request_rejected)
	"04",               // embedded 4: presets
	"82",               // embedded [ presets sorted by name
	"a2",               // embedded { 2 pairs: preset
	"01",               // embedded 1: name
	"6764656661756c74", // embedded "default"
	"02",               // embedded 2: available
	"f5",               // embedded true
	"a2",               // embedded { 2 pairs: preset
	"01",               // embedded 1: name
	"6466617374",       // embedded "fast"
	"02",               // embedded 2: available
	"f4",               // embedded false
	"05",               // embedded 5: as_of
	"1a6553f15a",       // embedded 1700000090
	"03",               // 3: signatures
	"81",               // [ 1 signature, sorted by encoded public key
	"a2",               // { 2 pairs: signature item
	"01",               // 1: public key
	"a2",               // { 2 pairs: public key
	"01",               // 1: algorithm
	"01",               // 1 (Ed25519)
	"02",               // 2: key bytes
	"5820d04ab232742bb4ab3a1368bd4615e4e6d0224ab71a016baf8520a332c9778737", // h'd04ab232742bb4ab3a1368bd4615e4e6d0224ab71a016baf8520a332c9778737' (seed 0x11 x32)
	"02", // 2: signature
	"5840631d387924d98e0cee50faefb98adef55b21d10a3b818384c8e25a927bf4387cc257b0b24e062efe8c143e4f6228dbca9096ddb97461ce69bf6481ba61f69702", // bstr(64) Ed25519 signature
)

const goldenReplyStatusDigest = "1aceb82e593abedd7c713f1de00990bce04f0da69b7e37ccfc18293e33b96f20"
const goldenReplyStatusSignedBytes = "117369646572612f7265636f72642d736967167369646572612f636f6e74726f6c2d7265706c792f316da5016863746c2d30303034025001010101010101010101010101010101031a6553f15a04582064e53e346d0f70f253eff23689c61f3a14c410aa473c778a95a212e24e131bb708a501020203038201040482a2016764656661756c7402f5a201646661737402f4051a6553f15a"

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
	"58e5",                               // bstr(229) evidence 0: the control reply
	"a3",                                 // embedded { 3 pairs: the record
	"01",                                 // embedded 1: type
	"767369646572612f636f6e74726f6c2d7265706c792f31", // embedded "sidera/control-reply/1"
	"02",                                 // embedded 2: payload
	"585c",                               // embedded bstr(92) payload
	"a5",                                 // embedded embedded { 5 pairs: control reply payload
	"01",                                 // embedded embedded 1: message
	"6863746c2d30303031",                 // embedded embedded "ctl-0001"
	"02",                                 // embedded embedded 2: recipient
	"5002020202020202020202020202020202", // embedded embedded h'02020202020202020202020202020202'
	"03",                                 // embedded embedded 3: issued_at
	"1a6553f38a",                         // embedded embedded 1700000650
	"04",                                 // embedded embedded 4: sender
	"582064e53e346d0f70f253eff23689c61f3a14c410aa473c778a95a212e24e131bb7", // embedded embedded h'64e53e346d0f70f253eff23689c61f3a14c410aa473c778a95a212e24e131bb7' (node B, the sending node)
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
	"58400f8d514a7bd27e38ca8c7f1a18e9517eb21fbde107c55fd9872cefb25e660ac36e5f13c8f3cd0bbec1ca86c8cf064a010429ec34b88de9bba23878f771b49304", // embedded bstr(64) Ed25519 signature
	"58d7", // bstr(215) evidence 1: the status report
	"a3",   // embedded { 3 pairs: the record
	"01",   // embedded 1: type
	"767369646572612f7374617475732d7265706f72742f31", // embedded "sidera/status-report/1"
	"02",                                 // embedded 2: payload
	"584e",                               // embedded bstr(78) payload
	"a6",                                 // embedded embedded { 6 pairs: status report payload
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
	"08",                                 // embedded embedded 8: sender
	"582064e53e346d0f70f253eff23689c61f3a14c410aa473c778a95a212e24e131bb7", // embedded embedded h'64e53e346d0f70f253eff23689c61f3a14c410aa473c778a95a212e24e131bb7' (node B, the sending node)
	"03", // embedded 3: signatures
	"81", // embedded [ 1 signature, sorted by encoded public key
	"a2", // embedded { 2 pairs: signature item
	"01", // embedded 1: public key
	"a2", // embedded { 2 pairs: public key
	"01", // embedded 1: algorithm
	"01", // embedded 1 (Ed25519)
	"02", // embedded 2: key bytes
	"5820d04ab232742bb4ab3a1368bd4615e4e6d0224ab71a016baf8520a332c9778737", // embedded h'd04ab232742bb4ab3a1368bd4615e4e6d0224ab71a016baf8520a332c9778737' (seed 0x11 x32)
	"02", // embedded 2: signature
	"5840444e2a632066028f9064bdf6add113e9b6978c8195273ac70bcf3c1e533e01fa964e2503931dfc20473e2f278da9590391b512ffaf4cda6dfe11252ba0856c08", // embedded bstr(64) Ed25519 signature
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

const goldenSessionCreateRequestDigest = "969d368e36698f0ef78f05632599890299c704a0c66cb26b8f9670c006280d51"
const goldenReplyRequestDigest = "36b57c2c0c2c23954a786ecb3d22b1c2574f7ca99ba1d413fd147fca60452abe"
