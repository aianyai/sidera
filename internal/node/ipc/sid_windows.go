package ipc

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// currentUserSID is the TokenUser of this process. It is the user, never the
// Administrators group, even when the process runs elevated.
func currentUserSID() (*windows.SID, error) {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return nil, fmt.Errorf("open the process token: %w", err)
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("read TokenUser of the process token: %w", err)
	}
	sid, err := user.User.Sid.Copy()
	if err != nil {
		return nil, fmt.Errorf("copy the user SID: %w", err)
	}
	return sid, nil
}
