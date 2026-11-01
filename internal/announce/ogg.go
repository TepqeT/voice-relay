// Package announce plays short pre-recorded sounds (raid start, regroup, end of
// raid...) into every speaker channel of an active voice raid.
//
// Sounds are Ogg Opus files read once at startup. Their Opus packets are sent
// as-is, without decoding or re-encoding, so each packet must hold exactly
// 20 ms of audio: that is the frame size the Discord audio sender paces on.
package announce

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// FrameDuration is the only Opus packet duration accepted, in microseconds.
const frameDurationMicros = 20000

var (
	errNotOgg       = errors.New("not an Ogg stream (missing OggS capture pattern)")
	errNotOpus      = errors.New("Ogg stream does not carry Opus (missing OpusHead)")
	errNoAudio      = errors.New("Ogg Opus stream holds no audio packet")
	errFrameSize    = errors.New("Opus packets must be exactly 20 ms long (encode with -frame_duration 20)")
	errTruncatedOgg = errors.New("truncated Ogg page")
)

// readOggPackets splits an Ogg bitstream into its packets. Only the first
// logical stream is read; chained or multiplexed streams are not supported.
func readOggPackets(r io.Reader) ([][]byte, error) {
	var (
		packets [][]byte
		cur     []byte
		serial  uint32
		first   = true
		header  [27]byte
	)
	for {
		if _, err := io.ReadFull(r, header[:]); err != nil {
			if errors.Is(err, io.EOF) && !first {
				break
			}
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				if first {
					return nil, errNotOgg
				}
				return nil, errTruncatedOgg
			}
			return nil, err
		}
		if !bytes.Equal(header[0:4], []byte("OggS")) {
			return nil, errNotOgg
		}
		pageSerial := binary.LittleEndian.Uint32(header[14:18])
		if first {
			serial = pageSerial
			first = false
		}
		nSegs := int(header[26])
		segTable := make([]byte, nSegs)
		if _, err := io.ReadFull(r, segTable); err != nil {
			return nil, errTruncatedOgg
		}
		total := 0
		for _, s := range segTable {
			total += int(s)
		}
		body := make([]byte, total)
		if _, err := io.ReadFull(r, body); err != nil {
			return nil, errTruncatedOgg
		}
		if pageSerial != serial {
			continue // another logical stream: ignored
		}
		off := 0
		for _, s := range segTable {
			cur = append(cur, body[off:off+int(s)]...)
			off += int(s)
			if s < 255 {
				packets = append(packets, cur)
				cur = nil
			}
		}
	}
	return packets, nil
}

// opusPacketDurationMicros returns the audio duration carried by one Opus
// packet, from its TOC byte (RFC 6716 section 3.1).
func opusPacketDurationMicros(pkt []byte) (int, error) {
	if len(pkt) == 0 {
		return 0, errors.New("empty Opus packet")
	}
	toc := pkt[0]
	config := int(toc >> 3)
	var frame int
	switch {
	case config < 12: // SILK
		frame = []int{10000, 20000, 40000, 60000}[config%4]
	case config < 16: // Hybrid
		frame = []int{10000, 20000}[config%2]
	default: // CELT
		frame = []int{2500, 5000, 10000, 20000}[config%4]
	}
	var count int
	switch toc & 0x3 {
	case 0:
		count = 1
	case 1, 2:
		count = 2
	default:
		if len(pkt) < 2 {
			return 0, errors.New("truncated Opus code 3 packet")
		}
		count = int(pkt[1] & 0x3f)
	}
	return frame * count, nil
}

// decodeOggOpus extracts the Opus audio packets of an Ogg Opus file and checks
// that every one of them lasts 20 ms.
func decodeOggOpus(r io.Reader) ([][]byte, error) {
	packets, err := readOggPackets(r)
	if err != nil {
		return nil, err
	}
	if len(packets) == 0 || !bytes.HasPrefix(packets[0], []byte("OpusHead")) {
		return nil, errNotOpus
	}
	audio := make([][]byte, 0, len(packets))
	for i, p := range packets[1:] {
		if i == 0 && bytes.HasPrefix(p, []byte("OpusTags")) {
			continue
		}
		if len(p) == 0 {
			continue
		}
		d, err := opusPacketDurationMicros(p)
		if err != nil {
			return nil, fmt.Errorf("packet %d: %w", i+1, err)
		}
		if d != frameDurationMicros {
			return nil, fmt.Errorf("packet %d lasts %.1f ms: %w", i+1, float64(d)/1000, errFrameSize)
		}
		audio = append(audio, p)
	}
	if len(audio) == 0 {
		return nil, errNoAudio
	}
	return audio, nil
}
