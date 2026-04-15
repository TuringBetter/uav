// Package codec provides binary serialisation and deserialisation for Message values.
//
// Encoding is little-endian for efficiency on common architectures.
// An optional zlib compression flag (leading byte) is supported for large payloads.
//
// Wire format:
//
//	[compressed:1][Type:1][From:2][To:2][Seq:4][Timestamp:8][DataTime:8][StreamID:1][TTL:2][Priority:1][PayloadLen:4][Payload:N]
//
// When compressed == 1, the bytes after the first byte are zlib-compressed.
// When compressed == 0, the remaining bytes follow the raw wire format above.
package codec

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"io"
	"uav/pkg/message"
)

var le = binary.LittleEndian

// CompressThreshold is the minimum payload size (in bytes) that triggers automatic
// zlib compression.  Set to 0 to always compress; MaxInt to never compress.
const CompressThreshold = 256

// Encode serialises msg into a byte slice.
// Payloads larger than CompressThreshold are automatically zlib-compressed.
func Encode(msg message.Message) ([]byte, error) {
	raw := encodeRaw(msg)

	if len(msg.Payload) >= CompressThreshold {
		compressed, err := compress(raw)
		if err == nil && len(compressed) < len(raw) {
			// Prefix with 0x01 to signal compression.
			out := make([]byte, 1+len(compressed))
			out[0] = 0x01
			copy(out[1:], compressed)
			return out, nil
		}
		// Compression did not help; fall through to uncompressed.
	}

	// Prefix with 0x00 (uncompressed).
	out := make([]byte, 1+len(raw))
	out[0] = 0x00
	copy(out[1:], raw)
	return out, nil
}

// Decode deserialises a byte slice produced by Encode into a Message.
func Decode(data []byte) (message.Message, error) {
	if len(data) < 1 {
		return message.Message{}, fmt.Errorf("codec: empty data")
	}

	compressed := data[0] == 0x01
	payload := data[1:]

	if compressed {
		var err error
		payload, err = decompress(payload)
		if err != nil {
			return message.Message{}, fmt.Errorf("codec: decompress: %w", err)
		}
	}

	return decodeRaw(payload)
}

// encodeRaw serialises msg fields (without the compression flag byte).
func encodeRaw(msg message.Message) []byte {
	payloadLen := uint32(len(msg.Payload))
	buf := make([]byte, message.HeaderSize+int(payloadLen))
	off := 0

	buf[off] = uint8(msg.Type)
	off++
	le.PutUint16(buf[off:], msg.From)
	off += 2
	le.PutUint16(buf[off:], msg.To)
	off += 2
	le.PutUint32(buf[off:], msg.Seq)
	off += 4
	le.PutUint64(buf[off:], uint64(msg.Timestamp))
	off += 8
	le.PutUint64(buf[off:], uint64(msg.DataTime))
	off += 8
	buf[off] = uint8(msg.StreamID)
	off++
	le.PutUint16(buf[off:], msg.TTL)
	off += 2
	buf[off] = uint8(msg.Priority)
	off++
	le.PutUint32(buf[off:], payloadLen)
	off += 4
	copy(buf[off:], msg.Payload)
	return buf
}

// decodeRaw deserialises raw (non-compressed) bytes into a Message.
func decodeRaw(data []byte) (message.Message, error) {
	if len(data) < message.HeaderSize {
		return message.Message{}, fmt.Errorf("codec: data too short: got %d, need %d",
			len(data), message.HeaderSize)
	}

	var msg message.Message
	off := 0

	msg.Type = message.MessageType(data[off])
	off++
	msg.From = le.Uint16(data[off:])
	off += 2
	msg.To = le.Uint16(data[off:])
	off += 2
	msg.Seq = le.Uint32(data[off:])
	off += 4
	msg.Timestamp = int64(le.Uint64(data[off:]))
	off += 8
	msg.DataTime = int64(le.Uint64(data[off:]))
	off += 8
	msg.StreamID = data[off]
	off++
	msg.TTL = le.Uint16(data[off:])
	off += 2
	msg.Priority = message.Priority(data[off])
	off++
	payloadLen := le.Uint32(data[off:])
	off += 4

	remaining := len(data) - off
	if remaining < int(payloadLen) {
		return message.Message{}, fmt.Errorf("codec: payload truncated: declared %d, available %d",
			payloadLen, remaining)
	}
	if payloadLen > 0 {
		msg.Payload = make([]byte, payloadLen)
		copy(msg.Payload, data[off:off+int(payloadLen)])
	}
	return msg, nil
}

func compress(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	if _, err := w.Write(data); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func decompress(data []byte) ([]byte, error) {
	r, err := zlib.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}
