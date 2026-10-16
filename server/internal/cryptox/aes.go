package cryptox

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"io"
)

// 平台不再对凭据加密（明文入库，随数据库自身访问控制保障）。
// 以下保留 AES-256-GCM 逻辑仅用于读取历史版本的密文数据。
var key []byte

func Init(k []byte) {
	if len(k) != 32 {
		panic("cryptox: 解密密钥必须为 32 字节")
	}
	key = k
}

// Encrypt 明文存储，原样返回
func Encrypt(plaintext []byte) (string, error) {
	return string(plaintext), nil
}

// Decrypt 优先按历史密文（AES-256-GCM + base64）解密；解不开（本就是明文 / 密钥不符）则原样返回。
// 旧密文读取成功后应尽快重新保存，落库即明文。
func Decrypt(ciphertext string) ([]byte, error) {
	if key != nil {
		if plain, err := decryptLegacy(ciphertext); err == nil {
			return plain, nil
		}
	}
	return []byte(ciphertext), nil
}

// decryptLegacy 历史版本密文：nonce 前置拼接密文后 base64
func decryptLegacy(ciphertext string) ([]byte, error) {
	data, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(data) < gcm.NonceSize() {
		return nil, io.ErrUnexpectedEOF
	}
	return gcm.Open(nil, data[:gcm.NonceSize()], data[gcm.NonceSize():], nil)
}
