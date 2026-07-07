//go:build windows

package provider

import "testing"

// Valid UTF-8 must pass through untouched — we must never corrupt a CLI's normal
// (UTF-8) output while trying to rescue a garbled one.
func TestToUTF8PassThroughValidUTF8(t *testing.T) {
	in := "already UTF-8 中文 ok"
	if got := toUTF8([]byte(in)); got != in {
		t.Errorf("valid UTF-8 mangled: got %q want %q", got, in)
	}
}

// On a zh-CN box (code page 936) GBK-encoded stderr should come back readable.
// Skipped on other locales so the suite stays portable.
func TestToUTF8DecodesGBKStderr(t *testing.T) {
	if cp := legacyCP(); cp != 936 {
		t.Skipf("code page %d != 936 (GBK); skipping locale-specific check", cp)
	}
	gbk := []byte{0xC4, 0xE3, 0xBA, 0xC3} // "你好" in cp936/GBK — invalid as UTF-8
	if got := toUTF8(gbk); got != "你好" {
		t.Errorf("GBK decode: got %q, want 你好", got)
	}
}
