package xstats

import (
	"encoding/binary"
	"errors"
	"math"
)

// XRL "xon" bodies are the schema fields back to back, little-endian, no padding.

var errShort = errors.New("xstats: short body")

// Value types on the wire (XAM's stats param/column union, FUN_819359b0 and
// FUN_81922140): 0 null (u8), 1 u8, 2 u16, 3 int32, 4 int64, 5 double, 7 u64
// (XUID in procs, datetime in columns).
const (
	tNull   = 0
	tU8     = 1
	tU16    = 2
	tInt32  = 3
	tInt64  = 4
	tDouble = 5
	tU64    = 7
)

// Value is one typed stats value. Bits holds the integer, or the float64 bits for doubles.
type Value struct {
	Type uint8  `json:"t"`
	Bits uint64 `json:"v"`
}

func (v Value) isFloat() bool    { return v.Type == tDouble }
func (v Value) float() float64   { return math.Float64frombits(v.Bits) }
func floatValue(f float64) Value { return Value{tDouble, math.Float64bits(f)} }

// Int is the value as a signed integer (doubles truncate).
func (v Value) Int() int64 {
	switch v.Type {
	case tInt32:
		return int64(int32(v.Bits))
	case tDouble:
		return int64(v.float())
	}
	return int64(v.Bits)
}

func width(t uint8) int {
	switch t {
	case tNull, tU8:
		return 1
	case tU16:
		return 2
	case tInt32:
		return 4
	case tInt64, tDouble, tU64:
		return 8
	}
	return 0 // unknown type: nothing on the wire (no union arm matches)
}

type reader struct {
	b   []byte
	err error
}

func (r *reader) take(n int) []byte {
	if r.err != nil || len(r.b) < n {
		r.err = errShort
		return make([]byte, n)
	}
	v := r.b[:n]
	r.b = r.b[n:]
	return v
}

func (r *reader) u8() uint8   { return r.take(1)[0] }
func (r *reader) u16() uint16 { return binary.LittleEndian.Uint16(r.take(2)) }
func (r *reader) u32() uint32 { return binary.LittleEndian.Uint32(r.take(4)) }
func (r *reader) u64() uint64 { return binary.LittleEndian.Uint64(r.take(8)) }

func (r *reader) value() Value {
	t := r.u8()
	var b [8]byte
	copy(b[:], r.take(width(t)))
	return Value{t, binary.LittleEndian.Uint64(b[:])}
}

// count reads a u32 array count and rejects counts the body cannot hold.
func (r *reader) count(minElem int) int {
	n := int(r.u32())
	if r.err == nil && n*minElem > len(r.b) {
		r.err = errShort
		return 0
	}
	return n
}

type writer []byte

func (w *writer) u8(v uint8)   { *w = append(*w, v) }
func (w *writer) u16(v uint16) { *w = binary.LittleEndian.AppendUint16(*w, v) }
func (w *writer) u32(v uint32) { *w = binary.LittleEndian.AppendUint32(*w, v) }
func (w *writer) u64(v uint64) { *w = binary.LittleEndian.AppendUint64(*w, v) }

func (w *writer) value(v Value) {
	w.u8(v.Type)
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], v.Bits)
	*w = append(*w, b[:width(v.Type)]...)
}
