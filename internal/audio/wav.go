package audio

import (
	"encoding/binary"
	"fmt"
)

type PCM struct {
	Data       []byte
	SampleRate int
	Channels   int
	Bits       int
}

func DecodeWAV(data []byte) (*PCM, error) {
	if len(data) < 12 {
		return nil, fmt.Errorf("WAV data is too short")
	}

	if string(data[0:4]) != "RIFF" {
		return nil, fmt.Errorf("invalid WAV RIFF header")
	}

	if string(data[8:12]) != "WAVE" {
		return nil, fmt.Errorf("invalid WAV format")
	}

	var (
		formatFound bool
		dataFound   bool

		audioFormat uint16
		channels    uint16
		sampleRate  uint32
		bits        uint16

		pcmData []byte
	)

	offset := 12

	for offset+8 <= len(data) {
		chunkID := string(data[offset : offset+4])

		chunkSize := int(
			binary.LittleEndian.Uint32(
				data[offset+4 : offset+8],
			),
		)

		offset += 8

		if chunkSize < 0 || offset+chunkSize > len(data) {
			return nil, fmt.Errorf(
				"invalid WAV chunk %q size %d",
				chunkID,
				chunkSize,
			)
		}

		chunk := data[offset : offset+chunkSize]

		switch chunkID {
		case "fmt ":
			if len(chunk) < 16 {
				return nil, fmt.Errorf(
					"WAV fmt chunk is too short",
				)
			}

			audioFormat = binary.LittleEndian.Uint16(
				chunk[0:2],
			)

			channels = binary.LittleEndian.Uint16(
				chunk[2:4],
			)

			sampleRate = binary.LittleEndian.Uint32(
				chunk[4:8],
			)

			bits = binary.LittleEndian.Uint16(
				chunk[14:16],
			)

			formatFound = true

		case "data":
			pcmData = append(
				[]byte(nil),
				chunk...,
			)

			dataFound = true
		}

		offset += chunkSize

		// RIFF chunks are word-aligned.
		if chunkSize%2 != 0 {
			offset++
		}
	}

	if !formatFound {
		return nil, fmt.Errorf("WAV fmt chunk not found")
	}

	if !dataFound {
		return nil, fmt.Errorf("WAV data chunk not found")
	}

	if audioFormat != 1 {
		return nil, fmt.Errorf(
			"unsupported WAV audio format %d; PCM required",
			audioFormat,
		)
	}

	if channels == 0 {
		return nil, fmt.Errorf("invalid WAV channel count")
	}

	if sampleRate == 0 {
		return nil, fmt.Errorf("invalid WAV sample rate")
	}

	if bits != 16 {
		return nil, fmt.Errorf(
			"unsupported WAV bit depth %d; 16-bit PCM required",
			bits,
		)
	}

	if len(pcmData)%2 != 0 {
		return nil, fmt.Errorf(
			"16-bit PCM data has odd byte length %d",
			len(pcmData),
		)
	}

	return &PCM{
		Data:       pcmData,
		SampleRate: int(sampleRate),
		Channels:   int(channels),
		Bits:       int(bits),
	}, nil
}
