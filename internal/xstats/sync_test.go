package xstats

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openpak/x360-common/wire"
)

func syncPost(t *testing.T, h *Handler, path string, body []byte) []byte {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, path, nil)
	r.Header.Set(wire.UsersHeader, me+":owner")
	w := httptest.NewRecorder()
	if !h.xstats(w, r, body) || w.Code != 200 {
		t.Fatalf("%s: code %d", path, w.Code)
	}
	return w.Body.Bytes()
}

func hx(s string) []byte {
	b, err := hex.DecodeString(strings.ReplaceAll(s, " ", ""))
	if err != nil {
		panic(err)
	}
	return b
}

// Note 4.1 request example, version 5, one title (Halo 3 numbers, nameLen 0).
const titleReq = "3412000000000900 0000 0000 0500000000000000 0100 e607534d 0000 f0e0d0c0b0a0d801 fa000000 0300 0a00 e8030000 4d000000 00000000"

func TestTitlesRoundTrip(t *testing.T) {
	h := &Handler{Store: &Store{}}
	// The note's xuid bytes are not ours; patch in the test user.
	req := hx(titleReq)
	binary.LittleEndian.PutUint64(req, 0x0009000000000001)
	out := syncPost(t, h, "/xstats/synctitles.ashx", req)
	// version 1, unread 0, nothing else to return (the upload is not echoed).
	if !bytes.Equal(out, hx("0100000000000000 0000 0000")) {
		t.Fatalf("0x707 reply %x", out)
	}
	// second title from another console, console version 1: reply carries only items newer than 1.
	req2 := hx(titleReq)
	binary.LittleEndian.PutUint64(req2, 0x0009000000000001)
	binary.LittleEndian.PutUint32(req2[22:], 0x4d5307e7)
	binary.LittleEndian.PutUint64(req2[12:], 1)
	out = syncPost(t, h, "/xstats/synctitles.ashx", req2)
	if !bytes.Equal(out, hx("0200000000000000 0000 0000")) {
		t.Fatalf("0x707 second reply %x", out)
	}
	// poll with no records: version 1 gets title 2 only, version 0 gets both.
	poll := func(v uint64) []byte {
		b := binary.LittleEndian.AppendUint64(nil, 0x0009000000000001)
		b = append(b, 0, 0, 0, 0)
		b = binary.LittleEndian.AppendUint64(b, v)
		return append(b, 0, 0)
	}
	out = syncPost(t, h, "/xstats/synctitles.ashx", poll(1))
	if binary.LittleEndian.Uint16(out[10:]) != 1 || binary.LittleEndian.Uint32(out[12:]) != 0x4d5307e7 || len(out) != 12+34 {
		t.Fatalf("delta reply %x", out)
	}
	if out = syncPost(t, h, "/xstats/synctitles.ashx", poll(0)); binary.LittleEndian.Uint16(out[10:]) != 2 {
		t.Fatalf("full reply %x", out)
	}
	// enum
	enum := binary.LittleEndian.AppendUint64(nil, 0x0009000000000001)
	enum = binary.LittleEndian.AppendUint64(enum, 0x0009000000000001)
	enum = append(enum, 0, 0, 0, 0, 0, 1)
	out = syncPost(t, h, "/xstats/enumtitles.ashx", enum)
	want := hx("0200 e607534d 0000 f0e0d0c0b0a0d801 fa000000 0300 0a00 e8030000 01000000 00000000")
	if !bytes.HasPrefix(out, want) || len(out) != 2+2*34 {
		t.Fatalf("0x708 reply %x", out)
	}
	// clear
	clr := binary.LittleEndian.AppendUint64(nil, 0x0009000000000001)
	clr = binary.LittleEndian.AppendUint32(clr, 0x4d5307e6)
	syncPost(t, h, "/xstats/cleartitle.ashx", clr)
	if out = syncPost(t, h, "/xstats/enumtitles.ashx", enum); out[0] != 1 {
		t.Fatalf("after clear %x", out)
	}
}

func itemBody(title uint32, have uint64, avatar bool, ids ...uint32) []byte {
	b := binary.LittleEndian.AppendUint32(nil, title)
	b = binary.LittleEndian.AppendUint64(b, 0x0009000000000001)
	b = binary.LittleEndian.AppendUint64(b, have)
	b = append(b, 0, 0, 0, 0)
	b = binary.LittleEndian.AppendUint16(b, uint16(len(ids)))
	for _, id := range ids {
		b = binary.LittleEndian.AppendUint32(b, id)
		b = append(b, hx("f0e0d0c0b0a0d801")...)
		b = append(b, 0, 0, 0, 0)       // stamp
		b = append(b, 0x00, 0, 0x13, 0) // flags 0x130000: achieved + pending
		if avatar {
			b = append(b, hx("44332211 6655 8877 0001020304050607 34120000 00 00 00 01 00000000 07000000")...)
		} else {
			b = append(b, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)
		}
	}
	return b
}

