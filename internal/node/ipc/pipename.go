package ipc

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
)

// The functions in this file decide what a Windows pipe is called and who may
// open it. They are pure and live outside the Windows files so that they are
// tested on every platform; only reading the user's SID and the pipe calls
// themselves are Windows-only.

// pipeHash is the 16 hex characters that tell the pipes of different data
// directories and different users apart: the start of
// SHA-256(user SID string, 0x00, canonical data directory). The pipe namespace
// is machine-wide, so without the SID two users who chose the same directory
// string would fight over one name. The SID is not a secret and adds no
// security; it only removes that accidental clash.
func pipeHash(userSID, canonicalHome string) string {
	sum := sha256.Sum256([]byte(userSID + "\x00" + canonicalHome))
	return hex.EncodeToString(sum[:])[:16]
}

// canonicalPipeHome is the form of the data directory that goes into the hash:
// absolute, clean and lower case, because NTFS paths are case-insensitive and
// every spelling a user can type must reach the same pipe. Two spellings that
// reach one directory by other means (a junction) yield two pipe names; the
// client then cannot connect, which is visible and easy to fix.
func canonicalPipeHome(home string) (string, error) {
	abs, err := absoluteHome(home)
	if err != nil {
		return "", err
	}
	return strings.ToLower(filepath.Clean(abs)), nil
}

func pipeName(hash string, kind Kind) string {
	return fmt.Sprintf(`\\.\pipe\sidera-%s-%s`, hash, kind)
}

// pipeSDDL grants the user, and nobody else, full access to the pipe. The owner
// is written out instead of using the owner-rights shorthand: a process started
// elevated may have the Administrators group as the default owner in its
// token, and "owner" would then mean the group. D:P makes the list protected
// from inheritance; SYSTEM and Administrators are deliberately absent because
// the daemon serves only its own user.
func pipeSDDL(userSID string) string {
	return fmt.Sprintf("O:%sD:P(A;;GA;;;%s)", userSID, userSID)
}
