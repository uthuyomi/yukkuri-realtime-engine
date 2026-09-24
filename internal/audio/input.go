package audio

import (
	"bytes"
	"errors"
)

var ErrInputLimit = errors.New("input audio capacity exceeded")
var ErrInputAlignment = errors.New("input PCM is not frame aligned")

// AppendPCM16 is shared by manual transcription and legacy realtime input.
func AppendPCM16(buffer *bytes.Buffer, payload []byte, limit int) error {
	if len(payload)%2 != 0 {
		return ErrInputAlignment
	}
	if buffer.Len()+len(payload) > limit {
		return ErrInputLimit
	}
	_, err := buffer.Write(payload)
	return err
}
