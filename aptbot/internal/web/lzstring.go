package web

import (
	"errors"
	"strings"
	"unicode/utf16"
)

// LZ-String (https://pieroxy.net/blog/pages/lz-string/) is a small JavaScript
// compression format; immowelt embeds its search data with it
// ("compressToBase64" output, recognisable by the "N4Ig…" prefix).

const lzKeyBase64 = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/="

var errLZ = errors.New("lzstring: corrupt input")

// LZDecompressFromBase64 decodes LZString.compressToBase64 output.
func LZDecompressFromBase64(input string) (string, error) {
	if input == "" {
		return "", nil
	}
	var idx [256]int
	for i := range idx {
		idx[i] = -1
	}
	for i := range len(lzKeyBase64) {
		idx[lzKeyBase64[i]] = i
	}
	n := len(input)
	next := func(i int) (int, error) {
		if i >= n {
			return 0, nil // past the end: lz-string pads with zero bits
		}
		v := idx[input[i]]
		if v < 0 {
			return 0, errLZ
		}
		return v, nil
	}
	return lzDecompress(n, 32, next)
}

func lzDecompress(length, resetValue int, next func(int) (int, error)) (string, error) {
	dict := [][]uint16{{0}, {1}, {2}}
	enlargeIn, numBits := 4, 3
	val, err := next(0)
	if err != nil {
		return "", err
	}
	position, index := resetValue, 1

	read := func(bitsN int) (int, error) {
		bits, power, maxpower := 0, 1, 1<<bitsN
		for power != maxpower {
			resb := val & position
			position >>= 1
			if position == 0 {
				position = resetValue
				var err error
				if val, err = next(index); err != nil {
					return 0, err
				}
				index++
			}
			if resb > 0 {
				bits |= power
			}
			power <<= 1
		}
		return bits, nil
	}

	var c []uint16
	first, err := read(2)
	if err != nil {
		return "", err
	}
	switch first {
	case 0, 1:
		width := 8
		if first == 1 {
			width = 16
		}
		v, err := read(width)
		if err != nil {
			return "", err
		}
		c = []uint16{uint16(v)}
	case 2:
		return "", nil
	default:
		return "", errLZ
	}
	dict = append(dict, c)
	w := c
	result := append([]uint16{}, c...)

	for {
		if index > length+1 {
			return "", errLZ
		}
		code, err := read(numBits)
		if err != nil {
			return "", err
		}
		switch code {
		case 0, 1:
			width := 8
			if code == 1 {
				width = 16
			}
			v, err := read(width)
			if err != nil {
				return "", err
			}
			dict = append(dict, []uint16{uint16(v)})
			code = len(dict) - 1
			enlargeIn--
		case 2:
			return string(utf16.Decode(result)), nil
		}
		if enlargeIn == 0 {
			enlargeIn = 1 << numBits
			numBits++
		}
		var entry []uint16
		switch {
		case code < len(dict):
			entry = dict[code]
		case code == len(dict):
			entry = append(append([]uint16{}, w...), w[0])
		default:
			return "", errLZ
		}
		result = append(result, entry...)
		dict = append(dict, append(append([]uint16{}, w...), entry[0]))
		enlargeIn--
		w = entry
		if enlargeIn == 0 {
			enlargeIn = 1 << numBits
			numBits++
		}
	}
}

// lzHasPrefix reports whether s looks like compressToBase64 output of JSON.
func lzHasPrefix(s string) bool { return strings.HasPrefix(s, "N4Ig") }
