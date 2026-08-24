//go:build windows

package paths

import "golang.org/x/sys/windows"

// systemDownloadsDir asks Windows for the user's actual Downloads
// known-folder path (SHGetKnownFolderPath under the hood) rather than
// assuming %USERPROFILE%\Downloads — a user can relocate that folder
// to another drive entirely via Explorer's Properties dialog, and
// %USERPROFILE%\Downloads then isn't it. home is unused on this
// platform; kept so the signature matches every OS's implementation.
func systemDownloadsDir(_ string) string {
	p, err := windows.KnownFolderPath(windows.FOLDERID_Downloads, 0)
	if err != nil {
		return ""
	}
	return p
}
