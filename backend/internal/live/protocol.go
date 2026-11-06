// Package live relays collaborative edits to a page between browsers and
// keeps them durable: a short buffer on local disk, update segments and
// snapshots in S3 (docs/plan.md, docs/adr/0001-yjs-relay-in-go.md).
//
// The server never decodes Yjs documents. Binary frames start with a type
// byte:
//
//	1 update     both ways  a Yjs update
//	2 batch      to client  the stored document on join: repeated (u32 length, update)
//	3 awareness  both ways  a y-protocols awareness update (cursors, who is here)
//	5 snapshot   to server  u32 header length, JSON header, full Yjs state
//
// Text frames are JSON control messages: from the server "synced",
// "access", "compact" and "error"; from the browser "refresh".
package live

import (
	"encoding/binary"
	"errors"
)

const (
	frameUpdate    byte = 1
	frameBatch     byte = 2
	frameAwareness byte = 3
	frameSnapshot  byte = 5
)

const (
	maxUpdateBytes   = 1 << 20
	maxSnapshotBytes = 32 << 20
	maxTextBytes     = 1 << 20
)

// record is one update with its sequence number in the page's log.
type record struct {
	seq  int64
	data []byte
}

// appendRecord encodes a record as u64 seq, u32 length, bytes. Buffer
// files and S3 segments use the same encoding.
func appendRecord(dst []byte, r record) []byte {
	dst = binary.BigEndian.AppendUint64(dst, uint64(r.seq))
	dst = binary.BigEndian.AppendUint32(dst, uint32(len(r.data)))
	return append(dst, r.data...)
}

// parseRecords decodes records. A truncated record at the end (a write cut
// short by a crash) is dropped.
func parseRecords(b []byte) []record {
	var out []record
	for len(b) >= 12 {
		seq := int64(binary.BigEndian.Uint64(b))
		n := int(binary.BigEndian.Uint32(b[8:]))
		if len(b)-12 < n {
			break
		}
		out = append(out, record{seq: seq, data: b[12 : 12+n]})
		b = b[12+n:]
	}
	return out
}

// batchFrame packs updates into one frame for a joining browser.
func batchFrame(updates [][]byte) []byte {
	size := 1
	for _, u := range updates {
		size += 4 + len(u)
	}
	out := make([]byte, 0, size)
	out = append(out, frameBatch)
	for _, u := range updates {
		out = binary.BigEndian.AppendUint32(out, uint32(len(u)))
		out = append(out, u...)
	}
	return out
}

// snapshotHeader comes before the state in a snapshot frame.
type snapshotHeader struct {
	// Upto is the sequence number the server asked to compact to; the
	// state includes every update up to it.
	Upto  int64    `json:"upto"`
	Text  string   `json:"text"`
	Links []string `json:"links"`
}

var errBadFrame = errors.New("malformed frame")

// splitSnapshot splits a snapshot frame body into header JSON and state.
func splitSnapshot(b []byte) (header, state []byte, err error) {
	if len(b) < 4 {
		return nil, nil, errBadFrame
	}
	n := int(binary.BigEndian.Uint32(b))
	if len(b)-4 < n {
		return nil, nil, errBadFrame
	}
	return b[4 : 4+n], b[4+n:], nil
}

// awarenessClients reads the client IDs and clocks in a y-protocols
// awareness update: varuint count, then per client varuint id, varuint
// clock and a varstring JSON state. lib0 varuints are LEB128, like Go's.
func awarenessClients(b []byte) (map[uint64]uint64, error) {
	count, n := binary.Uvarint(b)
	if n <= 0 {
		return nil, errBadFrame
	}
	b = b[n:]
	out := make(map[uint64]uint64, count)
	for i := uint64(0); i < count; i++ {
		id, n := binary.Uvarint(b)
		if n <= 0 {
			return nil, errBadFrame
		}
		b = b[n:]
		clock, n := binary.Uvarint(b)
		if n <= 0 {
			return nil, errBadFrame
		}
		b = b[n:]
		l, n := binary.Uvarint(b)
		if n <= 0 || uint64(len(b)-n) < l {
			return nil, errBadFrame
		}
		b = b[n+int(l):]
		out[id] = clock
	}
	return out, nil
}

// awarenessRemoval builds the awareness update that tells other browsers
// these clients left (state null, clock bumped), like y-protocols'
// removeAwarenessStates.
func awarenessRemoval(clients map[uint64]uint64) []byte {
	out := []byte{frameAwareness}
	out = binary.AppendUvarint(out, uint64(len(clients)))
	for id, clock := range clients {
		out = binary.AppendUvarint(out, id)
		out = binary.AppendUvarint(out, clock+1)
		out = binary.AppendUvarint(out, 4)
		out = append(out, "null"...)
	}
	return out
}
