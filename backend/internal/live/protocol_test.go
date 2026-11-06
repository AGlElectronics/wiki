package live

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestRecordsRoundTrip(t *testing.T) {
	var b []byte
	b = appendRecord(b, record{seq: 1, data: []byte("a")})
	b = appendRecord(b, record{seq: 2, data: []byte{}})
	b = appendRecord(b, record{seq: 3, data: []byte("ccc")})

	got := parseRecords(b)
	if len(got) != 3 || got[0].seq != 1 || string(got[0].data) != "a" || len(got[1].data) != 0 || string(got[2].data) != "ccc" {
		t.Fatalf("round trip = %+v", got)
	}
	// A write cut short by a crash loses only the last record.
	if got := parseRecords(b[:len(b)-1]); len(got) != 2 {
		t.Fatalf("truncated tail: got %d records, want 2", len(got))
	}
	if got := parseRecords(b[:5]); len(got) != 0 {
		t.Fatalf("truncated header: got %d records", len(got))
	}
}

func TestBatchFrame(t *testing.T) {
	f := batchFrame([][]byte{[]byte("x"), []byte("yz")})
	want := []byte{frameBatch, 0, 0, 0, 1, 'x', 0, 0, 0, 2, 'y', 'z'}
	if !bytes.Equal(f, want) {
		t.Fatalf("batch = %v, want %v", f, want)
	}
}

func TestSplitSnapshot(t *testing.T) {
	body := binary.BigEndian.AppendUint32(nil, 2)
	body = append(body, "{}STATE"...)
	h, s, err := splitSnapshot(body)
	if err != nil || string(h) != "{}" || string(s) != "STATE" {
		t.Fatalf("split = %q %q %v", h, s, err)
	}
	if _, _, err := splitSnapshot(body[:3]); err == nil {
		t.Fatal("short body should fail")
	}
	if _, _, err := splitSnapshot(binary.BigEndian.AppendUint32(nil, 10)); err == nil {
		t.Fatal("header longer than body should fail")
	}
}

func TestAwareness(t *testing.T) {
	var b []byte
	b = binary.AppendUvarint(b, 2)
	for _, c := range []struct {
		id, clock uint64
		state     string
	}{{300, 5, `{"user":{}}`}, {7, 1, "null"}} {
		b = binary.AppendUvarint(b, c.id)
		b = binary.AppendUvarint(b, c.clock)
		b = binary.AppendUvarint(b, uint64(len(c.state)))
		b = append(b, c.state...)
	}
	clients, err := awarenessClients(b)
	if err != nil || len(clients) != 2 || clients[300] != 5 || clients[7] != 1 {
		t.Fatalf("clients = %v %v", clients, err)
	}
	if _, err := awarenessClients(b[:len(b)-2]); err == nil {
		t.Fatal("truncated state should fail")
	}

	removal := awarenessRemoval(map[uint64]uint64{300: 5})
	if removal[0] != frameAwareness {
		t.Fatalf("removal frame type %d", removal[0])
	}
	got, err := awarenessClients(removal[1:])
	if err != nil || got[300] != 6 {
		t.Fatalf("removal clients = %v %v", got, err)
	}
	if !bytes.HasSuffix(removal, []byte("null")) {
		t.Fatalf("removal state should be null: %q", removal)
	}
}
