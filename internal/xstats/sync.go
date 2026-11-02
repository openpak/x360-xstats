package xstats

// Live service 35 (and two paths of 34): per-profile title, achievement and
// avatar-award sync, XAM 17559 requests 0x707-0x716. Layouts: Wharf note
// re-xstats-sync.md (docs/protocol.md). Lists live in the Store's JSON file.
//
// Versions: every list has a monotonic u64. Each accepted change gets its own
// revision (++list version), so "newer than the console's version" is Rev > have.
// The console stores the reply version and sends it back; 0 = never synced.

import (
	"encoding/hex"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/openpak/x360-common/wire"
)

const (
	maxSyncRecords = 32  // schema maximum per request, also our reply cap
	maxTitlePage   = 256 // 0x708 pages are 0x100
	maxAchPage     = 200
	flagPending    = 0x100000 // "achieved, pending upload": console-local, never stored
)

// Title is one title row of a profile (0x707 / 0x708 record).
type Title struct {
	Name        string `json:"name,omitempty"`
	LastPlayed  uint64 `json:"played"`
	GsEarned    uint32 `json:"gs"`
	AchUnlocked uint16 `json:"ach"`
	AchTotal    uint16 `json:"achtotal"`
	GsTotal     uint32 `json:"gstotal"`
	Rev         uint64 `json:"rev"`
}

// Item is one achievement (0x70a/0x70b) or avatar award (0x713/0x714).
type Item struct {
	Time  uint64 `json:"time"`
	Flags uint32 `json:"flags"`
	Image uint32 `json:"image,omitempty"`
	Cred  uint32 `json:"cred,omitempty"` // gamerscore as uploaded
	GUID  []byte `json:"guid,omitempty"` // avatar awards: asset GUID, 16 bytes
	Kind  uint8  `json:"kind,omitempty"` // achievements: type; awards: type/gender nibble
	Extra uint32 `json:"extra,omitempty"`
	Rev   uint64 `json:"rev"`
}

// ItemList is one (profile, title) list with its version.
type ItemList struct {
	Ver   uint64           `json:"ver"`
	Items map[uint32]*Item `json:"items"`
}

// Profile is everything synced for one XUID.
type Profile struct {
	TitleVer uint64               `json:"titlever"`
	Titles   map[uint32]*Title    `json:"titles"`
	Ach      map[uint32]*ItemList `json:"ach"`
	Avatar   map[uint32]*ItemList `json:"avatar"`
}

func (s *Store) profile(xuid uint64) *Profile {
	if s.Profiles == nil {
		s.Profiles = map[string]*Profile{}
	}
	k := xuidKey(xuid)
	p := s.Profiles[k]
	if p == nil {
		p = &Profile{Titles: map[uint32]*Title{}, Ach: map[uint32]*ItemList{}, Avatar: map[uint32]*ItemList{}}
		s.Profiles[k] = p
	}
	if p.Titles == nil {
		p.Titles = map[uint32]*Title{}
	}
	if p.Ach == nil {
		p.Ach = map[uint32]*ItemList{}
	}
	if p.Avatar == nil {
		p.Avatar = map[uint32]*ItemList{}
	}
	return p
}

func list(m map[uint32]*ItemList, title uint32) *ItemList {
	l := m[title]
	if l == nil {
		l = &ItemList{Items: map[uint32]*Item{}}
		m[title] = l
	}
	if l.Items == nil {
		l.Items = map[uint32]*Item{}
	}
	return l
}

func (r *reader) str8() string  { return string(r.take(int(r.u8()))) }
func (w *writer) str8(s string) { w.u8(uint8(len(s))); *w = append(*w, s...) }

func (r *reader) titleRec() (uint32, Title) {
	id := r.u32()
	t := Title{Name: string(r.take(int(r.u16())))}
	t.LastPlayed = r.u64()
	t.GsEarned = r.u32()
	t.AchUnlocked = r.u16()
	t.AchTotal = r.u16()
	t.GsTotal = r.u32()
	r.u32() // console change stamp, ignored
	r.u32() // unused
	return id, t
}

func (w *writer) titleRec(id uint32, t *Title) {
	w.u32(id)
	w.u16(uint16(len(t.Name)))
	*w = append(*w, t.Name...)
	w.u64(t.LastPlayed)
	w.u32(t.GsEarned)
	w.u16(t.AchUnlocked)
	w.u16(t.AchTotal)
	w.u32(t.GsTotal)
	w.u32(uint32(t.Rev))
	w.u32(0)
}