func itemEnumBody(title uint32, start, max uint16) []byte {
	b := binary.LittleEndian.AppendUint32(nil, title)
	b = binary.LittleEndian.AppendUint64(b, 0x0009000000000001)
	b = binary.LittleEndian.AppendUint64(b, 0x0009000000000001)
	b = append(b, 0, 0)
	b = binary.LittleEndian.AppendUint16(b, start)
	return binary.LittleEndian.AppendUint16(b, max)
}

func TestItemsRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name, sync, enum string
		avatar           bool
	}{
		{"achievement", "syncachievements", "xachievementenum", false},
		{"avatar", "syncavatarassets", "xavatarassetenum", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &Handler{Store: &Store{}}
			out := syncPost(t, h, "/xstats/"+tc.sync+".ashx", itemBody(0x4d5307e6, 0, tc.avatar, 5))
			if !bytes.Equal(out, hx("0100000000000000 0000 0000")) {
				t.Fatalf("upload reply %x", out)
			}
			out = syncPost(t, h, "/xstats/"+tc.sync+".ashx", itemBody(0x4d5307e6, 1, tc.avatar, 6))
			if !bytes.Equal(out, hx("0200000000000000 0000 0000")) {
				t.Fatalf("second upload reply %x", out)
			}
			// re-uploading the same record does not bump the version
			if out = syncPost(t, h, "/xstats/"+tc.sync+".ashx", itemBody(0x4d5307e6, 2, tc.avatar, 6)); out[0] != 2 {
				t.Fatalf("idempotent upload bumped version: %x", out)
			}
			// poll from version 1 returns award 6 only
			out = syncPost(t, h, "/xstats/"+tc.sync+".ashx", itemBody(0x4d5307e6, 1, tc.avatar))
			if binary.LittleEndian.Uint16(out[10:]) != 1 || binary.LittleEndian.Uint32(out[12:]) != 6 {
				t.Fatalf("delta reply %x", out)
			}
			out = syncPost(t, h, "/xstats/"+tc.enum+".ashx", itemEnumBody(0x4d5307e6, 0, 200))
			if binary.LittleEndian.Uint16(out) != 2 || binary.LittleEndian.Uint32(out[2:]) != 5 {
				t.Fatalf("enum reply %x", out)
			}
			if !bytes.Equal(out[6:14], hx("f0e0d0c0b0a0d801")) {
				t.Fatalf("unlock time lost: %x", out)
			}
			// pending-upload bit is never echoed; achieved bit is
			if f := binary.LittleEndian.Uint32(out[18:]); f&^7 != 0x30000 {
				t.Fatalf("flags %x", f)
			}
			// paging: start past the end is empty, max 1 gives one
			if out = syncPost(t, h, "/xstats/"+tc.enum+".ashx", itemEnumBody(0x4d5307e6, 1, 1)); out[0] != 1 || binary.LittleEndian.Uint32(out[2:]) != 6 {
				t.Fatalf("page 2: %x", out)
			}
			if out = syncPost(t, h, "/xstats/"+tc.enum+".ashx", itemEnumBody(0x4d5307e6, 2, 200)); !bytes.Equal(out, []byte{0, 0}) {
				t.Fatalf("page past end: %x", out)
			}
		})
	}
}

func TestAuditAndRefusal(t *testing.T) {
	h := &Handler{Store: &Store{}}
	b := binary.LittleEndian.AppendUint64(nil, 0x0009000000000001)
	b = append(b, make([]byte, 10)...)
	b = append(b, 1, 1, 2, 3, 4, 5)
	if out := syncPost(t, h, "/xstats/uploadconsoleauditlist.ashx", b); len(out) != 0 {
		t.Fatalf("audit reply %x", out)
	}
	if out := syncPost(t, h, "/xstats/validateavatarmanifest.ashx", nil); !bytes.Equal(out, []byte{0}) {
		t.Fatalf("0x716 reply %x", out)
	}
	// a XUID that is not on the ticket is refused
	r := httptest.NewRequest(http.MethodPost, "/xstats/cleartitle.ashx", nil)
	r.Header.Set(wire.UsersHeader, "0009000000000002:other")
	w := httptest.NewRecorder()
	h.xstats(w, r, append(binary.LittleEndian.AppendUint64(nil, 0x0009000000000001), 0, 0, 0, 0))
	if w.Code != 403 {
		t.Fatalf("foreign xuid code %d", w.Code)
	}
	if h.xstats(w, httptest.NewRequest("POST", "/xstats/other.ashx", nil), nil) {
		t.Fatal("unknown xstats path served")
	}
}

func TestSyncPersists(t *testing.T) {
	path := pgSchema(t)
	s := testOpen(t, path)
	h := &Handler{Store: s}
	syncPost(t, h, "/xstats/syncachievements.ashx", itemBody(1, 0, false, 5))
	s2 := testOpen(t, path)
	if got := s2.Profiles[me].Ach[1].Items[5]; got == nil || got.Rev != 1 {
		t.Fatalf("not persisted: %+v", s2.Profiles)
	}
}
