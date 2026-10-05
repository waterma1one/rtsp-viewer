package stream

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

func box(typ string, payload ...[]byte) []byte {
	body := bytes.Join(payload, nil)
	out := make([]byte, 8, 8+len(body))
	binary.BigEndian.PutUint32(out, uint32(8+len(body)))
	copy(out[4:], typ)
	return append(out, body...)
}

func largeBox(typ string, body []byte) []byte {
	out := make([]byte, 16, 16+len(body))
	binary.BigEndian.PutUint32(out, 1)
	copy(out[4:], typ)
	binary.BigEndian.PutUint64(out[8:], uint64(16+len(body)))
	return append(out, body...)
}

// initSegment builds ftyp+moov with one video track using the given sample
// entry type and avcC profile/compat/level bytes.
func initSegment(entry string, profile, compat, level byte) []byte {
	avcC := box("avcC", []byte{1, profile, compat, level, 0xff})
	sampleEntry := box(entry, make([]byte, 78), avcC)
	stsd := box("stsd", []byte{0, 0, 0, 0, 0, 0, 0, 1}, sampleEntry)
	trak := box("trak", box("tkhd", make([]byte, 20)),
		box("mdia", box("mdhd", make([]byte, 24)), box("minf", box("stbl", stsd))))
	return append(box("ftyp", []byte("isom\x00\x00\x02\x00")), box("moov", box("mvhd", make([]byte, 100)), trak)...)
}

func TestSegmentReader(t *testing.T) {
	init := initSegment("avc1", 0x64, 0x00, 0x1f)
	frag1 := append(box("moof", []byte("frag-1-meta")), box("mdat", []byte("frag-1-data"))...)
	frag2 := append(box("moof", []byte("frag-2-meta")), largeBox("mdat", []byte("frag-2-large"))...)

	r := NewSegmentReader(bytes.NewReader(bytes.Join([][]byte{init, frag1, frag2}, nil)))

	want := []Segment{{InitSegment, init}, {MediaSegment, frag1}, {MediaSegment, frag2}}
	for i, w := range want {
		got, err := r.Next()
		if err != nil {
			t.Fatalf("segment %d: %v", i, err)
		}
		if got.Kind != w.Kind || !bytes.Equal(got.Data, w.Data) {
			t.Fatalf("segment %d mismatch: kind %v len %d, want kind %v len %d", i, got.Kind, len(got.Data), w.Kind, len(w.Data))
		}
	}
	if _, err := r.Next(); err != io.EOF {
		t.Fatalf("want io.EOF at clean end, got %v", err)
	}
}

func TestSegmentReaderErrors(t *testing.T) {
	init := initSegment("avc1", 0x42, 0xc0, 0x1e)
	tests := []struct {
		name  string
		input []byte
		want  error
	}{
		{"truncated box", append(init, box("moof", []byte("xxxx"))[:10]...), io.ErrUnexpectedEOF},
		{"mdat before moov", box("mdat", []byte("x")), nil},
		{"size zero", []byte{0, 0, 0, 0, 'm', 'd', 'a', 't'}, nil},
		{"size smaller than header", []byte{0, 0, 0, 4, 'm', 'o', 'o', 'f'}, nil},
		{"oversized", []byte{0x7f, 0xff, 0xff, 0xff, 'm', 'd', 'a', 't'}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := NewSegmentReader(bytes.NewReader(tc.input))
			var err error
			for range 5 {
				if _, err = r.Next(); err != nil {
					break
				}
			}
			if err == nil || err == io.EOF {
				t.Fatalf("want error, got %v", err)
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
		})
	}
}

func TestCodecString(t *testing.T) {
	tests := []struct {
		name    string
		init    []byte
		want    string
		wantErr error
	}{
		{"high 3.1", initSegment("avc1", 0x64, 0x00, 0x1f), "avc1.64001f", nil},
		{"baseline 3.0", initSegment("avc1", 0x42, 0xc0, 0x1e), "avc1.42c01e", nil},
		{"avc3", initSegment("avc3", 0x4d, 0x40, 0x28), "avc3.4d4028", nil},
		{"hevc", initSegment("hvc1", 0, 0, 0), "", ErrUnsupportedCodec},
		{"no moov", box("ftyp"), "", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CodecString(tc.init)
			if tc.want != "" {
				if err != nil || got != tc.want {
					t.Fatalf("got %q, %v; want %q", got, err, tc.want)
				}
				return
			}
			if err == nil {
				t.Fatal("want error")
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("want %v, got %v", tc.wantErr, err)
			}
		})
	}
}
