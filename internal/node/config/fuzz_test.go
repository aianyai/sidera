package config_test

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/node/config"
)

// The decoders read files an owner may edit and, for the patches and the preset,
// documents that arrive over the management interface. For input of any shape
// each returns a value or a typed error and never panics; and what decodes
// encodes to a canonical text that decodes to the same value and encodes to the
// same text again.

func isTypedError(err error) bool {
	var fe *config.FileError
	var fl *config.FieldError
	return errors.As(err, &fe) || errors.As(err, &fl)
}

// roundTrip checks the property of a decoder on one input, given its decode and
// the encode of its value.
func roundTrip[T any](t *testing.T, data []byte, decode func([]byte) (T, error), encode func(T) ([]byte, error)) {
	t.Helper()
	v1, err := decode(data)
	if err != nil {
		if !isTypedError(err) {
			t.Fatalf("an untyped error for %q: %v", data, err)
		}
		return
	}
	b1, err := encode(v1)
	if err != nil {
		t.Fatalf("a value that decoded does not encode: %v\ninput %q", err, data)
	}
	v2, err := decode(b1)
	if err != nil {
		t.Fatalf("the canonical text does not decode: %v\ninput %q\ntext %s", err, data, b1)
	}
	if !reflect.DeepEqual(v1, v2) {
		t.Fatalf("the canonical text decodes to another value:\n%+v\n%+v\ninput %q", v1, v2, data)
	}
	b2, err := encode(v2)
	if err != nil || !bytes.Equal(b1, b2) {
		t.Fatalf("the encoding is not canonical: %v\n%s\n%s", err, b1, b2)
	}
}

func seeds(f *testing.F, docs ...string) {
	for _, d := range docs {
		f.Add([]byte(d))
	}
	f.Add([]byte(``))
	f.Add([]byte(`{}`))
	f.Add([]byte(`null`))
	f.Add([]byte(`[]`))
	f.Add([]byte("{\"format\":1,\"version\":1"))
}

func FuzzDecodeNode(f *testing.F) {
	seeds(f, goldenNode, initNode, strings.Replace(goldenNode, `"person": "local"`, `"person": "`+personText+`"`, 1))
	f.Fuzz(func(t *testing.T, data []byte) {
		roundTrip(t, data, config.DecodeNode, config.Node.Encode)
	})
}

func FuzzDecodePolicy(f *testing.F) {
	seeds(f, goldenPolicy, `{"format":1,"version":3,"rules":[]}`)
	f.Fuzz(func(t *testing.T, data []byte) {
		roundTrip(t, data, config.DecodePolicy, config.PolicyFile.Encode)
	})
}

func FuzzDecodePresets(f *testing.F) {
	seeds(f, goldenPresets, initPresets, `{"format":1,"version":7,"presets":{"mine":{"local_sources":"deny"}}}`)
	f.Fuzz(func(t *testing.T, data []byte) {
		roundTrip(t, data, config.DecodePresets, config.Presets.Encode)
	})
}

func FuzzDecodePreset(f *testing.F) {
	seeds(f, `{"local_sources":"allow"}`, `{"local_sources":"deny","description":"d","config":{"example":{"k":1}}}`)
	f.Fuzz(func(t *testing.T, data []byte) {
		p, err := config.DecodePreset(data)
		if err != nil {
			if !isTypedError(err) {
				t.Fatalf("an untyped error: %v", err)
			}
			if p.LocalSources != 0 {
				t.Fatalf("a refused preset has a statement: %v", p.LocalSources)
			}
			return
		}
		if p.LocalSources == 0 {
			t.Fatalf("a preset without a statement decoded: %q", data)
		}
	})
}

func FuzzDecodeInstances(f *testing.F) {
	seeds(f, goldenInstances, initInstances)
	f.Fuzz(func(t *testing.T, data []byte) {
		roundTrip(t, data, config.DecodeInstances, config.Instances.Encode)
	})
}

func FuzzDecodeEndpointIDs(f *testing.F) {
	seeds(f, goldenEndpointIDs, `{"format":1,"direct":{"a":"`+ep2Text+`"}}`)
	f.Fuzz(func(t *testing.T, data []byte) {
		roundTrip(t, data, func(b []byte) (config.EndpointIDs, error) { return config.DecodeEndpointIDs("example", b) }, config.EndpointIDs.Encode)
	})
}

func FuzzDecodeBackup(f *testing.F) {
	_, digest, _ := selfRevocation(f, 7)
	seeds(f, strings.Replace(goldenBackup, "%s", digest.String(), 1))
	f.Fuzz(func(t *testing.T, data []byte) {
		roundTrip(t, data, config.DecodeBackup, config.Backup.Encode)
	})
}

func FuzzDecodeAdapterCurrent(f *testing.F) {
	seeds(f, goldenCurrent, `{"format":1,"version":"1.2.3-rc.1+build.5"}`)
	f.Fuzz(func(t *testing.T, data []byte) {
		v, err := config.DecodeAdapterCurrent("example", data)
		if err != nil {
			if !isTypedError(err) {
				t.Fatalf("an untyped error: %v", err)
			}
			return
		}
		enc, err := config.EncodeAdapterCurrent(v)
		if err != nil {
			t.Fatalf("a version that decoded does not encode: %v", err)
		}
		v2, err := config.DecodeAdapterCurrent("example", enc)
		if err != nil || v2 != v {
			t.Fatalf("the canonical text decodes to %q, %v, want %q", v2, err, v)
		}
	})
}

func FuzzDecodeRevocation(f *testing.F) {
	data, digest, _ := selfRevocation(f, 7)
	f.Add(digest.String()+".rec", data)
	f.Add("x.rec", data)
	f.Add("", []byte{})
	f.Add(digest.String()+".rec", data[:len(data)/2])
	f.Fuzz(func(t *testing.T, name string, data []byte) {
		rev, rec, err := config.DecodeRevocation(name, data)
		if err != nil {
			if !isTypedError(err) {
				t.Fatalf("an untyped error: %v", err)
			}
			return
		}
		if name != rec.Digest().String()+".rec" || rev.Digest() != rec.Digest() {
			t.Fatalf("a revocation under the name %q has the digest %v", name, rec.Digest())
		}
		again, err := rec.Encode()
		if err != nil || !bytes.Equal(again, data) {
			t.Fatalf("the record does not encode to the bytes it was read from: %v", err)
		}
	})
}

func FuzzDecodeNodePatch(f *testing.F) {
	seeds(f, `{"reply_permission":false}`, `{"node_queue_limit":20000,"endpoint_defaults":{"backlog_limit":9}}`)
	f.Fuzz(func(t *testing.T, data []byte) {
		if _, err := config.DecodeNodePatch(data); err != nil && !isTypedError(err) {
			t.Fatalf("an untyped error: %v", err)
		}
	})
}

func FuzzDecodeEndpointPatch(f *testing.F) {
	seeds(f, `{"max_queue_s":60}`, `{"backlog_limit":5,"max_queue_s":7200}`)
	f.Fuzz(func(t *testing.T, data []byte) {
		if _, err := config.DecodeEndpointPatch(data); err != nil && !isTypedError(err) {
			t.Fatalf("an untyped error: %v", err)
		}
	})
}
