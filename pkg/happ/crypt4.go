package happ

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"
)

// DecryptCrypt4 расшифровывает полезную нагрузку happ://crypt4/
// rawPayload - строка base64 после префикса happ://crypt4/
// secretKeys - кандидаты ключей: HWID, sub.HWID, токен или дефолтные соли
func DecryptCrypt4(rawPayload string, secretKeys ...string) ([]byte, error) {
	rawPayload = strings.TrimSpace(rawPayload)
	rawPayload = strings.TrimPrefix(rawPayload, "happ://crypt4/")
	rawPayload = strings.TrimPrefix(rawPayload, "crypt4/")

	if pad := len(rawPayload) % 4; pad != 0 {
		rawPayload += strings.Repeat("=", 4-pad)
	}

	data, err := base64.StdEncoding.DecodeString(rawPayload)
	if err != nil {
		data, err = base64.URLEncoding.DecodeString(rawPayload)
		if err != nil {
			return nil, fmt.Errorf("crypt4 base64 decode failed: %w", err)
		}
	}

	if len(data) < aes.BlockSize*2 {
		return nil, fmt.Errorf("crypt4 payload too short")
	}

	iv := data[:aes.BlockSize]
	ciphertext := data[aes.BlockSize:]

	var lastErr error
	for _, keyStr := range secretKeys {
		if keyStr == "" {
			continue
		}

		hash := sha256.Sum256([]byte(keyStr))

		// Пробуем AES-256 (32 байта) и AES-128 (16 байт)
		for _, keyLen := range []int{32, 16} {
			key := hash[:keyLen]
			block, err := aes.NewCipher(key)
			if err != nil {
				lastErr = err
				continue
			}

			if len(ciphertext)%block.BlockSize() != 0 {
				lastErr = fmt.Errorf("ciphertext is not a multiple of the block size")
				continue
			}

			mode := cipher.NewCBCDecrypter(block, iv)
			plain := make([]byte, len(ciphertext))
			mode.CryptBlocks(plain, ciphertext)

			unpadded, err := pkcs7Unpad(plain)
			if err == nil && len(unpadded) > 0 {
				return unpadded, nil
			}
			lastErr = err
		}
	}

	return nil, fmt.Errorf("failed to decrypt crypt4 with provided keys: %v", lastErr)
}

func pkcs7Unpad(data []byte) ([]byte, error) {
	length := len(data)
	if length == 0 {
		return nil, fmt.Errorf("empty data")
	}
	padding := int(data[length-1])
	if padding == 0 || padding > length || padding > aes.BlockSize {
		return nil, fmt.Errorf("invalid padding size: %d", padding)
	}
	for i := 0; i < padding; i++ {
		if data[length-1-i] != byte(padding) {
			return nil, fmt.Errorf("invalid padding byte")
		}
	}
	return data[:length-padding], nil
}

// IsCrypt4 определяет формат ссылки happ://crypt4/
func IsCrypt4(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.HasPrefix(s, "happ://crypt4/") || strings.HasPrefix(s, "crypt4/")
}
