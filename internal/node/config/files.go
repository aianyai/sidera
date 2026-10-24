package config

import (
	"encoding/hex"
	"fmt"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/internal/revocation"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// endpointIDsWire is the endpoint-id file of one instance.
type endpointIDsWire struct {
	Format  int               `json:"format"`
	Runtime string            `json:"runtime,omitempty"`
	Direct  map[string]string `json:"direct,omitempty"`
}

func decodeEndpointIDs(data []byte) (EndpointIDs, error) {
	var w endpointIDsWire
	if err := decodeStrict(data, &w); err != nil {
		return EndpointIDs{}, err
	}
	if err := checkHeader(w.Format, 0, false); err != nil {
		return EndpointIDs{}, err
	}
	var out EndpointIDs
	seen := map[ident.EndpointID]string{}
	if w.Runtime != "" {
		id, err := parseEndpoint("runtime", w.Runtime)
		if err != nil {
			return EndpointIDs{}, err
		}
		out.Runtime = id
		seen[id] = "runtime"
	}
	if w.Direct != nil {
		out.Direct = make(map[string]ident.EndpointID, len(w.Direct))
		for _, key := range sortedKeys(w.Direct) {
			field := "direct." + key
			if err := checkKey(field, key); err != nil {
				return EndpointIDs{}, err
			}
			id, err := parseEndpoint(field, w.Direct[key])
			if err != nil {
				return EndpointIDs{}, err
			}
			if first, dup := seen[id]; dup {
				return EndpointIDs{}, invalidf(field, "the id %s is already the id of %s", id, first)
			}
			seen[id] = field
			out.Direct[key] = id
		}
	}
	return out, nil
}

// DecodeEndpointIDs decodes the endpoint-id file of an instance. The error is a
// *FileError that names the file.
func DecodeEndpointIDs(instance string, data []byte) (EndpointIDs, error) {
	e, err := decodeEndpointIDs(data)
	if err != nil {
		return EndpointIDs{}, fileErrorFor(EndpointIDsPath(instance), KindEndpointIDs, err)
	}
	return e, nil
}

// Encode returns the bytes of an endpoint-id file.
func (e EndpointIDs) Encode() ([]byte, error) {
	w := endpointIDsWire{Format: FormatVersion}
	if !e.Runtime.IsZero() {
		w.Runtime = e.Runtime.String()
	}
	if len(e.Direct) != 0 {
		w.Direct = make(map[string]string, len(e.Direct))
		for key, id := range e.Direct {
			w.Direct[key] = id.String()
		}
	}
	return encodeJSON(w)
}

func cloneEndpointIDs(e EndpointIDs) EndpointIDs {
	if e.Direct != nil {
		d := make(map[string]ident.EndpointID, len(e.Direct))
		for k, v := range e.Direct {
			d[k] = v
		}
		e.Direct = d
	}
	return e
}

// every returns the ids of the set.
func (e EndpointIDs) every() map[ident.EndpointID]string {
	out := map[ident.EndpointID]string{}
	if !e.Runtime.IsZero() {
		out[e.Runtime] = "runtime"
	}
	for k, id := range e.Direct {
		out[id] = "direct." + k
	}
	return out
}

// backupWire is revocation-backup.json.
type backupWire struct {
	Format      int    `json:"format"`
	ConfirmedAt string `json:"confirmed_at"`
	Digest      string `json:"digest"`
}

func decodeBackup(data []byte) (Backup, error) {
	var w backupWire
	if err := decodeStrict(data, &w); err != nil {
		return Backup{}, err
	}
	if err := checkHeader(w.Format, 0, false); err != nil {
		return Backup{}, err
	}
	at, err := parseTime(w.ConfirmedAt)
	if err != nil {
		return Backup{}, invalid("confirmed_at", err)
	}
	if at.IsZero() {
		return Backup{}, invalidf("confirmed_at", "a confirmation has a time")
	}
	raw, err := decodeHex(w.Digest, len(record.Digest{}))
	if err != nil {
		return Backup{}, invalid("digest", err)
	}
	var d record.Digest
	copy(d[:], raw)
	if d.IsZero() {
		return Backup{}, invalidf("digest", "the digest of a record is never zero")
	}
	return Backup{ConfirmedAt: at, Digest: d}, nil
}

// DecodeBackup decodes revocation-backup.json. The error is a *FileError that
// names the file.
func DecodeBackup(data []byte) (Backup, error) {
	b, err := decodeBackup(data)
	if err != nil {
		return Backup{}, fileErrorFor(BackupPath, KindBackup, err)
	}
	return b, nil
}

// Encode returns the bytes of revocation-backup.json.
func (b Backup) Encode() ([]byte, error) {
	if b.ConfirmedAt.IsZero() || b.Digest.IsZero() {
		return nil, fmt.Errorf("a confirmation has a time and a digest")
	}
	return encodeJSON(backupWire{
		Format:      FormatVersion,
		ConfirmedAt: formatTime(b.ConfirmedAt),
		Digest:      hex.EncodeToString(b.Digest[:]),
	})
}

// DecodeRevocation decodes the file of a revocation record, verifies it with the
// rules of the revocation package, and checks that the file is named after the
// record's digest. name is the name of the file in config/revocations. The
// error is a *FileError that names the file.
func DecodeRevocation(name string, data []byte) (revocation.Revocation, record.Record, error) {
	path := RevocationsDir + "/" + name
	rec, err := record.Decode(data, record.DefaultMaxInput)
	if err != nil {
		return revocation.Revocation{}, record.Record{}, &FileError{Path: path, Kind: KindRevocation, Reason: FileMalformed, Err: err}
	}
	rev, err := revocation.Verify(rec)
	if err != nil {
		return revocation.Revocation{}, record.Record{}, &FileError{Path: path, Kind: KindRevocation, Reason: FileInvalid, Err: err}
	}
	if want := rec.Digest().String() + ".rec"; name != want {
		return revocation.Revocation{}, record.Record{}, &FileError{Path: path, Kind: KindRevocation, Reason: FileMisnamed,
			Err: fmt.Errorf("the record's digest names the file %s", want)}
	}
	return rev, rec, nil
}

// currentWire is the current pointer of an adapter.
type currentWire struct {
	Format  int    `json:"format"`
	Version string `json:"version"`
}

func decodeAdapterCurrent(data []byte) (string, error) {
	var w currentWire
	if err := decodeStrict(data, &w); err != nil {
		return "", err
	}
	if err := checkHeader(w.Format, 0, false); err != nil {
		return "", err
	}
	if err := adapterproto.ValidateSemVer(w.Version); err != nil {
		return "", invalid("version", err)
	}
	return w.Version, nil
}

// DecodeAdapterCurrent decodes the current pointer of an adapter and returns the
// version it names. The error is a *FileError that names the file.
func DecodeAdapterCurrent(id string, data []byte) (string, error) {
	v, err := decodeAdapterCurrent(data)
	if err != nil {
		return "", fileErrorFor(AdapterCurrentPath(id), KindAdapterCurrent, err)
	}
	return v, nil
}

// EncodeAdapterCurrent returns the bytes of a current pointer.
func EncodeAdapterCurrent(version string) ([]byte, error) {
	if err := adapterproto.ValidateSemVer(version); err != nil {
		return nil, err
	}
	return encodeJSON(currentWire{Format: FormatVersion, Version: version})
}
