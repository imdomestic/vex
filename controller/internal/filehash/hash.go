package filehash

import (
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"io"
	"os"
)

type Digests struct {
	SHA1   string
	SHA256 string
	SHA512 string
	Size   int64
}

func Reader(reader io.Reader) (Digests, error) {
	one, two, five := sha1.New(), sha256.New(), sha512.New()
	size, err := io.Copy(io.MultiWriter(one, two, five), reader)
	if err != nil {
		return Digests{}, err
	}
	return Digests{
		SHA1:   hex.EncodeToString(one.Sum(nil)),
		SHA256: hex.EncodeToString(two.Sum(nil)),
		SHA512: hex.EncodeToString(five.Sum(nil)),
		Size:   size,
	}, nil
}

func File(path string) (Digests, error) {
	file, err := os.Open(path)
	if err != nil {
		return Digests{}, err
	}
	defer file.Close()
	return Reader(file)
}
