//go:build windows

package worker

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsTokenACL(t *testing.T) {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		t.Fatal(err)
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("test-token"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, extra string
		allowed     bool
	}{
		{"owner", "", true},
		{"system-admin", "(A;;FA;;;SY)(A;;FA;;;BA)", true},
		{"everyone", "(A;;FR;;;WD)", false},
		{"authenticated-users", "(A;;FR;;;AU)", false},
		{"everyone-write", "(A;;FW;;;WD)", false},
		{"everyone-change-acl", "(A;;WD;;;WD)", false},
		{"everyone-change-owner", "(A;;WO;;;WD)", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + user.User.Sid.String() + ")" + tc.extra)
			if err != nil {
				t.Fatal(err)
			}
			acl, _, err := sd.DACL()
			if err != nil {
				t.Fatal(err)
			}
			if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
				t.Fatal(err)
			}
			_, err = ReadTokenFile(path)
			if (err == nil) != tc.allowed {
				t.Fatalf("allowed=%v error=%v", tc.allowed, err)
			}
		})
	}
}