func skipArray(r *reader) { r.take(4 * int(r.u8())) }

// itemRec reads an achievement (avatar false) or avatar-award record.
func (r *reader) itemRec(avatar bool) (uint32, Item) {
	id := r.u32()
	it := Item{Time: r.u64()}
	r.u32() // stamp
	it.Flags = r.u32() &^ flagPending
	if avatar {
		it.GUID = append([]byte(nil), r.take(16)...)
		it.Image = r.u32()
		r.str8()
		r.str8()
		r.str8()
		it.Kind = r.u8()
		r.u32()
		it.Extra = r.u32()
		return id, it
	}
	it.Kind = r.u8()
	it.Flags = it.Flags&^7 | uint32(it.Kind&7)
	it.Image = r.u32()
	it.Cred = r.u32()
	r.str8()
	r.str8()
	r.str8()
	skipArray(r)
	skipArray(r)
	return id, it
}

func (w *writer) itemRec(id uint32, it *Item, avatar bool) {
	w.u32(id)
	w.u64(it.Time)
	w.u32(uint32(it.Rev))
	if avatar {
		w.u32(it.Flags)
		g := make([]byte, 16)
		copy(g, it.GUID)
		*w = append(*w, g...)
		w.u32(it.Image)
		// ponytail: catalogue strings come later from x360-progress
		w.str8("")
		w.str8("")
		w.str8("")
		w.u8(it.Kind)
		w.u32(0)
		w.u32(it.Extra)
		return
	}
	w.u32(it.Flags &^ 7)
	w.u8(uint8(it.Flags & 7))
	w.u32(it.Image)
	w.u32(it.Cred)
	w.str8("") // ponytail: catalogue strings come later from x360-progress
	w.str8("")
	w.str8("")
	w.u8(0)
	w.u8(0)
}

// ids returns the list's item ids ordered by revision (oldest first) or id.
func sortedItems(l *ItemList, byRev bool) []uint32 {
	ids := make([]uint32, 0, len(l.Items))
	for id := range l.Items {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if byRev && l.Items[ids[i]].Rev != l.Items[ids[j]].Rev {
			return l.Items[ids[i]].Rev < l.Items[ids[j]].Rev
		}
		return ids[i] < ids[j]
	})
	return ids
}

func (h *Handler) xstats(w http.ResponseWriter, r *http.Request, body []byte) bool {
	var fn func(*reader, map[uint64]string) ([]byte, error)
	switch strings.ToLower(r.URL.Path) {
	case "/xstats/synctitles.ashx":
		fn = h.syncTitles
	case "/xstats/enumtitles.ashx":
		fn = h.enumTitles
	case "/xstats/cleartitle.ashx":
		fn = h.clearTitle
	case "/xstats/syncachievements.ashx":
		fn = func(rd *reader, u map[uint64]string) ([]byte, error) { return h.syncItems(rd, u, false) }
	case "/xstats/xachievementenum.ashx":
		fn = func(rd *reader, u map[uint64]string) ([]byte, error) { return h.enumItems(rd, u, false) }
	case "/xstats/syncavatarassets.ashx":
		fn = func(rd *reader, u map[uint64]string) ([]byte, error) { return h.syncItems(rd, u, true) }
	case "/xstats/xavatarassetenum.ashx":
		fn = func(rd *reader, u map[uint64]string) ([]byte, error) { return h.enumItems(rd, u, true) }
	case "/xstats/uploadconsoleauditlist.ashx":
		fn = h.auditList
	case "/xstats/validateavatarmanifest.ashx":
		fn = func(*reader, map[uint64]string) ([]byte, error) { return []byte{0}, nil } // unused by 17559
	default:
		return false
	}
	var out []byte
	users, err := ticketUsers(r.Header.Get(wire.UsersHeader))
	if err == nil {
		out, err = fn(&reader{b: body}, users)
	}
	if err != nil {
		status := http.StatusBadRequest
		if re, ok := err.(*reqErr); ok {
			status = re.status
		}
		h.log().Warn("xstats refused", "path", r.URL.Path, "status", status, "err", err, "body_hex", hex.EncodeToString(body[:min(len(body), 256)]))
		http.Error(w, http.StatusText(status), status)
		return true
	}
	h.log().Info("xstats sync", "path", r.URL.Path, "body_len", len(body), "reply_len", len(out))
	w.Header().Set("Content-Type", ContentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(out)))
	w.WriteHeader(http.StatusOK)
	w.Write(out)
	return true
}

