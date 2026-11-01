package config

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

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

// currentWire is the current pointer of an adapter. digest and revision are
// absent in a pointer written before a package was told apart by its contents:
// digest is then unknown and revision is 0.
type currentWire struct {
	Format   int     `json:"format"`
	Version  string  `json:"version"`
	Digest   *string `json:"digest,omitempty"`
	Revision *uint32 `json:"revision,omitempty"`
}

// digestHexLen is the length of a package digest in hex: SHA-256.
const digestHexLen = 64

func decodeAdapterCurrent(data []byte) (AdapterCurrent, error) {
	var w currentWire
	if err := decodeStrict(data, &w); err != nil {
		return AdapterCurrent{}, err
	}
	if err := checkHeader(w.Format, 0, false); err != nil {
		return AdapterCurrent{}, err
	}
	if err := adapterproto.ValidateSemVer(w.Version); err != nil {
		return AdapterCurrent{}, invalid("version", err)
	}
	out := AdapterCurrent{Version: w.Version}
	if w.Digest != nil {
		if *w.Digest == "" {
			return AdapterCurrent{}, invalid("digest", errors.New("an empty digest is not a value: leave it out"))
		}
		if err := checkDigest(*w.Digest); err != nil {
			return AdapterCurrent{}, invalid("digest", err)
		}
		out.Digest = *w.Digest
	}
	if w.Revision != nil {
		if *w.Revision == 0 {
			return AdapterCurrent{}, invalid("revision", errors.New("revision 0 is not a value: leave it out"))
		}
		out.Revision = *w.Revision
	}
	return out, nil
}

// checkDigest accepts no digest (unknown) or one in lower case hex.
func checkDigest(d string) error {
	if d == "" {
		return nil
	}
	if len(d) != digestHexLen || strings.ToLower(d) != d {
		return fmt.Errorf("a package digest is %d characters of lower case hex", digestHexLen)
	}
	if _, err := hex.DecodeString(d); err != nil {
		return fmt.Errorf("a package digest is %d characters of lower case hex", digestHexLen)
	}
	return nil
}

// DecodeAdapterCurrent decodes the current pointer of an adapter. The error is a
// *FileError that names the file.
func DecodeAdapterCurrent(id string, data []byte) (AdapterCurrent, error) {
	v, err := decodeAdapterCurrent(data)
	if err != nil {
		return AdapterCurrent{}, fileErrorFor(AdapterCurrentPath(id), KindAdapterCurrent, err)
	}
	return v, nil
}

// EncodeAdapterCurrent returns the bytes of a current pointer.
func EncodeAdapterCurrent(c AdapterCurrent) ([]byte, error) {
	if err := adapterproto.ValidateSemVer(c.Version); err != nil {
		return nil, err
	}
	if err := checkDigest(c.Digest); err != nil {
		return nil, err
	}
	w := currentWire{Format: FormatVersion, Version: c.Version}
	if c.Digest != "" {
		w.Digest = &c.Digest
	}
	if c.Revision != 0 {
		w.Revision = &c.Revision
	}
	return encodeJSON(w)
}
