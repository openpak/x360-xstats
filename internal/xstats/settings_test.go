package xstats

import (
	"context"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openpak/x360-common/accountcore"
	"github.com/openpak/x360-common/wire"
)

type profCore struct {
	p       accountcore.Profile
	visible bool
}

func (c *profCore) Profile(context.Context, string) (*accountcore.Profile, error) {
	p := c.p
	return &p, nil
}
func (c *profCore) UpdateProfile(_ context.Context, _ string, u accountcore.ProfilePatch) error {
	if u.Bio != nil {
		c.p.Bio = *u.Bio
	}
	if u.Motto != nil {
		c.p.Motto = *u.Motto
	}
	c.p.Version++
	return nil
}
func (c *profCore) CanDiscover(context.Context, string, string) (bool, error) { return c.visible, nil }

const (
	me    = "0009000000000001"
	other = uint64(0x0009000000000002)
)

func postSettings(h *Handler, path string, body []byte) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, path, nil)
	r.Header.Set(wire.UsersHeader, me+":owner")
	w := httptest.NewRecorder()
	h.profileSettings(w, r, body)
	return w
}

func syncBody(have uint64, id uint32, v string) []byte {
	b := binary.LittleEndian.AppendUint32(nil, 0xFFFE07D1)
	b = binary.LittleEndian.AppendUint64(b, 0x0009000000000001)
	b = binary.LittleEndian.AppendUint64(b, have)
	b = binary.LittleEndian.AppendUint16(b, 0)
	b = binary.LittleEndian.AppendUint16(b, 1)
	e := settingEntry(nil, 0x0009000000000001, id, v)
	return append(b, e...)
}

func TestSyncUploadAndStale(t *testing.T) {
	core := &profCore{p: accountcore.Profile{Bio: "old", Version: 3}}
	h := &Handler{Profiles: core}
	if w := postSettings(h, "/xstats/syncsettings.ashx", syncBody(3, settingMotto, "hello")); w.Code != 200 || core.p.Motto != "hello" {
		t.Fatalf("current upload: %d %q", w.Code, core.p.Motto)
	}
	w := postSettings(h, "/xstats/syncsettings.ashx", syncBody(3, settingMotto, "stale")) // version is now 4
	if core.p.Motto != "hello" {
		t.Fatal("stale upload overwrote the core")
	}
	if got := binary.LittleEndian.Uint64(w.Body.Bytes()); got != 4 {
		t.Fatalf("version %d", got)
	}
	if n := binary.LittleEndian.Uint16(w.Body.Bytes()[10:]); n != 4 {
		t.Fatalf("entries %d", n)
	}
}

func TestReadDeniesUndiscoverable(t *testing.T) {
	core := &profCore{p: accountcore.Profile{Motto: "hi"}}
	h := &Handler{Profiles: core, Accounts: accts{other: "stranger"}}
	b := binary.LittleEndian.AppendUint32(nil, 0)
	b = binary.LittleEndian.AppendUint64(b, 0x0009000000000001)
	b = binary.LittleEndian.AppendUint16(b, 1)
	b = binary.LittleEndian.AppendUint64(b, other)
	b = binary.LittleEndian.AppendUint16(b, 1)
	b = binary.LittleEndian.AppendUint32(b, settingMotto)
	if w := postSettings(h, "/xstats/readsettings.ashx", b); w.Body.Len() != 2 {
		t.Fatalf("hidden profile leaked: %x", w.Body.Bytes())
	}
	core.visible = true
	if w := postSettings(h, "/xstats/readsettings.ashx", b); binary.LittleEndian.Uint16(w.Body.Bytes()) != 1 {
		t.Fatalf("visible profile missing: %x", w.Body.Bytes())
	}
}

type accts map[uint64]string

func (a accts) AccountByXUID(_ context.Context, x uint64) (string, bool, error) {
	v, ok := a[x]
	return v, ok, nil
}

func TestXStatsEmpty(t *testing.T) {
	rec := httptest.NewRecorder()
	h := &Handler{}
	if !h.xstats(rec, httptest.NewRequest("POST", "/xstats/synctitles.ashx", nil), nil) || rec.Body.Len() != 12 {
		t.Fatalf("xstats reply %d bytes", rec.Body.Len())
	}
	if h.xstats(rec, httptest.NewRequest("POST", "/xstats/other.ashx", nil), nil) {
		t.Fatal("unknown xstats path served")
	}
}
