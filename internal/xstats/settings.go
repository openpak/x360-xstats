package xstats

// Live services 34 (settings) and 35 (titles, achievements, avatars), also under
// /xstats: moved out of x360-xuacs.

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/openpak/x360-common/accountcore"
	"github.com/openpak/x360-common/wire"
)

// Native profile settings (Live service 34, /xstats/readsettings.ashx 0x711 and
// /xstats/syncsettings.ashx 0x710). Values are UTF-16LE with a terminator and
// come from the account core: Name = display name, Location = country code.
const (
	settingName     = 0x41040040
	settingLocation = 0x40520041
	settingBio      = 0x43E80043
	settingMotto    = 0x402C0011

	sourceDashboard = 1
	maxReadTargets  = 16
	maxSettings     = 32
)

// Profiles is the account core's profile store and discovery check.
type Profiles interface {
	Profile(context.Context, string) (*accountcore.Profile, error)
	UpdateProfile(context.Context, string, accountcore.ProfilePatch) error
	CanDiscover(context.Context, string, string) (bool, error)
}

// Accounts maps online XUIDs to accounts (x360-xuacs internal API).
type Accounts interface {
	AccountByXUID(context.Context, uint64) (string, bool, error)
}

type reqErr struct {
	status int
	msg    string
}

func (e *reqErr) Error() string             { return e.msg }
func bad(msg string) *reqErr               { return &reqErr{http.StatusBadRequest, msg} }
func deny(status int, msg string) *reqErr { return &reqErr{status, msg} }

// ticketUsers parses the gateway's users header strictly: every entry an
// online XUID and an account, at most 4.
func ticketUsers(header string) (map[uint64]string, error) {
	users := make(map[uint64]string)
	for _, entry := range strings.Split(header, ",") {
		x, account, ok := strings.Cut(entry, ":")
		if !ok || len(x) != 16 || account == "" {
			return nil, deny(403, "missing tunnel user")
		}
		xuid, err := strconv.ParseUint(x, 16, 64)
		if err != nil || xuid>>48 != 9 || users[xuid] != "" {
			return nil, deny(403, "invalid tunnel user")
		}
		users[xuid] = account
	}
	if len(users) > 4 {
		return nil, deny(403, "too many tunnel users")
	}
	return users, nil
}

func settingValue(p *accountcore.Profile, id uint32) (string, bool) {
	switch id {
	case settingName:
		return p.Name, true
	case settingLocation:
		return p.Country, true
	case settingBio:
		return p.Bio, true
	case settingMotto:
		return p.Motto, true
	}
	return "", false
}

func settingEntry(out []byte, xuid uint64, id uint32, v string) []byte {
	u := utf16.Encode([]rune(v))
	out = binary.LittleEndian.AppendUint32(out, sourceDashboard)
	out = binary.LittleEndian.AppendUint64(out, xuid)
	out = binary.LittleEndian.AppendUint32(out, id)
	out = binary.LittleEndian.AppendUint16(out, uint16(2*len(u)+2))
	for _, c := range u {
		out = binary.LittleEndian.AppendUint16(out, c)
	}
	return binary.LittleEndian.AppendUint16(out, 0)
}

func decodeSetting(b []byte) (string, bool) {
	if len(b) < 2 || len(b)%2 != 0 {
		return "", false
	}
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = binary.LittleEndian.Uint16(b[2*i:])
	}
	if u[len(u)-1] != 0 {
		return "", false
	}
	return string(utf16.Decode(u[:len(u)-1])), true
}

