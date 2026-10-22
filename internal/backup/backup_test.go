package backup

import (
	"bytes"
	"testing"
)

func TestAuthenticatedEncryption(t *testing.T) {
	password := "isolated-backup-test-passphrase"
	plaintext := []byte(`{"test":"private identity and certificate"}`)
	file, err := Seal(plaintext, password)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(file, plaintext) {
		t.Fatal("plaintext leaked")
	}
	second, _ := Seal(plaintext, password)
	if bytes.Equal(file, second) {
		t.Fatal("salt/nonce reused")
	}
	got, err := Open(file, password)
	if err != nil || !bytes.Equal(got, plaintext) {
		t.Fatal("roundtrip", err)
	}
	for _, tc := range []struct {
		name     string
		file     []byte
		password string
	}{
		{"wrong-password", file, "incorrect-backup-passphrase"},
		{"truncated", file[:len(file)-1], password},
		{"short", []byte("IXP"), password},
		{"oversized", make([]byte, MaxFile+1), password},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Open(tc.file, tc.password); err == nil {
				t.Fatal("accepted invalid file")
			}
		})
	}
	for _, index := range []int{0, len(magic), headerSize - 1, len(file) - 1} {
		bad := append([]byte(nil), file...)
		bad[index] ^= 1
		if _, err := Open(bad, password); err == nil {
			t.Fatal("tamper accepted", index)
		}
	}
	if _, err = Seal(plaintext, ""); err == nil {
		t.Fatal("empty password accepted")
	}
	if _, err = Seal(plaintext, "x"); err != nil {
		t.Fatal("backup passwords have no length limit", err)
	}
	if _, err = Seal(make([]byte, MaxPlaintext+1), password); err == nil {
		t.Fatal("oversized plaintext")
	}
}
