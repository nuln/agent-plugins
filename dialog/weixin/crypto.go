package weixin

import (
	"bytes"
	"crypto/aes"
	"errors"
)

// EncryptAesEcb encrypts plaintext with AES-128-ECB and PKCS7 padding.
func EncryptAesEcb(plaintext, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	if len(key) != 16 {
		return nil, errors.New("weixin: key must be 16 bytes for AES-128")
	}

	padded := pkcs7Padding(plaintext, block.BlockSize())
	ciphertext := make([]byte, len(padded))
	for bs, be := 0, block.BlockSize(); bs < len(padded); bs, be = bs+block.BlockSize(), be+block.BlockSize() {
		block.Encrypt(ciphertext[bs:be], padded[bs:be])
	}
	return ciphertext, nil
}

// DecryptAesEcb decrypts ciphertext with AES-128-ECB and PKCS7 padding.
func DecryptAesEcb(ciphertext, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	if len(ciphertext)%block.BlockSize() != 0 {
		return nil, errors.New("weixin: ciphertext is not a multiple of the block size")
	}

	plaintext := make([]byte, len(ciphertext))
	for bs, be := 0, block.BlockSize(); bs < len(ciphertext); bs, be = bs+block.BlockSize(), be+block.BlockSize() {
		block.Decrypt(plaintext[bs:be], ciphertext[bs:be])
	}
	return pkcs7UnPadding(plaintext)
}

func pkcs7Padding(ciphertext []byte, blockSize int) []byte {
	padding := blockSize - len(ciphertext)%blockSize
	padtext := bytes.Repeat([]byte{byte(padding)}, padding)
	return append(ciphertext, padtext...)
}

func pkcs7UnPadding(origData []byte) ([]byte, error) {
	length := len(origData)
	if length == 0 {
		return nil, errors.New("weixin: empty ciphertext")
	}
	unpadding := int(origData[length-1])
	if unpadding > length || unpadding == 0 {
		return nil, errors.New("weixin: invalid padding")
	}
	return origData[:(length - unpadding)], nil
}

// AesEcbPaddedSize computes the size of the ciphertext after PKCS7 padding.
func AesEcbPaddedSize(plaintextSize int) int {
	return ((plaintextSize / 16) + 1) * 16
}
