// Package backup处理有界、认证加密的业务数据包，不接受归档路径或外部指令。
package backup

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"independent-panel/internal/i18n"
	"io"

	"golang.org/x/crypto/scrypt"
)

const MaxPlaintext = 8 << 20
const MaxFile = MaxPlaintext + 64
const magic = "IXPBACK\x01"
const headerSize = len(magic) + 16 + 12

func ValidatePassword(password string) error {
	// 备份密码不限长度（用户2026-10-22要求），只要求非空。
	if password == "" {
		return i18n.New("请填写备份密码，请妥善保存，遗忘无法恢复", "Enter a backup password; keep it safe, it cannot be recovered")
	}
	return nil
}

func aead(password string, salt []byte) (cipher.AEAD, error) {
	key, err := scrypt.Key([]byte(password), salt, 65536, 8, 1, 32)
	if err != nil {
		return nil, err
	}
	defer clear(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func Seal(plaintext []byte, password string) ([]byte, error) {
	if err := ValidatePassword(password); err != nil {
		return nil, err
	}
	if len(plaintext) == 0 || len(plaintext) > MaxPlaintext {
		return nil, i18n.New("备份内容为空或超过8 MiB上限", "Backup content is empty or exceeds the 8 MiB limit")
	}
	header := make([]byte, headerSize)
	copy(header, magic)
	if _, err := io.ReadFull(rand.Reader, header[len(magic):]); err != nil {
		return nil, err
	}
	a, err := aead(password, header[len(magic):len(magic)+16])
	if err != nil {
		return nil, err
	}
	return a.Seal(header, header[headerSize-12:], plaintext, header), nil
}

func Open(file []byte, password string) ([]byte, error) {
	if err := ValidatePassword(password); err != nil {
		return nil, err
	}
	if len(file) < headerSize+16 || len(file) > MaxFile || !bytes.Equal(file[:len(magic)], []byte(magic)) {
		return nil, i18n.New("不是受支持的加密备份，或文件版本/大小无效", "Not a supported encrypted backup, or invalid file version/size")
	}
	header := file[:headerSize]
	a, err := aead(password, header[len(magic):len(magic)+16])
	if err != nil {
		return nil, err
	}
	plaintext, err := a.Open(nil, header[headerSize-12:], file[headerSize:], header)
	if err != nil {
		return nil, i18n.New("备份密码错误，或文件已损坏；未改变当前数据", "Wrong backup password or corrupted file; current data unchanged")
	}
	if len(plaintext) == 0 || len(plaintext) > MaxPlaintext {
		return nil, i18n.New("备份内容大小无效", "Invalid backup content size")
	}
	return plaintext, nil
}
