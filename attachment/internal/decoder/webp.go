package decoder

import (
	"encoding/binary"
	"errors"
)

// webpFrame wraps the first animation frame for the standard WebP decoder.
// It validates all RIFF chunk bounds and frame rectangles without decoding later
// frames. The wrapper is temporary; the Store publishes the untouched source.
func webpFrame(data []byte) ([]byte, error) {
	invalid := errors.New("attachment: invalid WebP container")
	if len(data) < 12 || int64(binary.LittleEndian.Uint32(data[4:8]))+8 != int64(len(data)) {
		return nil, invalid
	}
	canvasWidth, canvasHeight := 0, 0
	animated := false
	var first []byte
	for offset := 12; offset < len(data); {
		if len(data)-offset < 8 {
			return nil, invalid
		}
		size := int64(binary.LittleEndian.Uint32(data[offset+4 : offset+8]))
		next := int64(offset) + 8 + size + (size & 1)
		if next > int64(len(data)) {
			return nil, invalid
		}
		chunk := data[offset+8 : int64(offset)+8+size]
		switch string(data[offset : offset+4]) {
		case "VP8X":
			if len(chunk) != 10 || canvasWidth != 0 {
				return nil, invalid
			}
			canvasWidth = uint24(chunk[4:7]) + 1
			canvasHeight = uint24(chunk[7:10]) + 1
			animated = chunk[0]&2 != 0
		case "ANMF":
			if !animated || len(chunk) < 16 {
				return nil, invalid
			}
			width, height := uint24(chunk[6:9])+1, uint24(chunk[9:12])+1
			if uint24(chunk[0:3])*2+width > canvasWidth || uint24(chunk[3:6])*2+height > canvasHeight {
				return nil, invalid
			}
			if first == nil {
				// ANMF carries ALPH/VP8/VP8L chunks after its 16-byte frame header.
				flags := byte(0)
				if len(chunk) >= 20 && string(chunk[16:20]) == "ALPH" {
					flags = 16
				}
				first = make([]byte, 30, len(chunk)+14)
				copy(first, "RIFF")
				copy(first[8:], "WEBPVP8X")
				binary.LittleEndian.PutUint32(first[16:20], 10)
				first[20] = flags
				putUint24(first[24:27], width-1)
				putUint24(first[27:30], height-1)
				first = append(first, chunk[16:]...)
				binary.LittleEndian.PutUint32(first[4:8], uint32(len(first)-8))
			}
		}
		offset = int(next)
	}
	if animated {
		if first == nil {
			return nil, invalid
		}
		return first, nil
	}
	return data, nil
}

func uint24(data []byte) int { return int(data[0]) | int(data[1])<<8 | int(data[2])<<16 }
func putUint24(data []byte, value int) {
	data[0] = byte(value)
	data[1] = byte(value >> 8)
	data[2] = byte(value >> 16)
}
