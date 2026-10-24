package wake

import (
	"bytes"
	"fmt"
	"io"

	pk "github.com/Tnze/go-mc/net/packet"
)

// go-mc 的可变字段分配内存前需要调用方限制声明长度。
type boundedField struct {
	decoder pk.FieldDecoder
	maximum int
}

func (field boundedField) ReadFrom(reader io.Reader) (int64, error) {
	var length pk.VarInt
	count, err := length.ReadFrom(reader)
	if err != nil {
		return count, err
	}
	if length < 0 || int(length) > field.maximum {
		return count, fmt.Errorf("Minecraft field exceeds its size limit")
	}
	var prefix bytes.Buffer
	if _, err = length.WriteTo(&prefix); err != nil {
		return count, err
	}
	return field.decoder.ReadFrom(io.MultiReader(&prefix, io.LimitReader(reader, int64(length))))
}
