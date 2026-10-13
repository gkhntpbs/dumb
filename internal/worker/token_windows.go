//go:build windows

package worker

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows mode bits do not describe access. Conservatively reject grants to
// anyone except the process user and the trusted operating-system administrators.
func checkTokenPermissions(path string, _ os.FileInfo) error {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("worker: token ACL: %w", err)
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil {
		return fmt.Errorf("worker: token file has no readable owner ACL")
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil {
		return fmt.Errorf("worker: token file must have a restricted DACL")
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return err
	}
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return err
	}
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return err
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return err
	}
	trusted := func(sid *windows.SID) bool {
		return sid.Equals(user.User.Sid) || sid.Equals(system) || sid.Equals(admins)
	}
	if !trusted(owner) {
		return fmt.Errorf("worker: token owner is not the process user, SYSTEM or Administrators")
	}
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil {
			return err
		}
		if ace.Header.AceFlags&0x08 != 0 {
			continue
		} // INHERIT_ONLY_ACE does not apply to this file.
		if ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
			continue
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return fmt.Errorf("worker: unsupported token ACL entry; restrict access to the process user, SYSTEM and Administrators")
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if ace.Mask&(windows.FILE_READ_DATA|windows.FILE_WRITE_DATA|windows.FILE_APPEND_DATA|windows.WRITE_DAC|windows.WRITE_OWNER|windows.GENERIC_READ|windows.GENERIC_WRITE|windows.GENERIC_ALL) != 0 && !trusted(sid) {
			return fmt.Errorf("worker: token file is accessible by %s; restrict its ACL to the process user, SYSTEM and Administrators", sid.String())
		}
	}
	return nil
}
