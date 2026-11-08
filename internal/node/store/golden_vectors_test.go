package store_test

import "github.com/aianyai/sidera/internal/node/store"

// goldenRecords are complete record files of every kind, written out by hand (the checksums and names
// were computed with a separate implementation of CRC32C and SHA-256): the
// body is the canonical JSON the format defines, the first line carries its
// CRC32C, its length and the key, and the file name is the hash of kind and key (for a file of out/ the sending endpoint comes before it).
var goldenRecords = []struct {
	name string
	dir  string
	file string
	hash string
}{
	{name: "seq", dir: store.SeqDir,
		file: "sidera-record 1 seq 2e6da816 116 ZXAxenlnM3p5ZzN6eWczenlnM3p5ZzN6eWczenlqMGtjZ3E\n{\"floors\":{\"node1qgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpq7haevd/ep1zgfpyysjzgfpyysjzgfpyysjzgtln4pc\":5}}",
		hash: "ac159df1a18c7300592cffe5b0d484ba1aed51dd80a0ad21faf5a269653600fa.rec"},
	{name: "alert", dir: store.AlertsDir,
		file: "sidera-record 1 alert 4d2d5cb1 215 MDAxMTIyMzM0NDU1NjY3Nzg4OTlhYWJiY2NkZGVlZmY\n{\"kind\":\"corrupt_record\",\"at\":\"2026-10-24T09:00:00Z\",\"detail\":\"state/in/x.rec: body checksum mismatch\",\"subject\":\"state/in/x.rec\",\"acked\":{\"at\":\"2026-10-24T09:30:00Z\",\"operation\":\"0123456789abcdef0123456789abcdef\"}}",
		hash: "fb3ef3f36f3a72a9ca9e4124d55e48f673c0324b5627ef97071b93eca5937fe4.rec"},
	{name: "checkpoint", dir: store.CheckpointsDir,
		file: "sidera-record 1 checkpoint 9d9acba8 22 YXR0ZW1wdCAwYjRkNmUxYS0yZjNjLTRkNWUtOGY2MC03MTgyOTNhNGI1YzY\n{\"state\":{\"cursor\":7}}",
		hash: "1cb08d300721d7cd582a3f5ecc5ccd020a58675c9767adaf97db814482440962.rec"},
	{name: "admin", dir: store.AdminDir,
		file: "sidera-record 1 admin 6f7e3662 195 bG9jYWwgMDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY\n{\"fingerprint\":\"abababababababababababababababababababababababababababababababab\",\"op\":\"endpoint.close\",\"result\":{\"closed\":true},\"at\":\"2026-10-24T09:00:00Z\",\"retain_until\":\"2026-10-31T09:00:00Z\"}",
		hash: "db0d8331675d3440070a42ba987583277c562f3d0e301da73ecba349f40043d5.rec"},
	{name: "attempts", dir: store.AttemptsDir,
		file: "sidera-record 1 attempts 67604fa7 150 ZXAxejUyMzI5ZzR6NTIzMjlnNHo1MjMyOWc0ejVrOHEyZzQ\n{\"attempts\":[{\"at\":\"2026-10-24T08:50:00Z\",\"outcome\":\"delivered\",\"kind\":\"first\"}],\"blocked\":{\"cause\":\"adapter_offline\",\"since\":\"2026-10-24T08:55:00Z\"}}",
		hash: "3a94785304195d38d01dcf3be2ea7924396996b0008b5ef1080d044a90400617.rec"},
	{name: "endpoint", dir: store.EndpointsDir,
		file: "sidera-record 1 endpoint 14d0a10f 254 ZXAxejUyMzI5ZzR6NTIzMjlnNHo1MjMyOWc0ejVrOHEyZzQ\n{\"kind\":\"session\",\"instance\":\"claude\",\"status\":\"creating\",\"token\":\"RUVFRUVFRUVFRUVFRUVFRUVFRUVFRUVFRUVFRUVFRUU\",\"session\":{\"preset\":\"default\",\"tool_access\":[\"SIDERA_TOKEN\"]},\"created_by\":\"b5c8d7b80655e880380440180e306644b581f191eb47622b58ece7c2bac4748d\"}",
		hash: "c2ccbb5272953d655682b33fc9219b88055463bccb316d5d1750793bbe10e030.rec"},
	// A closed session endpoint whose native session the adapter confirmed released, and
	// the same endpoint with the native session still to be released.
	{name: "endpoint released", dir: store.EndpointsDir,
		file: "sidera-record 1 endpoint 33f5b75c 236 ZXAxejUyMzI5ZzR6NTIzMjlnNHo1MjMyOWc0ejVrOHEyZzQ\n{\"kind\":\"session\",\"instance\":\"claude\",\"status\":\"closed\",\"session\":{\"preset\":\"default\",\"native_ref\":\"sess_1\",\"tool_access\":[\"SIDERA_TOKEN\"],\"released\":true},\"created_by\":\"b5c8d7b80655e880380440180e306644b581f191eb47622b58ece7c2bac4748d\"}",
		hash: "c2ccbb5272953d655682b33fc9219b88055463bccb316d5d1750793bbe10e030.rec"},
	{name: "endpoint closed unreleased", dir: store.EndpointsDir,
		file: "sidera-record 1 endpoint 18a620b2 220 ZXAxejUyMzI5ZzR6NTIzMjlnNHo1MjMyOWc0ejVrOHEyZzQ\n{\"kind\":\"session\",\"instance\":\"claude\",\"status\":\"closed\",\"session\":{\"preset\":\"default\",\"native_ref\":\"sess_1\",\"tool_access\":[\"SIDERA_TOKEN\"]},\"created_by\":\"b5c8d7b80655e880380440180e306644b581f191eb47622b58ece7c2bac4748d\"}",
		hash: "c2ccbb5272953d655682b33fc9219b88055463bccb316d5d1750793bbe10e030.rec"},
	// An attached session: a session a person opened, open, with its native session from the
	// start and neither preset nor tool access. The origin is written only for it.
	{name: "endpoint attached", dir: store.EndpointsDir,
		file: "sidera-record 1 endpoint fea1301f 162 ZXAxejUyMzI5ZzR6NTIzMjlnNHo1MjMyOWc0ejVrOHEyZzQ\n{\"kind\":\"session\",\"instance\":\"claude\",\"status\":\"open\",\"token\":\"RUVFRUVFRUVFRUVFRUVFRUVFRUVFRUVFRUVFRUVFRUU\",\"session\":{\"origin\":\"attached\",\"native_ref\":\"sess_1\"}}",
		hash: "c2ccbb5272953d655682b33fc9219b88055463bccb316d5d1750793bbe10e030.rec"},
	// An attached session that was closed: it keeps its origin and its native session, and
	// owes no release, so nothing in the record says it was released.
	{name: "endpoint attached closed", dir: store.EndpointsDir,
		file: "sidera-record 1 endpoint dc12d27b 110 ZXAxejUyMzI5ZzR6NTIzMjlnNHo1MjMyOWc0ejVrOHEyZzQ\n{\"kind\":\"session\",\"instance\":\"claude\",\"status\":\"closed\",\"session\":{\"origin\":\"attached\",\"native_ref\":\"sess_1\"}}",
		hash: "c2ccbb5272953d655682b33fc9219b88055463bccb316d5d1750793bbe10e030.rec"},
	// A direct endpoint with the name and the description its adapter registered
	// it under (the name holds a character of two bytes: the length counts bytes).
	{name: "endpoint named", dir: store.EndpointsDir,
		file: "sidera-record 1 endpoint 1610de94 188 ZXAxemN0cHY5c2t6Y3Rwdjlza3pjdHB2OXNremMwaDk4cGQ\n{\"kind\":\"direct\",\"instance\":\"chat\",\"display_name\":\"B\u00fcro phone\",\"description\":\"The front desk line, answered 9 to 5.\",\"status\":\"open\",\"token\":\"RkZGRkZGRkZGRkZGRkZGRkZGRkZGRkZGRkZGRkZGRkY\"}",
		hash: "d4a4a31d1de4f1e5c78f896f609ab71e5654007723cbd534d6e95f9450f19046.rec"},
	{name: "in", dir: store.InDir,
		file: "sidera-record 1 in 8e41365f 579 bm9kZTFxZ3BxeXFzenFncHF5cXN6cWdwcXlxc3pxZ3BxeXFzenFncHF5cXN6cWdwcXlxc3pxZ3BxN2hhZXZkIGVwMXp2ZjN4eWNuenZmM3h5Y256dmYzeHljbnp2bDV1NGNkIG1zZy0wMDAxIGVwMXpnZnB5eXNqemdmcHl5c2p6Z2ZweXlzanpndGxuNHBj\n{\"record\":{\"side\":\"target\",\"state\":\"accepted\",\"state_at\":\"2026-10-24T09:00:00Z\",\"accept_before\":\"2026-10-24T09:01:00Z\",\"report_by\":\"2026-10-24T12:03:00Z\",\"backlog_deadline\":\"2026-10-24T09:30:00Z\"},\"dedup\":{\"digest\":\"0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20\",\"retain_until\":\"2026-10-24T12:03:00Z\"},\"auth\":{\"kind\":\"allow\"},\"reply\":{\"reason\":\"disabled\"},\"handout\":{\"content_type\":\"text/plain\",\"sent_at\":\"2026-10-24T09:00:00Z\"},\"accepted_at\":\"2026-10-24T09:00:00Z\",\"body\":{\"sha256\":\"ea8c004183bd3bd33e27907ed5cb35a24808cb99ab2bb41a727d0c675f5bcdfc\",\"len\":17}}",
		hash: "9217a562696c0e2029e1e14cca852cea2de0be95435a39f5921d9b797081f128.rec"},
	// A notice of the system endpoint is exempt traffic: the record says so and
	// holds neither a verdict of the authorization check nor a reply permission.
	{name: "in notice", dir: store.InDir,
		file: "sidera-record 1 in 8e7a244d 539 bm9kZTFxZ3BxeXFzenFncHF5cXN6cWdwcXlxc3pxZ3BxeXFzenFncHF5cXN6cWdwcXlxc3pxZ3BxN2hhZXZkIGVwMWxsbGxsbGxsbGxsbGxsbGxsbGxsbGxsbGx1c2E2MjhxIG1zZy0wMDAxIGVwMXpnZnB5eXNqemdmcHl5c2p6Z2ZweXlzanpndGxuNHBj\n{\"record\":{\"side\":\"target\",\"state\":\"accepted\",\"state_at\":\"2026-10-24T09:00:00Z\",\"exempt\":true,\"accept_before\":\"2026-10-24T09:01:00Z\",\"report_by\":\"2026-10-24T12:03:00Z\",\"backlog_deadline\":\"2026-10-24T09:30:00Z\"},\"dedup\":{\"digest\":\"0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20\",\"retain_until\":\"2026-10-24T12:03:00Z\"},\"handout\":{\"content_type\":\"text/plain\",\"sent_at\":\"2026-10-24T09:00:00Z\"},\"accepted_at\":\"2026-10-24T09:00:00Z\",\"body\":{\"sha256\":\"ea8c004183bd3bd33e27907ed5cb35a24808cb99ab2bb41a727d0c675f5bcdfc\",\"len\":17}}",
		hash: "a035bbd39591f93d4dbadb7fed6023a8001e3a56c443fed431057ec792b43006.rec"},
	{name: "out", dir: store.OutDir,
		file: "sidera-record 1 out c7fba894 809 ZXAxenlnM3p5ZzN6eWczenlnM3p5ZzN6eWczenlqMGtjZ3EgbXNnLTAwMDEgbm9kZTFxZ3BxeXFzenFncHF5cXN6cWdwcXlxc3pxZ3BxeXFzenFncHF5cXN6cWdwcXlxc3pxZ3BxN2hhZXZkL2VwMXpnZnB5eXNqemdmcHl5c2p6Z2ZweXlzanpndGxuNHBj\n{\"kind\":\"message\",\"body_non_empty\":true,\"accept_before\":\"2026-10-24T09:01:00Z\",\"recipients\":[{\"address\":\"node1qgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpq7haevd/ep1zgfpyysjzgfpyysjzgfpyysjzgtln4pc\",\"delivery\":{\"side\":\"source\",\"state\":\"queued\",\"state_at\":\"2026-10-24T09:00:00Z\",\"accept_before\":\"2026-10-24T09:01:00Z\",\"report_by\":\"2026-10-24T12:03:00Z\"}}],\"to\":[\"node1qgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpq7haevd/ep1zgfpyysjzgfpyysjzgfpyysjzgtln4pc\"],\"seq\":[{\"endpoint\":\"ep1zgfpyysjzgfpyysjzgfpyysjzgtln4pc\",\"seq\":1}],\"sent_at\":\"2026-10-24T09:00:00Z\",\"report_by\":\"2026-10-24T12:03:00Z\",\"content_type\":\"text/plain\",\"body\":{\"sha256\":\"ea8c004183bd3bd33e27907ed5cb35a24808cb99ab2bb41a727d0c675f5bcdfc\",\"len\":17},\"body_digest\":\"0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20\"}",
		hash: e1 + ".32bb53b3134a3e55c25a074dd89be386eab83340dfc46b65e4478d271eeb9db0.rec"},
	// The sender discloses the recipients: each recipient may see the whole list, and
	// a resend must carry the same flag, which the digest of the envelope covers.
	{name: "out disclosed", dir: store.OutDir,
		file: "sidera-record 1 out 2bd38a39 836 ZXAxenlnM3p5ZzN6eWczenlnM3p5ZzN6eWczenlqMGtjZ3EgbXNnLTAwMDEgbm9kZTFxZ3BxeXFzenFncHF5cXN6cWdwcXlxc3pxZ3BxeXFzenFncHF5cXN6cWdwcXlxc3pxZ3BxN2hhZXZkL2VwMXpnZnB5eXNqemdmcHl5c2p6Z2ZweXlzanpndGxuNHBj\n{\"kind\":\"message\",\"body_non_empty\":true,\"accept_before\":\"2026-10-24T09:01:00Z\",\"recipients\":[{\"address\":\"node1qgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpq7haevd/ep1zgfpyysjzgfpyysjzgfpyysjzgtln4pc\",\"delivery\":{\"side\":\"source\",\"state\":\"queued\",\"state_at\":\"2026-10-24T09:00:00Z\",\"accept_before\":\"2026-10-24T09:01:00Z\",\"report_by\":\"2026-10-24T12:03:00Z\"}}],\"to\":[\"node1qgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpq7haevd/ep1zgfpyysjzgfpyysjzgfpyysjzgtln4pc\"],\"disclose_recipients\":true,\"seq\":[{\"endpoint\":\"ep1zgfpyysjzgfpyysjzgfpyysjzgtln4pc\",\"seq\":1}],\"sent_at\":\"2026-10-24T09:00:00Z\",\"report_by\":\"2026-10-24T12:03:00Z\",\"content_type\":\"text/plain\",\"body\":{\"sha256\":\"ea8c004183bd3bd33e27907ed5cb35a24808cb99ab2bb41a727d0c675f5bcdfc\",\"len\":17},\"body_digest\":\"0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20\"}",
		hash: e1 + ".32bb53b3134a3e55c25a074dd89be386eab83340dfc46b65e4478d271eeb9db0.rec"},
	// A session creation of the local runtime that chooses a model and an effort: the file is
	// of kind out-ctl, and the envelope part of the record keeps both.
	{name: "out session creation with a model and an effort", dir: store.OutDir,
		file: "sidera-record 1 out-ctl 84b73756 880 ZXAxenlnM3p5ZzN6eWczenlnM3p5ZzN6eWczenlqMGtjZ3EgbXNnLTAwMDEgbm9kZTFxZ3BxeXFzenFncHF5cXN6cWdwcXlxc3pxZ3BxeXFzenFncHF5cXN6cWdwcXlxc3pxZ3BxN2hhZXZkL2VwMXpnZnB5eXNqemdmcHl5c2p6Z2ZweXlzanpndGxuNHBj\n{\"kind\":\"control.session.create\",\"body_non_empty\":true,\"accept_before\":\"2026-10-24T09:01:00Z\",\"recipients\":[{\"address\":\"node1qgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpq7haevd/ep1zgfpyysjzgfpyysjzgfpyysjzgtln4pc\",\"delivery\":{\"side\":\"source\",\"state\":\"queued\",\"state_at\":\"2026-10-24T09:00:00Z\",\"accept_before\":\"2026-10-24T09:01:00Z\",\"report_by\":\"2026-10-24T12:03:00Z\"}}],\"to\":[\"node1qgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpq7haevd/ep1zgfpyysjzgfpyysjzgfpyysjzgtln4pc\"],\"seq\":[{\"endpoint\":\"ep1zgfpyysjzgfpyysjzgfpyysjzgtln4pc\",\"seq\":1}],\"sent_at\":\"2026-10-24T09:00:00Z\",\"report_by\":\"2026-10-24T12:03:00Z\",\"content_type\":\"text/plain\",\"preset\":\"default\",\"model\":\"sonnet[1m]\",\"effort\":\"high\",\"body\":{\"sha256\":\"ea8c004183bd3bd33e27907ed5cb35a24808cb99ab2bb41a727d0c675f5bcdfc\",\"len\":17},\"body_digest\":\"0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20\"}",
		hash: e1 + ".3cf1dbb4f4e3bbb53e22b888eb96d1d20fea3b9b5fc594449044caf676d00dbe.rec"},
	{name: "ctl", dir: store.CtlDir,
		file: "sidera-record 1 ctl 12a8daed 600 bm9kZTFxZ3BxeXFzenFncHF5cXN6cWdwcXlxc3pxZ3BxeXFzenFncHF5cXN6cWdwcXlxc3pxZ3BxN2hhZXZkIGVwMXp2ZjN4eWNuenZmM3h5Y256dmYzeHljbnp2bDV1NGNkIG1zZy0wMDAxIGVwMXpzMnBnOXE1enMycGc5cTV6czJwZzlxNXpzenYwMjNx\n{\"record\":{\"side\":\"target\",\"state\":\"accepted\",\"state_at\":\"2026-10-24T09:00:00Z\",\"control\":true,\"accept_before\":\"2026-10-24T09:01:00Z\",\"report_by\":\"2026-10-24T12:03:00Z\"},\"dedup\":{\"digest\":\"0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20\",\"retain_until\":\"2026-10-24T12:03:00Z\"},\"kind\":\"control.session.create\",\"preset\":\"default\",\"session_endpoint\":\"ep1z52329g4z52329g4z52329g4z5k8q2g4\",\"accepted_at\":\"2026-10-24T09:00:00Z\",\"sent_at\":\"2026-10-24T09:00:00Z\",\"intent\":{\"attempt\":\"0b4d6e1a-2f3c-4d5e-8f60-718293a4b5c6\",\"endpoint\":\"ep1z52329g4z52329g4z52329g4z5k8q2g4\",\"state\":\"creating\"}}",
		hash: "b5c8d7b80655e880380440180e306644b581f191eb47622b58ece7c2bac4748d.rec"},
	// A creation whose requester chose a model and an effort within the preset: the record
	// keeps both, and a creation that chose nothing writes neither.
	{name: "ctl with a model and an effort", dir: store.CtlDir,
		file: "sidera-record 1 ctl a2bec448 637 bm9kZTFxZ3BxeXFzenFncHF5cXN6cWdwcXlxc3pxZ3BxeXFzenFncHF5cXN6cWdwcXlxc3pxZ3BxN2hhZXZkIGVwMXp2ZjN4eWNuenZmM3h5Y256dmYzeHljbnp2bDV1NGNkIG1zZy0wMDAxIGVwMXpzMnBnOXE1enMycGc5cTV6czJwZzlxNXpzenYwMjNx\n{\"record\":{\"side\":\"target\",\"state\":\"accepted\",\"state_at\":\"2026-10-24T09:00:00Z\",\"control\":true,\"accept_before\":\"2026-10-24T09:01:00Z\",\"report_by\":\"2026-10-24T12:03:00Z\"},\"dedup\":{\"digest\":\"0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20\",\"retain_until\":\"2026-10-24T12:03:00Z\"},\"kind\":\"control.session.create\",\"preset\":\"default\",\"model\":\"sonnet[1m]\",\"effort\":\"high\",\"session_endpoint\":\"ep1z52329g4z52329g4z52329g4z5k8q2g4\",\"accepted_at\":\"2026-10-24T09:00:00Z\",\"sent_at\":\"2026-10-24T09:00:00Z\",\"intent\":{\"attempt\":\"0b4d6e1a-2f3c-4d5e-8f60-718293a4b5c6\",\"endpoint\":\"ep1z52329g4z52329g4z52329g4z5k8q2g4\",\"state\":\"creating\"}}",
		hash: "b5c8d7b80655e880380440180e306644b581f191eb47622b58ece7c2bac4748d.rec"},
	// A creation that carries a message for its session: the control record keeps the
	// content type and the digest and length of the body (SHA-256 of "body of message 1",
	// 17 bytes) that the delivery derived from it holds in a file of its own.
	{name: "ctl with an initial message", dir: store.CtlDir,
		file: "sidera-record 1 ctl c58227cb 772 bm9kZTFxZ3BxeXFzenFncHF5cXN6cWdwcXlxc3pxZ3BxeXFzenFncHF5cXN6cWdwcXlxc3pxZ3BxN2hhZXZkIGVwMXp2ZjN4eWNuenZmM3h5Y256dmYzeHljbnp2bDV1NGNkIG1zZy0wMDAxIGVwMXpzMnBnOXE1enMycGc5cTV6czJwZzlxNXpzenYwMjNx\n{\"record\":{\"side\":\"target\",\"state\":\"accepted\",\"state_at\":\"2026-10-24T09:00:00Z\",\"control\":true,\"accept_before\":\"2026-10-24T09:01:00Z\",\"report_by\":\"2026-10-24T12:03:00Z\"},\"dedup\":{\"digest\":\"0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20\",\"retain_until\":\"2026-10-24T12:03:00Z\"},\"kind\":\"control.session.create\",\"preset\":\"default\",\"session_endpoint\":\"ep1z52329g4z52329g4z52329g4z5k8q2g4\",\"accepted_at\":\"2026-10-24T09:00:00Z\",\"endpoint_max_queue_ns\":1800000000000,\"sent_at\":\"2026-10-24T09:00:00Z\",\"initial\":{\"content_type\":\"text/plain\",\"body\":{\"sha256\":\"ea8c004183bd3bd33e27907ed5cb35a24808cb99ab2bb41a727d0c675f5bcdfc\",\"len\":17}},\"intent\":{\"attempt\":\"0b4d6e1a-2f3c-4d5e-8f60-718293a4b5c6\",\"endpoint\":\"ep1z52329g4z52329g4z52329g4z5k8q2g4\",\"state\":\"creating\"}}",
		hash: "b5c8d7b80655e880380440180e306644b581f191eb47622b58ece7c2bac4748d.rec"},
	// A control signal the target refused at intake: rejected with the reason, and nothing
	// of an execution (no session endpoint, no intent, no reply, no body).
	{name: "ctl refused", dir: store.CtlDir,
		file: "sidera-record 1 ctl b1923e90 443 bm9kZTFxZ3BxeXFzenFncHF5cXN6cWdwcXlxc3pxZ3BxeXFzenFncHF5cXN6cWdwcXlxc3pxZ3BxN2hhZXZkIGVwMXp2ZjN4eWNuenZmM3h5Y256dmYzeHljbnp2bDV1NGNkIG1zZy0wMDAxIGVwMXpzMnBnOXE1enMycGc5cTV6czJwZzlxNXpzenYwMjNx\n{\"record\":{\"side\":\"target\",\"state\":\"rejected\",\"state_at\":\"2026-10-24T09:00:00Z\",\"reject\":\"endpoint_missing\",\"control\":true,\"accept_before\":\"2026-10-24T09:01:00Z\",\"report_by\":\"2026-10-24T12:03:00Z\"},\"dedup\":{\"digest\":\"0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20\",\"retain_until\":\"2026-10-24T12:03:00Z\"},\"kind\":\"control.session.create\",\"preset\":\"default\",\"accepted_at\":\"2026-10-24T09:00:00Z\",\"sent_at\":\"2026-10-24T09:00:00Z\"}",
		hash: "b5c8d7b80655e880380440180e306644b581f191eb47622b58ece7c2bac4748d.rec"},
	// The replacement of a source record lost to storage corruption, discovered at
	// 10:00. Its report_by is the discovery plus the longest delivery deadline
	// (1 h), the clock tolerance (2 min) and the report offset (3 h 2 min): 14:04.
	// A target keeps no replacement, so there is no such vector for in/ and ctl/.
	{name: "out lost at the sender", dir: store.OutDir,
		file: "sidera-record 1 out c2b6e47c 576 ZXAxenlnM3p5ZzN6eWczenlnM3p5ZzN6eWczenlqMGtjZ3EgbXNnLTAwMDIgbm9kZTFxZ3BxeXFzenFncHF5cXN6cWdwcXlxc3pxZ3BxeXFzenFncHF5cXN6cWdwcXlxc3pxZ3BxN2hhZXZkL2VwMXpnZnB5eXNqemdmcHl5c2p6Z2ZweXlzanpndGxuNHBj\n{\"lost\":{\"at\":\"2026-10-24T10:00:00Z\",\"deliveries\":[{\"side\":\"source\",\"state\":\"delivery_unknown\",\"state_at\":\"2026-10-24T10:00:00Z\",\"unknown\":\"lost_at_sender\",\"lost\":true,\"report_by\":\"2026-10-24T14:04:00Z\",\"reported\":[\"lost_at_sender\"]}]},\"feedback\":[{\"sender\":\"node1qyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqsnp67e9/ep1zyg3zyg3zyg3zyg3zyg3zyg3zyj0kcgq\",\"message\":\"msg-0002\",\"recipient\":\"node1qgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpq7haevd/ep1zgfpyysjzgfpyysjzgfpyysjzgtln4pc\",\"kind\":\"lost_at_sender\",\"state\":\"delivery_unknown\",\"at\":\"2026-10-24T10:00:00Z\"}]}",
		hash: e1 + ".ef9ba86060ef0b002fd40522d8da8a67e6010656eb53e54bc573c0ad0af540d5.rec"},
	// The notice of a source file nothing could be read from: named after the damaged
	// file (the hash of its name is in the key), holding the one feedback item until a
	// notice covers it.
	{name: "out lost notice", dir: store.OutDir,
		file: "sidera-record 1 out-lost 521713ef 155 ZXAxenlnM3p5ZzN6eWczenlnM3p5ZzN6eWczenlqMGtjZ3EgMDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWYwMTIzNDU2Nzg5YWJjZGVmMDEyMzQ1Njc4OWFiY2RlZg\n{\"at\":\"2026-10-24T10:00:00Z\",\"feedback\":[{\"sender\":\"node1qyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqsnp67e9/ep1zyg3zyg3zyg3zyg3zyg3zyg3zyj0kcgq\"}]}",
		hash: e1 + ".14eae9848c676a357c5cf6ee0eb572deeb46e4a3de1cee1084546d314330433b.rec"},
	// The records of trust and of peers hold signed statements of other parties, made
	// with deterministic keys (trustSigner): seeds 1 and 2 for a credential and its
	// revocation, signed by the node of seed 1, and seed 2 for the certificate of a
	// peer. A credential and a revocation are the record's canonical bytes
	// themselves; a peer holds them as base64url in a JSON body. Unlike the vectors
	// above these four were produced by the encoder, because a signed record is not
	// written by hand, and are read back by the decoder.
	{name: "credential", dir: store.TrustDir,
		file: "sidera-record 1 credential 73a25906 349 ZGZjMDM3MzRkYzc4MjFkMWYxNjExNmVkZmY1NmRmYThhYTQxOWQzYzE5ZmE4NTBiZDcyMTU2ZjczYjBmNGE5YQ\n\xa3\x01x\x19sidera/trust-credential/1\x02Xe\xa5\x01X [\x82[\xcem\x88\xdd(j\xac\x8c\x8a\xbc\xc0\x80\xffb\x92\xaa\xf6\x10nP\x00\x17Z\xc7\b\xd9\xe6\xa8\xc0\x02X \xfd\x9c\xe51+\u02ec_\xa0\xad\x89\xa5%\xa0\x0f4\x941'\xbco\xdb\xdc\xfe\xcb|2\xb6\x04hP\x1f\x03\x1aj\xdcs\x90\x04\x1ak\x04\x00\x90\x05P\x01\x02\x01\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x03\x82\xa2\x01\xa2\x01\x01\x02X \x819w\x0e\xa8}\x17_V\xa3Tf\xc3L~\xcc\u02cd\x8a\x91\xb4\xee7\xa2]\xf6\x0f[\x8f\u0273\x94\x02X@\x95\xc7\xe8V<\x94\x9dt/k\x80\xbe\xe8DSD\xd2\xeeK_\x8a+m\x03<\u0686\xc9O\xb9\xf5\xa7\xad\b\x0e\xa9O9\x8b\xea\xc6q\x8bU4\b\x95\xaaA\x98\a\xb4\xcf\xd7\x01\xfb\x97\"3\xe8\x1ew\x14\v\xa2\x01\xa2\x01\x01\x02X \x8a\x88\xe3\xddt\t\xf1\x95\xfdR\xdb-<\xba]r\xcag\t\xbf\x1d\x94\x12\x1b\xf3t\x88\x01\xb4\x0fo\\\x02X@\xe9\x1f\xa5f\x05\xf1\xa1\xcd[\u0167%A\u07f9#\x9f\xa5}V\x7f\xc8\x17D*c\xe0(0\xd5\x0er\x9e\xf4s\x83Qw\xa1,f`\x01l\x92\xaa\xfb\xd3h\x04\xc4\xc0\x81n\xd4}\xf6\x99|\xaf1\xef\x13\b",
		hash: "52a87b2054a4ba4c336d35f7ec9ef92026d17baee0b3db7d5614e01f711ac400.rec"},
	{name: "cred-revocation", dir: store.TrustDir,
		file: "sidera-record 1 cred-revocation c8b8eed7 257 ZGZjMDM3MzRkYzc4MjFkMWYxNjExNmVkZmY1NmRmYThhYTQxOWQzYzE5ZmE4NTBiZDcyMTU2ZjczYjBmNGE5YQ\n\xa3\x01x$sidera/trust-credential-revocation/1\x02Xi\xa1\x01Xe\xa5\x01X [\x82[\xcem\x88\xdd(j\xac\x8c\x8a\xbc\xc0\x80\xffb\x92\xaa\xf6\x10nP\x00\x17Z\xc7\b\xd9\xe6\xa8\xc0\x02X \xfd\x9c\xe51+\u02ec_\xa0\xad\x89\xa5%\xa0\x0f4\x941'\xbco\xdb\xdc\xfe\xcb|2\xb6\x04hP\x1f\x03\x1aj\xdcs\x90\x04\x1ak\x04\x00\x90\x05P\x01\x02\x01\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x03\x81\xa2\x01\xa2\x01\x01\x02X \x8a\x88\xe3\xddt\t\xf1\x95\xfdR\xdb-<\xba]r\xcag\t\xbf\x1d\x94\x12\x1b\xf3t\x88\x01\xb4\x0fo\\\x02X@#\x92az\xc0\x1f\xf4\x8ay(\xce\xd3\x06\xc6\b\xd8\xff,\xc1\xe9$\xe3KW\xbc\xcb\x0f\xa3I\xa1#k\xc1\xfe\x8f\x057jM4_\f\x93XP\xfdX\xbe\x9f\x19\x04}\xd1\x11\x12\u02d7\xa3\xdc^\xa2\xfa+\b",
		hash: "60a4cb114f41a5d7ef82b423ea2c35b5cae920ac88c44cd94cab0b7eee903e07.rec"},
	{name: "local-revoked", dir: store.TrustDir,
		file: "sidera-record 1 local-revoked 35bcbf44 134 bm9kZTFsa3d3MnZmdGV3azlsZzlkM3hqanRncTB4ajJyemZhdWRsZGFlbGt0MHNldHZwcmcycTBzaGFuN3p6\n{\"node\":\"node1lkww2vftewk9lg9d3xjjtgq0xj2rzfaudldaelkt0setvprg2q0shan7zz\",\"at\":\"2026-10-24T09:00:00Z\",\"operation\":\"trust.revoke-node\"}",
		hash: "cf46e0584399fb93c93122010020456c75d5c250a4c1084d99ceebce5ad7ae3c.rec"},
	{name: "peer", dir: store.PeersDir,
		file: "sidera-record 1 peer f30abb57 410 bm9kZTF0d3A5aG5uZDNyd2pzNjR2M2o5dGVzeXFsYTNmOTJoa3pwaDlxcXFodHRyczNrMHg0cnFxNDRhbjRh\n{\"cert\":\"owF4GXNpZGVyYS9uZXR3b3JrLWtleS1jZXJ0LzECWFmlAVggW4Jbzm2I3ShqrIyKvMCA_2KSqvYQblAAF1rHCNnmqMACogEBAlggNLTZBDFWy23PC-sKKUm3VZyUDSvLbb6MU6mzAnjjp0YDAQQaatxzGAUaawQAkAOBogGiAQECWCCBOXcOqH0XX1ajVGbDTH7My42KkbTuN6Jd9g9bj8mzlAJYQNqQquyVyVbe4i1w_2AC49doO2g3Da-xkE9mG9Ax_5KTyLDEi8bsDz3ZjYEXr5RoGCP82HVc8ATTuFHu0fCwSwk\",\"serial_seen\":1,\"digest\":\"e3d1b9e0f9163bcb776ed28f84e2cf4223f1c1352ce31a270ccc9205fa9404e8\"}",
		hash: "378e97949eeae96466d1d504ccf72fb4b136b40855a52582d670cf700a2bf524.rec"},
}
