//go:build windows

package provider

import (
	"syscall"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"
)

// On Windows a spawned CLI (especially one reached through a .cmd/.ps1 shim, or
// cmd.exe/PowerShell itself) writes localized error text on stderr in the console
// / ANSI code page (e.g. cp936/GBK on a zh-CN box), NOT UTF-8. If those raw bytes
// reach json.Encode they become U+FFFD ("�"), hiding the real message. toUTF8
// transcodes such bytes to UTF-8 so the error stays readable.
var (
	kernel32                = syscall.NewLazyDLL("kernel32.dll")
	procMultiByteToWideChar = kernel32.NewProc("MultiByteToWideChar")
	procGetOEMCP            = kernel32.NewProc("GetOEMCP")
	procGetACP              = kernel32.NewProc("GetACP")
)

// toUTF8 returns b unchanged when it is already valid UTF-8; otherwise it decodes
// b from the system OEM code page (falling back to the ANSI code page) into UTF-8.
func toUTF8(b []byte) string {
	if len(b) == 0 || utf8.Valid(b) {
		return string(b)
	}
	cp := legacyCP()
	// First call (cchWideChar = 0) returns the required wide-char count.
	n, _, _ := procMultiByteToWideChar.Call(
		uintptr(cp), 0, uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)), 0, 0,
	)
	if n == 0 {
		return string(b) // undecodable — keep raw rather than lose it
	}
	u16 := make([]uint16, n)
	r, _, _ := procMultiByteToWideChar.Call(
		uintptr(cp), 0, uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)),
		uintptr(unsafe.Pointer(&u16[0])), uintptr(n),
	)
	if r == 0 {
		return string(b)
	}
	return string(utf16.Decode(u16[:r]))
}

// legacyCP is the child's likely stderr encoding: the system OEM code page (what
// cmd.exe / native console programs emit by default, e.g. 936 on zh-CN), falling
// back to the ANSI code page. Deliberately NOT the process console output CP —
// that can be UTF-8 (65001) even while a spawned shim still writes OEM bytes.
func legacyCP() uint32 {
	if r, _, _ := procGetOEMCP.Call(); r != 0 {
		return uint32(r)
	}
	r, _, _ := procGetACP.Call()
	return uint32(r)
}