func (h *Handler) profileSettings(w http.ResponseWriter, r *http.Request, body []byte) bool {
	var fn func(context.Context, []byte, string) ([]byte, error)
	switch strings.ToLower(r.URL.Path) {
	case "/xstats/readsettings.ashx":
		fn = h.readSettings
	case "/xstats/syncsettings.ashx":
		fn = h.syncSettings
	default:
		return false
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	out, err := fn(ctx, body, r.Header.Get(wire.UsersHeader))
	if err != nil {
		status := http.StatusServiceUnavailable
		if re, ok := err.(*reqErr); ok {
			status = re.status
		}
		h.log().Warn("profile settings refused", "path", r.URL.Path, "status", status, "err", err)
		http.Error(w, http.StatusText(status), status)
		return true
	}
	h.log().Info("profile settings", "path", r.URL.Path, "body_len", len(body), "reply_len", len(out))
	w.Header().Set("Content-Type", "xon/5")
	w.Header().Set("Content-Length", strconv.Itoa(len(out)))
	w.WriteHeader(http.StatusOK)
	w.Write(out)
	return true
}

// readSettings: u32 flags, u64 caller, u16 n, n x u64 target, u16 m, m x u32 id.
func (h *Handler) readSettings(ctx context.Context, body []byte, usersHeader string) ([]byte, error) {
	core := h.Profiles
	if core == nil {
		return nil, deny(503, "account profile unavailable")
	}
	if len(body) < 14 {
		return nil, bad("0x711: short request")
	}
	users, err := ticketUsers(usersHeader)
	if err != nil {
		return nil, err
	}
	caller := binary.LittleEndian.Uint64(body[4:])
	callerAcct := users[caller]
	if callerAcct == "" {
		return nil, deny(403, "settings caller is not on tunnel ticket")
	}
	n := int(binary.LittleEndian.Uint16(body[12:]))
	b := body[14:]
	if n > maxReadTargets || len(b) < 8*n+2 {
		return nil, bad("0x711: bad target list")
	}
	targets := make([]uint64, n)
	for i := range targets {
		targets[i] = binary.LittleEndian.Uint64(b[8*i:])
	}
	b = b[8*n:]
	m := int(binary.LittleEndian.Uint16(b))
	b = b[2:]
	if m > maxSettings || len(b) != 4*m {
		return nil, bad("0x711: bad setting list")
	}
	var entries []byte
	count := 0
	for _, x := range targets {
		acct := users[x]
		if acct == "" && h.Accounts != nil {
			if acct, _, err = h.Accounts.AccountByXUID(ctx, x); err != nil {
				return nil, deny(503, "account service unavailable")
			}
		}
		if acct == "" {
			continue
		}
		if acct != callerAcct {
			if allowed, err := core.CanDiscover(ctx, callerAcct, acct); err != nil {
				return nil, deny(503, "account profile unavailable")
			} else if !allowed {
				continue
			}
		}
		p, err := core.Profile(ctx, acct)
		if err != nil {
			return nil, deny(503, "account profile unavailable")
		}
		for i := 0; i < m; i++ {
			id := binary.LittleEndian.Uint32(b[4*i:])
			if v, ok := settingValue(p, id); ok {
				entries = settingEntry(entries, x, id, v)
				count++
			}
		}
	}
	return append(binary.LittleEndian.AppendUint16(nil, uint16(count)), entries...), nil
}

// syncSettings: u32 title, u64 caller, u64 syncVersion, u16 start, u16 n, then
// n entries (u32 source, u64 xuid, u32 id, u16 len, bytes). Reply: u64 version,
// u16 pagination, u16 n, entries. The sync version is the core's profile_version:
// uploads apply only from a client that is current (or has never synced), so a
// stale console cannot overwrite an edit made on the website.
func (h *Handler) syncSettings(ctx context.Context, body []byte, usersHeader string) ([]byte, error) {
	core := h.Profiles
	if core == nil {
		return nil, deny(503, "account profile unavailable")
	}
	if len(body) < 24 {
		return nil, bad("0x710: short request")
	}
	users, err := ticketUsers(usersHeader)
	if err != nil {
		return nil, err
	}
	caller := binary.LittleEndian.Uint64(body[4:])
	acct := users[caller]
	if acct == "" {
		return nil, deny(403, "settings caller is not on tunnel ticket")
	}
	have := binary.LittleEndian.Uint64(body[12:])
	n := int(binary.LittleEndian.Uint16(body[22:]))
	if n > maxSettings {
		return nil, bad("0x710: too many entries")
	}
	b := body[24:]
	var patch accountcore.ProfilePatch
	dirty := false
	for i := 0; i < n; i++ {
		if len(b) < 18 {
			return nil, bad("0x710: truncated entry")
		}
		src := binary.LittleEndian.Uint32(b)
		xuid := binary.LittleEndian.Uint64(b[4:])
		id := binary.LittleEndian.Uint32(b[12:])
		l := int(binary.LittleEndian.Uint16(b[16:]))
		if len(b) < 18+l {
			return nil, bad("0x710: truncated value")
		}
		raw := b[18 : 18+l]
		b = b[18+l:]
		v, ok := decodeSetting(raw)
		if src == 0 || xuid != caller || !ok {
			continue
		}
		switch id {
		case settingName:
			patch.Name = &v
		case settingLocation:
			patch.Country = &v
		case settingBio:
			patch.Bio = &v
		case settingMotto:
			patch.Motto = &v
		default:
			continue
		}
		dirty = true
	}
	p, err := core.Profile(ctx, acct)
	if err != nil {
		return nil, deny(503, "account profile unavailable")
	}
	if dirty && (have == 0 || have == p.Version) {
		if err := core.UpdateProfile(ctx, acct, patch); err != nil {
			// The core rejected a value; the reply below sends the stored ones back.
			h.log().Warn("profile upload rejected", "err", err)
		} else if p, err = core.Profile(ctx, acct); err != nil {
			return nil, deny(503, "account profile unavailable")
		}
	}
	var entries []byte
	count := 0
	if have != p.Version {
		for _, id := range []uint32{settingName, settingLocation, settingBio, settingMotto} {
			v, _ := settingValue(p, id)
			entries = settingEntry(entries, caller, id, v)
			count++
		}
	}
	out := binary.LittleEndian.AppendUint64(nil, p.Version)
	out = binary.LittleEndian.AppendUint16(out, 0)
	out = binary.LittleEndian.AppendUint16(out, uint16(count))
	return append(out, entries...), nil
}

// xstatsEmpty maps the xstats endpoints (Live services 34 "settings" and 35
// "titles/avatars", requests 0x707-0x716) to an empty-success reply: every
// count zero (xrlcodec build_response(id, {}) in the Wharf notes). The console's
// post-logon title sync calls these; a profile with nothing to sync is the
// truthful answer for now. ponytail: stopgap inside xuacs; achievements and title
// history belong with x360-progress once it stores them.
var xstatsEmpty = map[string]string{
	"/xstats/syncsettings.ashx":           "000000000000000000000000",
	"/xstats/readsettings.ashx":           "0000",
	"/xstats/uploadconsoleauditlist.ashx": "",
	"/xstats/validateavatarmanifest.ashx": "00",
	"/xstats/synctitles.ashx":             "000000000000000000000000",
	"/xstats/enumtitles.ashx":             "0000",
	"/xstats/cleartitle.ashx":             "",
	"/xstats/syncachievements.ashx":       "000000000000000000000000",
	"/xstats/xachievementenum.ashx":       "0000",
	"/xstats/syncavatarassets.ashx":       "0000000000000000000000000000",
	"/xstats/xavatarassetenum.ashx":       "0000",
}

func (h *Handler) xstats(w http.ResponseWriter, r *http.Request, body []byte) bool {
	reply, ok := xstatsEmpty[strings.ToLower(r.URL.Path)]
	if !ok {
		return false
	}
	out, _ := hex.DecodeString(reply)
	h.log().Info("xstats request", "path", r.URL.Path, "account", r.Header.Get("X-X360-Account"), "body_len", len(body),
		"body_hex", hex.EncodeToString(body[:min(len(body), 256)]), "reply_len", len(out))
	w.Header().Set("Content-Type", "xon/5")
	w.Header().Set("Content-Length", strconv.Itoa(len(out)))
	w.WriteHeader(http.StatusOK)
	w.Write(out)
	return true
}
