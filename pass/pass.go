package pass

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	b64 "encoding/base64"
	"errors"
	"io"
	"strings"
)

func DecPass(encPass string, key []byte) (string, error) {
	var err error
	plainpass := encPass
	var cipherpass []byte
	var pass []byte
	if strings.HasPrefix(encPass, "{AES}") {
		encPass = strings.Replace(encPass, "{AES}", "", 1)
		cipherpass, err = b64.StdEncoding.DecodeString(encPass)
		if err != nil {
			return "", err
		}
		pass, err = decrypt(cipherpass, key)
		plainpass = string(pass)
	}
	return plainpass, err
}

func encrypt(plainpass []byte, key []byte) ([]byte, error) {
	c, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	gcm, err := cipher.NewGCM(c)
	if err != nil {
		return nil, err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}

	return gcm.Seal(nonce, nonce, plainpass, nil), nil
}

func decrypt(cipherpass []byte, key []byte) ([]byte, error) {
	c, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	gcm, err := cipher.NewGCM(c)
	if err != nil {
		return nil, err
	}

	nonceSize := gcm.NonceSize()
	if len(cipherpass) < nonceSize {
		return nil, errors.New("cipherpass too short")
	}

	nonce, cipherpass := cipherpass[:nonceSize], cipherpass[nonceSize:]
	return gcm.Open(nil, nonce, cipherpass, nil)
}
