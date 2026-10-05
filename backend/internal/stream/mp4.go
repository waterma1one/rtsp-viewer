package stream

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// maxBoxSize bounds memory per box. A 1 s H.264 fragment at camera bitrates
// is well under 2 MB; anything near this limit means a corrupt stream.
const maxBoxSize = 32 << 20

// SegmentKind tells the subscriber how to treat a segment.
type SegmentKind int

const (
	// InitSegment is ftyp+moov: codec configuration, sent once per client.
	InitSegment SegmentKind = iota
	// MediaSegment is moof+mdat. With -movflags frag_keyframe every media
	// segment starts on a keyframe, so a client may join at any of them.
	MediaSegment
)

type Segment struct {
	Kind SegmentKind
	Data []byte
}

// SegmentReader splits a fragmented MP4 byte stream (as written by
// `ffmpeg -f mp4 -movflags frag_keyframe+empty_moov`) into init and media
// segments that can be appended directly to an MSE SourceBuffer.
type SegmentReader struct {
	r       *bufio.Reader
	pending bytes.Buffer // boxes collected for the segment being built
	sawInit bool
}

func NewSegmentReader(r io.Reader) *SegmentReader {
	return &SegmentReader{r: bufio.NewReaderSize(r, 64<<10)}
}

// Next returns the next complete segment. Boxes other than moov/mdat (ftyp,
// moof, styp, sidx, free...) are accumulated and emitted with the box that
// closes the segment.
func (s *SegmentReader) Next() (Segment, error) {
	for {
		typ, box, err := readBox(s.r)
		if err != nil {
			return Segment{}, err
		}
		s.pending.Write(box)

		switch typ {
		case "moov":
			if s.sawInit {
				return Segment{}, errors.New("mp4: unexpected second moov")
			}
			s.sawInit = true
			return s.flush(InitSegment), nil
		case "mdat":
			if !s.sawInit {
				return Segment{}, errors.New("mp4: media data before moov")
			}
			return s.flush(MediaSegment), nil
		}
	}
}

func (s *SegmentReader) flush(kind SegmentKind) Segment {
	data := bytes.Clone(s.pending.Bytes())
	s.pending.Reset()
	return Segment{Kind: kind, Data: data}
}

// readBox reads one top-level box including its header.
func readBox(r io.Reader) (string, []byte, error) {
	var hdr [16]byte
	if _, err := io.ReadFull(r, hdr[:8]); err != nil {
		return "", nil, err
	}
	size := uint64(binary.BigEndian.Uint32(hdr[:4]))
	typ := string(hdr[4:8])
	hdrLen := 8
	switch size {
	case 0:
		// "extends to end of file" is meaningless on a live pipe.
		return "", nil, fmt.Errorf("mp4: box %q with size 0 not supported in a stream", typ)
	case 1:
		if _, err := io.ReadFull(r, hdr[8:16]); err != nil {
			return "", nil, unexpectedEOF(err)
		}
		size = binary.BigEndian.Uint64(hdr[8:16])
		hdrLen = 16
	}
	if size < uint64(hdrLen) {
		return "", nil, fmt.Errorf("mp4: box %q has invalid size %d", typ, size)
	}
	if size > maxBoxSize {
		return "", nil, fmt.Errorf("mp4: box %q size %d exceeds limit", typ, size)
	}
	buf := make([]byte, size)
	copy(buf, hdr[:hdrLen])
	if _, err := io.ReadFull(r, buf[hdrLen:]); err != nil {
		return "", nil, unexpectedEOF(err)
	}
	return typ, buf, nil
}

func unexpectedEOF(err error) error {
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}

// ErrUnsupportedCodec means the stream is not H.264. Browsers' MSE support
// for H.265 is inconsistent and transcoding is too expensive for the target
// hosting tier, so these streams are rejected with a clear message.
var ErrUnsupportedCodec = errors.New("unsupported video codec")

// CodecString derives the RFC 6381 codec parameter (e.g. "avc1.64001f") that
// MediaSource.addSourceBuffer needs, from an init segment.
func CodecString(init []byte) (string, error) {
	moov, ok := findChild(init, "moov")
	if !ok {
		return "", errors.New("mp4: init segment has no moov")
	}
	// Walk every trak: ffmpeg is told to map only video, but be tolerant.
	for _, trak := range children(moov, "trak") {
		stsd, ok := findPath(trak, "mdia", "minf", "stbl", "stsd")
		if !ok || len(stsd) < 8 {
			continue
		}
		// stsd is a FullBox: version/flags(4) + entry_count(4), then entries.
		for _, e := range boxes(stsd[8:]) {
			switch e.typ {
			case "avc1", "avc3":
				// VisualSampleEntry has 78 bytes of fixed fields before child boxes.
				if len(e.body) < 78 {
					return "", errors.New("mp4: truncated avc sample entry")
				}
				avcC, ok := findChild(e.body[78:], "avcC")
				if !ok || len(avcC) < 4 {
					return "", errors.New("mp4: missing avcC box")
				}
				return fmt.Sprintf("%s.%02x%02x%02x", e.typ, avcC[1], avcC[2], avcC[3]), nil
			case "hvc1", "hev1":
				return "", fmt.Errorf("%w: H.265/HEVC (only H.264 is supported)", ErrUnsupportedCodec)
			case "mp4v", "av01", "vp09":
				return "", fmt.Errorf("%w: %s (only H.264 is supported)", ErrUnsupportedCodec, e.typ)
			}
		}
	}
	return "", fmt.Errorf("%w: no video track found", ErrUnsupportedCodec)
}

type rawBox struct {
	typ  string
	body []byte
}

// boxes parses a sequence of boxes from an in-memory buffer, stopping at the
// first malformed header.
func boxes(b []byte) []rawBox {
	var out []rawBox
	for len(b) >= 8 {
		size := uint64(binary.BigEndian.Uint32(b[:4]))
		hdr := uint64(8)
		if size == 1 {
			if len(b) < 16 {
				break
			}
			size = binary.BigEndian.Uint64(b[8:16])
			hdr = 16
		} else if size == 0 {
			size = uint64(len(b))
		}
		if size < hdr || size > uint64(len(b)) {
			break
		}
		out = append(out, rawBox{typ: string(b[4:8]), body: b[hdr:size]})
		b = b[size:]
	}
	return out
}

func children(b []byte, typ string) [][]byte {
	var out [][]byte
	for _, c := range boxes(b) {
		if c.typ == typ {
			out = append(out, c.body)
		}
	}
	return out
}

func findChild(b []byte, typ string) ([]byte, bool) {
	for _, c := range boxes(b) {
		if c.typ == typ {
			return c.body, true
		}
	}
	return nil, false
}

func findPath(b []byte, path ...string) ([]byte, bool) {
	for _, p := range path {
		var ok bool
		if b, ok = findChild(b, p); !ok {
			return nil, false
		}
	}
	return b, true
}
