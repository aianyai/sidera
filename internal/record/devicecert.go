package record

import (
	"fmt"
	"time"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/timing"
)

// DeviceCertificateType is the type string of a device certificate. A
// temporary sub-key is the same record with a short validity.
const DeviceCertificateType = "sidera/device-cert/1"

// DeviceCertificate lets a device key act for a person, up to a level, for a
// time. The person's root key signs it. A certificate always states its cap:
// "no cap" is written as full control.
type DeviceCertificate struct {
	// Person is the person the certificate is for. The signer must be the root
	// key that derives this identifier.
	Person ident.PersonID
	// DeviceKey is the key the person's device signs requests with.
	DeviceKey ident.PublicKey
	// MaxLevel is the highest level the certificate allows: view, manage or full
	// control. It is never LevelNone.
	MaxLevel admin.Level
	// NotBefore and NotAfter are the validity window. Record does not judge
	// them: administration judges them, sometimes at the signing time of an
	// offline directive rather than now.
	NotBefore, NotAfter time.Time
}

// IssueDeviceCertificate builds the certificate a person's root key issues at
// issuedAt for a device key. NotBefore is issuedAt minus the clock tolerance,
// not issuedAt itself, for the reason given at IssueNetworkKeyCertificate:
// validity is compared without tolerance, so a verifier whose clock is behind
// by up to the tolerance would otherwise refuse a certificate issued moments
// ago as not yet valid. The result is sealed with Seal, which refuses invalid
// values.
func IssueDeviceCertificate(person ident.PersonID, deviceKey ident.PublicKey, maxLevel admin.Level, issuedAt, notAfter time.Time) DeviceCertificate {
	return DeviceCertificate{
		Person:    person,
		DeviceKey: deviceKey,
		MaxLevel:  maxLevel,
		NotBefore: wholeSeconds(issuedAt).Add(-timing.ClockTolerance),
		NotAfter:  wholeSeconds(notAfter),
	}
}

type deviceCertificateWire struct {
	Person    []byte  `cbor:"1,keyasint"`
	DeviceKey KeyWire `cbor:"2,keyasint"`
	MaxLevel  uint64  `cbor:"3,keyasint"`
	NotBefore int64   `cbor:"4,keyasint"`
	NotAfter  int64   `cbor:"5,keyasint"`
}

// DeviceCertificateDescriptor describes the device certificate. It is signed by
// exactly one key: the root key of the person it names (the chain check).
var DeviceCertificateDescriptor = NewDescriptor(DeviceCertificateType,
	func(c DeviceCertificate) (deviceCertificateWire, error) {
		level, err := LevelToWire(c.MaxLevel)
		if err != nil {
			return deviceCertificateWire{}, err
		}
		nb, err := TimeToWire(c.NotBefore)
		if err != nil {
			return deviceCertificateWire{}, fmt.Errorf("not_before: %w", err)
		}
		na, err := TimeToWire(c.NotAfter)
		if err != nil {
			return deviceCertificateWire{}, fmt.Errorf("not_after: %w", err)
		}
		return deviceCertificateWire{Person: c.Person.Bytes(), DeviceKey: KeyToWire(c.DeviceKey), MaxLevel: level, NotBefore: nb, NotAfter: na}, nil
	},
	func(w deviceCertificateWire) (DeviceCertificate, error) {
		person, err := ident.PersonIDFromBytes(w.Person)
		if err != nil {
			return DeviceCertificate{}, fmt.Errorf("person: %w", err)
		}
		key, err := KeyFromWire(w.DeviceKey)
		if err != nil {
			return DeviceCertificate{}, fmt.Errorf("device key: %w", err)
		}
		level, err := LevelFromWire(w.MaxLevel)
		if err != nil {
			return DeviceCertificate{}, err
		}
		if err := checkDeviceCertificate(person, key, w.NotBefore, w.NotAfter); err != nil {
			return DeviceCertificate{}, err
		}
		nb, err := TimeFromWire(w.NotBefore)
		if err != nil {
			return DeviceCertificate{}, fmt.Errorf("not_before: %w", err)
		}
		na, err := TimeFromWire(w.NotAfter)
		if err != nil {
			return DeviceCertificate{}, fmt.Errorf("not_after: %w", err)
		}
		return DeviceCertificate{Person: person, DeviceKey: key, MaxLevel: level, NotBefore: nb, NotAfter: na}, nil
	},
	func(c DeviceCertificate) []SignerSlot { return []SignerSlot{PersonRootKey(c.Person)} },
)

// checkDeviceCertificate holds the rules of the certificate: the fields are set,
// the window is not empty, and the device key is not the person's root key (a
// device holding the root key could certify further devices and its loss would
// be the loss of the person). Only the decoding
// side calls it; encoding reaches it through the descriptor, which decodes what
// it wrote.
func checkDeviceCertificate(person ident.PersonID, key ident.PublicKey, notBefore, notAfter int64) error {
	if person.IsZero() {
		return fmt.Errorf("person: %w", ident.ErrZero)
	}
	if key.IsZero() {
		return fmt.Errorf("device key: %w", ErrKey)
	}
	if notAfter <= notBefore {
		return fmt.Errorf("%w: not_after must be after not_before", ErrTime)
	}
	if derived, err := ident.PersonIDFromKey(key); err == nil && derived == person {
		return fmt.Errorf("%w: the device key is the root key of the person", ErrRoleSeparation)
	}
	return nil
}

// AdminCert returns the signature-derived facts that administration reads: the
// cap and the validity window, taken as written. Validity is not judged here;
// administration judges it. Both revocation flags are false: the revocation
// sets fill them in before the value reaches administration, so a certificate
// that has not been checked against them must not be handed on.
func (c DeviceCertificate) AdminCert() admin.DeviceCert {
	return admin.DeviceCert{MaxLevel: c.MaxLevel, NotBefore: c.NotBefore, NotAfter: c.NotAfter}
}