func (h *Handler) store() (*Store, error) {
	if h.Store == nil {
		return nil, deny(503, "no store")
	}
	return h.Store, nil
}

// caller checks the XUID is on the gateway ticket (a console may only sync its own users).
func caller(users map[uint64]string, xuid uint64) error {
	if users[xuid] == "" {
		return deny(403, "sync caller is not on tunnel ticket")
	}
	return nil
}

// 0x707: u64 xuid, u16, u16, u64 version, u16 n, n x title. Reply: u64 version,
// u16 0, u16 n, titles changed since `version` that this request did not upload.
// Merge [I]: union by titleId, keep the larger lastPlayed and counters.
func (h *Handler) syncTitles(r *reader, users map[uint64]string) ([]byte, error) {
	xuid := r.u64()
	r.u32()
	have := r.u64()
	n := int(r.u16())
	if r.err != nil || n > maxSyncRecords {
		return nil, bad("0x707: bad request")
	}
	s, err := h.store()
	if err != nil {
		return nil, err
	}
	if err := caller(users, xuid); err != nil {
		return nil, err
	}
	type up struct {
		id uint32
		t  Title
	}
	ups := make([]up, n)
	for i := range ups {
		ups[i].id, ups[i].t = r.titleRec()
	}
	if r.err != nil {
		return nil, bad("0x707: truncated")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.profile(xuid)
	sent := map[uint32]bool{}
	for _, u := range ups {
		sent[u.id] = true
		t := p.Titles[u.id]
		if t == nil {
			t = &Title{}
			p.Titles[u.id] = t
		} else if u.t.LastPlayed <= t.LastPlayed && u.t.GsEarned <= t.GsEarned && u.t.AchUnlocked <= t.AchUnlocked &&
			u.t.AchTotal <= t.AchTotal && u.t.GsTotal <= t.GsTotal && (u.t.Name == "" || u.t.Name == t.Name) {
			continue
		}
		t.LastPlayed = max(t.LastPlayed, u.t.LastPlayed)
		t.GsEarned = max(t.GsEarned, u.t.GsEarned)
		t.AchUnlocked = max(t.AchUnlocked, u.t.AchUnlocked)
		t.AchTotal = max(t.AchTotal, u.t.AchTotal)
		t.GsTotal = max(t.GsTotal, u.t.GsTotal)
		if u.t.Name != "" {
			t.Name = u.t.Name
		}
		p.TitleVer++
		t.Rev = p.TitleVer
	}
	var ids []uint32
	for id, t := range p.Titles {
		if t.Rev > have && !sent[id] {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return p.Titles[ids[i]].Rev < p.Titles[ids[j]].Rev })
	ver := p.TitleVer
	if len(ids) > maxSyncRecords {
		// A full page makes the console resync every title, so cut it short and
		// report the last returned revision: the next call continues from there. [I]
		ids = ids[:maxSyncRecords]
		ver = p.Titles[ids[len(ids)-1]].Rev
	}
	var w writer
	w.u64(ver)
	w.u16(0)
	w.u16(uint16(len(ids)))
	for _, id := range ids {
		w.titleRec(id, p.Titles[id])
	}
	return w, s.saveLocked()
}

// 0x708: u64 xuid, u64 target, u16, u16 start, u16 max. Reply: u16 n, titles by id.
// A target that is not a user on this ticket answers empty. [I]
func (h *Handler) enumTitles(r *reader, users map[uint64]string) ([]byte, error) {
	xuid, target := r.u64(), r.u64()
	r.u16()
	start, max := int(r.u16()), int(r.u16())
	if r.err != nil {
		return nil, bad("0x708: short request")
	}
	s, err := h.store()
	if err != nil {
		return nil, err
	}
	if err := caller(users, xuid); err != nil {
		return nil, err
	}
	if max > maxTitlePage {
		max = maxTitlePage
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []uint32
	if users[target] != "" {
		for id := range s.profile(target).Titles {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	ids = ids[min(start, len(ids)):]
	ids = ids[:min(max, len(ids))]
	var w writer
	w.u16(uint16(len(ids)))
	for _, id := range ids {
		w.titleRec(id, s.profile(target).Titles[id])
	}
	return w, nil
}

// 0x709: u64 xuid, u32 titleId. Drops the title, its achievements and awards.
func (h *Handler) clearTitle(r *reader, users map[uint64]string) ([]byte, error) {
	xuid, id := r.u64(), r.u32()
	if r.err != nil {
		return nil, bad("0x709: short request")
	}
	s, err := h.store()
	if err != nil {
		return nil, err
	}
	if err := caller(users, xuid); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.profile(xuid)
	delete(p.Titles, id)
	delete(p.Ach, id)
	delete(p.Avatar, id)
	p.TitleVer++
	return nil, s.saveLocked()
}

// 0x70a / 0x713: u32 title, u64 xuid, u64 version, u16, u16 start, u16 n, n records.
// Reply: u64 version, u16 0, u16 n, records newer than `version` that were not just
// uploaded (the console discards them but a non-zero count makes it enumerate).
func (h *Handler) syncItems(r *reader, users map[uint64]string, avatar bool) ([]byte, error) {
	title := r.u32()
	xuid := r.u64()
	have := r.u64()
	r.u16()
	r.u16()
	n := int(r.u16())
	if r.err != nil || n > maxSyncRecords {
		return nil, bad("sync: bad request")
	}
	s, err := h.store()
	if err != nil {
		return nil, err
	}
	if err := caller(users, xuid); err != nil {
		return nil, err
	}
	type up struct {
		id uint32
		it Item
	}
	ups := make([]up, n)
	for i := range ups {
		ups[i].id, ups[i].it = r.itemRec(avatar)
	}
	if r.err != nil {
		return nil, bad("sync: truncated")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.profile(xuid)
	m := p.Ach
	if avatar {
		m = p.Avatar
	}
	l := list(m, title)
	sent := map[uint32]bool{}
	for _, u := range ups {
		sent[u.id] = true
		if old := l.Items[u.id]; old != nil && old.Time == u.it.Time && old.Flags == u.it.Flags {
			continue
		}
		l.Ver++
		u.it.Rev = l.Ver
		l.Items[u.id] = &u.it
	}
	var ids []uint32
	for _, id := range sortedItems(l, true) {
		if l.Items[id].Rev > have && !sent[id] {
			ids = append(ids, id)
		}
	}
	ids = ids[:min(len(ids), maxSyncRecords)]
	var w writer
	w.u64(l.Ver)
	w.u16(0)
	w.u16(uint16(len(ids)))
	for _, id := range ids {
		w.itemRec(id, l.Items[id], avatar)
	}
	return w, s.saveLocked()
}

// 0x70b / 0x714: u32 title, u64 xuid, u64 target, u16, u16 start, u16 max. Reply:
// u16 n, records by id.
func (h *Handler) enumItems(r *reader, users map[uint64]string, avatar bool) ([]byte, error) {
	title := r.u32()
	xuid, target := r.u64(), r.u64()
	r.u16()
	start, max := int(r.u16()), int(r.u16())
	if r.err != nil {
		return nil, bad("enum: short request")
	}
	s, err := h.store()
	if err != nil {
		return nil, err
	}
	if err := caller(users, xuid); err != nil {
		return nil, err
	}
	if max > maxAchPage {
		max = maxAchPage
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []uint32
	var l *ItemList
	if users[target] != "" {
		p := s.profile(target)
		m := p.Ach
		if avatar {
			m = p.Avatar
		}
		l = list(m, title)
		ids = sortedItems(l, false)
	}
	ids = ids[min(start, len(ids)):]
	ids = ids[:min(max, len(ids))]
	var w writer
	w.u16(uint16(len(ids)))
	for _, id := range ids {
		w.itemRec(id, l.Items[id], avatar)
	}
	return w, nil
}

// 0x715: u64 xuid, 2 x u8[5] console ids, u8 n, n x u8[5]. Accepted, logged, not stored. [I]
func (h *Handler) auditList(r *reader, users map[uint64]string) ([]byte, error) {
	xuid := r.u64()
	r.take(10)
	n := int(r.u8())
	r.take(5 * n)
	if r.err != nil || n > 100 {
		return nil, bad("0x715: bad request")
	}
	if err := caller(users, xuid); err != nil {
		return nil, err
	}
	h.log().Info("console audit list", "xuid", xuidKey(xuid), "consoles", n)
	return nil, nil
}
