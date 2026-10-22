// Package backup处理有界的业务数据包：填口令时认证加密，口令为空时为不加密的JSON（用户2026-10-22选择口令可选）；不接受归档路径或外部指令。
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

// Encrypted报告文件是否为加密备份格式。
func Encrypted(file []byte) bool { return bytes.HasPrefix(file, []byte(magic)) }

// Seal按口令加密；口令为空时不加密，返回JSON内容的副本。
func Seal(plaintext []byte, password string) ([]byte, error) {
	if len(plaintext) == 0 || len(plaintext) > MaxPlaintext {
		return nil, i18n.New("备份内容为空或超过8 MiB上限", "Backup content is empty or exceeds the 8 MiB limit")
	}
	if password == "" {
		return bytes.Clone(plaintext), nil
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

// Open按文件头识别：加密备份需要口令；不加密的备份是JSON，口令被忽略。
func Open(file []byte, password string) ([]byte, error) {
	if !Encrypted(file) {
		if text := bytes.TrimLeft(file, " \t\r\n"); len(text) == 0 || len(file) > MaxPlaintext || text[0] != '{' {
			return nil, i18n.New("不是受支持的备份文件，或文件版本/大小无效", "Not a supported backup file, or invalid file version/size")
		}
		return bytes.Clone(file), nil
	}
	if password == "" {
		return nil, i18n.New("该备份已加密，请填写它的备份口令", "This backup is encrypted; enter its backup password")
	}
	if len(file) < headerSize+16 || len(file) > MaxFile {
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
