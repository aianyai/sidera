package store_test

import "github.com/aianyai/sidera/internal/node/store"

// goldenRecords are complete record files of every kind, written out by hand (the checksums and names
// were computed with a separate implementation of CRC32C and SHA-256): the
// body is the canonical JSON the format defines, the first line carries its
// CRC32C, its length and the key, and the file name is the hash of kind and key.
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
		file: "sidera-record 1 attempts 4220f629 391 ZXAxejUyMzI5ZzR6NTIzMjlnNHo1MjMyOWc0ejVrOHEyZzQ\n{\"attempts\":[{\"at\":\"2026-10-24T08:50:00Z\",\"outcome\":\"delivered\",\"kind\":\"first\"}],\"blocked\":{\"cause\":\"adapter_offline\",\"since\":\"2026-10-24T08:55:00Z\"},\"pending\":{\"event\":\"notified\",\"items\":[{\"key\":\"node1qgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpq7haevd ep1zvf3xycnzvf3xycnzvf3xycnzvl5u4cd msg-0001 ep1zgfpyysjzgfpyysjzgfpyysjzgtln4pc\",\"rev\":3,\"accepted_at\":\"2026-10-24T09:00:00Z\"}]}}",
		hash: "3a94785304195d38d01dcf3be2ea7924396996b0008b5ef1080d044a90400617.rec"},
	{name: "endpoint", dir: store.EndpointsDir,
		file: "sidera-record 1 endpoint 14d0a10f 254 ZXAxejUyMzI5ZzR6NTIzMjlnNHo1MjMyOWc0ejVrOHEyZzQ\n{\"kind\":\"session\",\"instance\":\"claude\",\"status\":\"creating\",\"token\":\"RUVFRUVFRUVFRUVFRUVFRUVFRUVFRUVFRUVFRUVFRUU\",\"session\":{\"preset\":\"default\",\"tool_access\":[\"SIDERA_TOKEN\"]},\"created_by\":\"b5c8d7b80655e880380440180e306644b581f191eb47622b58ece7c2bac4748d\"}",
		hash: "c2ccbb5272953d655682b33fc9219b88055463bccb316d5d1750793bbe10e030.rec"},
	{name: "in", dir: store.InDir,
		file: "sidera-record 1 in a55716ae 553 bm9kZTFxZ3BxeXFzenFncHF5cXN6cWdwcXlxc3pxZ3BxeXFzenFncHF5cXN6cWdwcXlxc3pxZ3BxN2hhZXZkIGVwMXp2ZjN4eWNuenZmM3h5Y256dmYzeHljbnp2bDV1NGNkIG1zZy0wMDAxIGVwMXpnZnB5eXNqemdmcHl5c2p6Z2ZweXlzanpndGxuNHBj\n{\"rev\":1,\"record\":{\"side\":\"target\",\"state\":\"accepted\",\"accept_before\":\"2026-10-24T09:01:00Z\",\"report_by\":\"2026-10-24T12:03:00Z\",\"backlog_deadline\":\"2026-10-24T09:30:00Z\"},\"dedup\":{\"digest\":\"0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20\",\"retain_until\":\"2026-10-24T12:03:00Z\"},\"auth\":{\"kind\":\"allow\"},\"reply\":{\"reason\":\"disabled\"},\"handout\":{\"content_type\":\"text/plain\",\"sent_at\":\"2026-10-24T09:00:00Z\"},\"accepted_at\":\"2026-10-24T09:00:00Z\",\"body\":{\"sha256\":\"ea8c004183bd3bd33e27907ed5cb35a24808cb99ab2bb41a727d0c675f5bcdfc\",\"len\":17}}",
		hash: "9217a562696c0e2029e1e14cca852cea2de0be95435a39f5921d9b797081f128.rec"},
	{name: "out", dir: store.OutDir,
		file: "sidera-record 1 out 0d261117 775 ZXAxenlnM3p5ZzN6eWczenlnM3p5ZzN6eWczenlqMGtjZ3EgbXNnLTAwMDEgbm9kZTFxZ3BxeXFzenFncHF5cXN6cWdwcXlxc3pxZ3BxeXFzenFncHF5cXN6cWdwcXlxc3pxZ3BxN2hhZXZkL2VwMXpnZnB5eXNqemdmcHl5c2p6Z2ZweXlzanpndGxuNHBj\n{\"kind\":\"message\",\"body_non_empty\":true,\"accept_before\":\"2026-10-24T09:01:00Z\",\"recipients\":[{\"address\":\"node1qgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpq7haevd/ep1zgfpyysjzgfpyysjzgfpyysjzgtln4pc\",\"delivery\":{\"side\":\"source\",\"state\":\"queued\",\"accept_before\":\"2026-10-24T09:01:00Z\",\"report_by\":\"2026-10-24T12:03:00Z\"}}],\"to\":[\"node1qgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpq7haevd/ep1zgfpyysjzgfpyysjzgfpyysjzgtln4pc\"],\"seq\":[{\"endpoint\":\"ep1zgfpyysjzgfpyysjzgfpyysjzgtln4pc\",\"seq\":1}],\"sent_at\":\"2026-10-24T09:00:00Z\",\"report_by\":\"2026-10-24T12:03:00Z\",\"content_type\":\"text/plain\",\"body\":{\"sha256\":\"ea8c004183bd3bd33e27907ed5cb35a24808cb99ab2bb41a727d0c675f5bcdfc\",\"len\":17},\"body_digest\":\"0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20\"}",
		hash: "32bb53b3134a3e55c25a074dd89be386eab83340dfc46b65e4478d271eeb9db0.rec"},
	{name: "ctl", dir: store.CtlDir,
		file: "sidera-record 1 ctl 38e399f5 566 bm9kZTFxZ3BxeXFzenFncHF5cXN6cWdwcXlxc3pxZ3BxeXFzenFncHF5cXN6cWdwcXlxc3pxZ3BxN2hhZXZkIGVwMXp2ZjN4eWNuenZmM3h5Y256dmYzeHljbnp2bDV1NGNkIG1zZy0wMDAxIGVwMXpzMnBnOXE1enMycGc5cTV6czJwZzlxNXpzenYwMjNx\n{\"record\":{\"side\":\"target\",\"state\":\"accepted\",\"control\":true,\"accept_before\":\"2026-10-24T09:01:00Z\",\"report_by\":\"2026-10-24T12:03:00Z\"},\"dedup\":{\"digest\":\"0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20\",\"retain_until\":\"2026-10-24T12:03:00Z\"},\"kind\":\"control.session.create\",\"preset\":\"default\",\"session_endpoint\":\"ep1z52329g4z52329g4z52329g4z5k8q2g4\",\"accepted_at\":\"2026-10-24T09:00:00Z\",\"sent_at\":\"2026-10-24T09:00:00Z\",\"intent\":{\"attempt\":\"0b4d6e1a-2f3c-4d5e-8f60-718293a4b5c6\",\"endpoint\":\"ep1z52329g4z52329g4z52329g4z5k8q2g4\",\"state\":\"creating\"}}",
		hash: "b5c8d7b80655e880380440180e306644b581f191eb47622b58ece7c2bac4748d.rec"},
}
