// Package imagemeta reads a page image's format and pixel dimensions from its
// header, without decoding the image. JPEG, PNG and GIF use the standard
// library's DecodeConfig; WebP, BMP and AVIF have no stdlib decoder, so their
// headers are parsed here — with every offset bounds-checked, since the bytes
// come from untrusted archives.
package imagemeta

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"slices"
)

// Info is what a header tells us about an image.
type Info struct {
	Format        string // "jpeg", "png", "gif", "webp", "bmp" or "avif"
	Width, Height int
}

// ErrUnknown is returned (wrapped) when the data is not a supported format.
var ErrUnknown = errors.New("not a recognised image")

// avifHeaderMax bounds how much of an AVIF file is searched for its size box.
// The metadata sits at the front of the file, ahead of the image data.
const avifHeaderMax = 1 << 20

// maxSide bounds a plausible image dimension; a larger header value is treated
// as invalid (it also keeps width*height comparisons far from overflow).
const maxSide = 1 << 20

// Decode identifies the image in r by its magic bytes and reads its
// dimensions. It consumes only as much of r as the header needs (via a
// bufio.Reader, so callers wanting the rest should use DecodeReader).
func Decode(r io.Reader) (Info, error) {
	return DecodeReader(bufio.NewReader(r))
}

// DecodeReader is Decode on a caller-owned bufio.Reader, so the caller can
// keep reading past the header (e.g. to the end, for a checksum).
func DecodeReader(br *bufio.Reader) (Info, error) {
	head, _ := br.Peek(16) // short data is handled by the format checks
	var (
		cfg    image.Config
		err    error
		format string
	)
	switch {
	case bytes.HasPrefix(head, []byte{0xFF, 0xD8, 0xFF}):
		format = "jpeg"
		cfg, err = jpeg.DecodeConfig(br)
	case bytes.HasPrefix(head, []byte("\x89PNG\r\n\x1a\n")):
		format = "png"
		cfg, err = png.DecodeConfig(br)
	case bytes.HasPrefix(head, []byte("GIF87a")) || bytes.HasPrefix(head, []byte("GIF89a")):
		format = "gif"
		cfg, err = gif.DecodeConfig(br)
	case len(head) >= 12 && string(head[:4]) == "RIFF" && string(head[8:12]) == "WEBP":
		format = "webp"
		cfg, err = webpConfig(br)
	case bytes.HasPrefix(head, []byte("BM")):
		format = "bmp"
		cfg, err = bmpConfig(br)
	case len(head) >= 12 && string(head[4:8]) == "ftyp":
		format = "avif"
		cfg, err = avifConfig(br)
	default:
		return Info{}, ErrUnknown
	}
	if err != nil {
		return Info{}, fmt.Errorf("%s header: %w", format, err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > maxSide || cfg.Height > maxSide {
		return Info{}, fmt.Errorf("%s header: invalid size %dx%d", format, cfg.Width, cfg.Height)
	}
	return Info{Format: format, Width: cfg.Width, Height: cfg.Height}, nil
}

// readN reads exactly n bytes, reporting truncation as io.ErrUnexpectedEOF.
func readN(r io.Reader, n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return nil, err
	}
	return b, nil
}

// webpConfig reads the canvas size from the first chunk after the RIFF/WEBP
// header: VP8 (lossy), VP8L (lossless) or VP8X (extended).
func webpConfig(r io.Reader) (image.Config, error) {
	b, err := readN(r, 30)
	if err != nil {
		return image.Config{}, err
	}
	chunk, data := string(b[12:16]), b[20:]
	var w, h int
	switch chunk {
	case "VP8 ":
		// 3-byte frame tag, then start code 9d 01 2a, then 14-bit dimensions.
		if data[3] != 0x9d || data[4] != 0x01 || data[5] != 0x2a {
			return image.Config{}, errors.New("bad VP8 start code")
		}
		w = int(binary.LittleEndian.Uint16(data[6:8]) & 0x3fff)
		h = int(binary.LittleEndian.Uint16(data[8:10]) & 0x3fff)
	case "VP8L":
		if data[0] != 0x2f {
			return image.Config{}, errors.New("bad VP8L signature")
		}
		bits := binary.LittleEndian.Uint32(data[1:5])
		w = int(bits&0x3fff) + 1
		h = int(bits>>14&0x3fff) + 1
	case "VP8X":
		// 4 bytes of flags/reserved, then 24-bit canvas width-1 and height-1.
		w = int(uint32(data[4])|uint32(data[5])<<8|uint32(data[6])<<16) + 1
		h = int(uint32(data[7])|uint32(data[8])<<8|uint32(data[9])<<16) + 1
	default:
		return image.Config{}, fmt.Errorf("unknown chunk %q", chunk)
	}
	return image.Config{Width: w, Height: h}, nil
}

// bmpConfig reads the size from the DIB header after the 14-byte file header.
// Height is negative for top-down bitmaps.
func bmpConfig(r io.Reader) (image.Config, error) {
	b, err := readN(r, 26)
	if err != nil {
		return image.Config{}, err
	}
	var w, h int
	if binary.LittleEndian.Uint32(b[14:18]) == 12 { // OS/2 BITMAPCOREHEADER
		w = int(binary.LittleEndian.Uint16(b[18:20]))
		h = int(binary.LittleEndian.Uint16(b[20:22]))
	} else {
		w = int(int32(binary.LittleEndian.Uint32(b[18:22])))
		h = int(int32(binary.LittleEndian.Uint32(b[22:26])))
	}
	if h < 0 {
		h = -h
	}
	return image.Config{Width: w, Height: h}, nil
}

// avifConfig finds the image size in an AVIF (ISOBMFF/HEIF) file: the ispe
// property under meta/iprp/ipco. A file can carry several (thumbnails, alpha
// planes); the primary image is the largest, so that one is returned.
func avifConfig(r io.Reader) (image.Config, error) {
	data, err := io.ReadAll(io.LimitReader(r, avifHeaderMax))
	if err != nil {
		return image.Config{}, err
	}
	ftyp, _, ok := box(data, "ftyp")
	if !ok || len(ftyp) < 8 {
		return image.Config{}, errors.New("no ftyp box")
	}
	// Major brand, minor version, then the compatible brands.
	brands := append([][]byte{ftyp[:4]}, chunks(ftyp[8:], 4)...)
	if !slices.ContainsFunc(brands, func(b []byte) bool { return string(b) == "avif" || string(b) == "avis" }) {
		return image.Config{}, fmt.Errorf("brand %q is not AVIF", ftyp[:4])
	}
	meta, _, ok := box(data, "meta")
	if !ok || len(meta) < 4 {
		return image.Config{}, errors.New("no meta box in the first 1 MiB")
	}
	iprp, _, ok := box(meta[4:], "iprp") // meta is a full box: skip version/flags
	if !ok {
		return image.Config{}, errors.New("no iprp box")
	}
	ipco, _, ok := box(iprp, "ipco")
	if !ok {
		return image.Config{}, errors.New("no ipco box")
	}
	var best image.Config
	for rest := ipco; ; {
		ispe, next, ok := box(rest, "ispe")
		if !ok {
			break
		}
		if len(ispe) >= 12 { // version/flags, then 32-bit width and height
			w := int(binary.BigEndian.Uint32(ispe[4:8]))
			h := int(binary.BigEndian.Uint32(ispe[8:12]))
			if w <= maxSide && h <= maxSide && w*h > best.Width*best.Height {
				best = image.Config{Width: w, Height: h}
			}
		}
		rest = next
	}
	if best.Width == 0 {
		return image.Config{}, errors.New("no ispe property")
	}
	return best, nil
}

// chunks splits b into consecutive n-byte pieces, dropping a short tail.
func chunks(b []byte, n int) [][]byte {
	var out [][]byte
	for len(b) >= n {
		out = append(out, b[:n])
		b = b[n:]
	}
	return out
}

// box scans the sibling boxes in data for the first of type typ, returning its
// payload and the data after it. Sizes are validated against the buffer, so a
// hostile length can only end the scan.
func box(data []byte, typ string) (payload, rest []byte, ok bool) {
	for len(data) >= 8 {
		size := uint64(binary.BigEndian.Uint32(data[:4]))
		hdr := uint64(8)
		switch size {
		case 0: // extends to the end of the data
			size = uint64(len(data))
		case 1: // 64-bit size follows the type
			if len(data) < 16 {
				return nil, nil, false
			}
			size, hdr = binary.BigEndian.Uint64(data[8:16]), 16
		}
		if size < hdr || size > uint64(len(data)) {
			return nil, nil, false
		}
		if string(data[4:8]) == typ {
			return data[hdr:size], data[size:], true
		}
		data = data[size:]
	}
	return nil, nil, false
}
