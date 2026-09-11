package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const version = "0.2.0"

// hostedMode is set when the server listens off loopback. A few things that
// are fine on your own laptop are not fine on a shared machine.
var hostedMode bool

// Writer turns a morning into a brief: gather, refine, ask Claude, store.
// It's shared by every account, and only writes one at a time so a server with
// ten users doesn't start ten Claudes at 07:30.
type Writer struct {
	mu    sync.Mutex
	board *progressBoard
}

func newWriter() *Writer { return &Writer{board: newProgressBoard()} }

// Progress is what this account's brief is doing right now.
func (wr *Writer) Progress(user string) Progress { return wr.board.For(user) }

func claudeAvailable() bool {
	_, err := exec.LookPath("claude")
	return err == nil
}

func (wr *Writer) Generate(ctx context.Context, store *UserStore, trigger string) (brief *Brief, err error) {
	wr.mu.Lock()
	defer wr.mu.Unlock()

	who := store.ID()
	wr.board.start(who, "write")
	defer func() {
		wr.board.edit(who, func(p *Progress) {
			if err != nil {
				p.Stage, p.Error, p.Note = StageFailed, err.Error(), "Could not write the brief"
				return
			}
			p.Stage, p.Note = StageDone, "Ready"
			if brief != nil {
				p.BriefID = brief.ID
			}
		})
	}()

	cfg := store.Config()
	now := cfg.Now()
	past, _ := store.Briefs()
	window := windowFor(past, cfg, now)

	// Read into the store. Everything gathered survives whatever happens
	// next: a failed write costs one Claude call, not five minutes of Slack.
	sig, own, err := wr.gather(ctx, store, cfg, window, watcher{wr.board, who})
	if err != nil {
		return nil, err
	}
	wr.board.edit(who, func(p *Progress) { p.Stage, p.Note = StageSorting, "Sorting what came back" })

	// The cheap model reads everything new and says what each thing is. Its
	// cost is recorded with the brief's; its failure is not fatal, because a
	// morning without triage is the morning we had before triage existed.
	past3 := history(store, 3)
	carried := []*PastTodo{}
	for i := range past3 {
		for j := range past3[i].Todos {
			carried = append(carried, &past3[i].Todos[j])
		}
	}
	usage := []Usage{}
	if spent, err := TriageNew(ctx, cfg, sig, own, now); err != nil {
		log.Printf("triage failed, writing without it: %v", err)
	} else {
		usage = append(usage, spent...)
	}
	// Old to-dos are judged again each morning: a deck for Tuesday's sync
	// has lapsed by Wednesday whatever Tuesday's brief thought of it.
	if spent, err := JudgeCarried(ctx, cfg, sig, now, carried); err != nil {
		log.Printf("carried to-dos not judged, carrying them as they are: %v", err)
	} else if spent != nil {
		usage = append(usage, *spent)
	}
	_ = store.SaveSignals(sig) // the classifications, so tomorrow does not pay for them again

	results := sig.ResultsSince(window.Since, cfg)
	// Silenced places go first: before ranking, before trimming, before the
	// model is asked to judge anything. (The sweep already declined to read
	// them; this catches what was stored before they were silenced.)
	Silence(results, cfg.Mutes)
	ledger := store.Attention()
	FadedOut(results, ledger)
	// The expensive model sees the shortlist and nothing else.
	Shortlist(results, sig, ledger, own)

	signals := 0
	for _, r := range results {
		signals += len(r.Items)
	}

	painting, _ := PickPainting(ctx, DayKey(now))

	prompt := BuildPrompt(cfg, now, results, store.Memory(), past3, painting, own)
	if dir := os.Getenv("POMONA_DEBUG_PROMPT"); dir != "" && !hostedMode {
		// Whether a thin brief means a thin morning or a broken collector is
		// invisible from the outside, and guessing at it is expensive.
		_ = os.WriteFile(filepath.Join(dir, "prompt-"+DayKey(now)+".txt"), []byte(prompt), 0o600)
	}

	wr.board.edit(who, func(p *Progress) {
		p.Stage, p.Signals = StageWriting, signals
		p.Note = fmt.Sprintf("Writing from %d signal%s", signals, plural2(signals))
	})

	written, err := Write(ctx, cfg, systemPrompt, prompt)
	if err != nil {
		return nil, err
	}
	if err := ValidateBrief(written.JSON); err != nil {
		return nil, err
	}
	// Two things the model is asked for and cannot be trusted to do every
	// time, so they are done to its output rather than requested of it.
	written.JSON = dedupePush(written.JSON)
	written.JSON = backfillDue(written.JSON, sig, carried...)

	wr.board.edit(who, func(p *Progress) { p.Stage, p.Note = StageSetting, "Setting the type" })

	// Everything that reached the model counts as shown, whether or not the
	// brief ended up mentioning it: the reader was given the chance to care.
	kept := []Item{}
	for _, r := range results {
		kept = append(kept, r.Items...)
	}
	_ = store.NoteAttention(func(l Ledger) { l.Saw(kept, now) })

	data, remember := decorate(written.JSON, now, results, painting)
	if len(remember) > 0 {
		_ = store.Remember(remember, DayKey(now))
	}

	spent := append(usage, written.Usage)
	_ = store.NoteUsage(spent, now)
	brief = &Brief{
		ID: DayKey(now), CreatedAt: now, Trigger: trigger, Data: data,
		Painting: painting, Model: written.Model, DowngradedFrom: written.DowngradedFrom,
		Sources: reports(results), Usage: spent,
	}
	return brief, store.SaveBrief(brief)
}

// Gathered is what one read of the sources came to.
type Gathered struct {
	Fresh   int            `json:"fresh"`   // items never seen before, or grown
	Stored  int            `json:"stored"`  // items in the store afterwards
	Owned   []string       `json:"owned"`   // what the reader owns, ranked
	Reports []SourceReport `json:"reports"` // how each source fared
	Took    time.Duration  `json:"took"`
}

// Refresh reads the sources into the store without writing a brief, for the
// page's "read again" and for anyone who wants the store warm before asking
// for a brief.
func (wr *Writer) Refresh(ctx context.Context, store *UserStore) (got *Gathered, err error) {
	wr.mu.Lock()
	defer wr.mu.Unlock()

	who := store.ID()
	wr.board.start(who, "refresh")
	defer func() {
		wr.board.edit(who, func(p *Progress) {
			if err != nil {
				p.Stage, p.Error, p.Note = StageFailed, err.Error(), "Could not read the sources"
				return
			}
			p.Stage = StageDone
			p.Note = fmt.Sprintf("%d new, %d kept, %ds", got.Fresh, got.Stored, int(got.Took.Seconds()))
		})
	}()

	cfg := store.Config()
	now := cfg.Now()
	past, _ := store.Briefs()
	window := windowFor(past, cfg, now)
	started := time.Now()

	sig, own, err := wr.gather(ctx, store, cfg, window, watcher{wr.board, who})
	if err != nil {
		return nil, err
	}
	fresh := 0
	for _, it := range sig.Items {
		if !it.LastSeen.Before(now) && (it.FirstSeen.Equal(it.LastSeen) || it.Updates > 0) {
			fresh++
		}
	}
	return &Gathered{
		Fresh: fresh, Stored: len(sig.Items), Owned: own.Ranked(12),
		Reports: sig.Reports, Took: time.Since(started),
	}, nil
}

// gather is the read: sources into the store, ownership recomputed from what
// the store now knows, both saved. The order matters. The sweep reads owned
// rooms first, so it uses yesterday's ownership; then ownership is recomputed
// so that a room first spoken in today is owned tomorrow.
func (wr *Writer) gather(ctx context.Context, store *UserStore, cfg *Config, window Window, watch Watcher) (*SignalStore, Ownership, error) {
	now := window.Now
	sig := store.Signals()
	prior := store.Ownership()
	ledger := store.Attention()

	faded := map[string]bool{}
	for _, origin := range ledger.Faded() {
		faded[origin] = true
	}
	window.Skip = func(origin string) bool {
		return muted(origin, cfg.Mutes) || faded[normaliseOrigin(origin)]
	}
	window.Owned = prior.Owns
	window.Cursors = sig.CursorSet()
	window.Signals = sig

	results := CollectAll(ctx, cfg, window, watch)

	present := map[string]bool{}
	for _, r := range results {
		if !r.OK {
			continue
		}
		sig.Merge(r.Items, now)
		if r.ID == "calendar" {
			sig.Events = r.Events
		}
		for _, it := range r.Items {
			present[it.Key] = true
		}
	}
	// An open pull request that stopped coming back was merged or closed.
	for _, r := range results {
		if r.ID == "github" && r.OK {
			sig.Resolved("github", []string{"github.review_requested", "github.assigned", "github.my_open_pr"}, present, now)
		}
	}
	sig.Reports = reports(results)
	sig.RefreshedAt = now
	sig.Prune(now)

	own := InferOwnership(ctx, cfg, sig, prior, now)

	// Something shipped in a repository they own that the matching room has
	// not heard about. A rule, not a judgement, and the strongest push there
	// is when it fires.
	if s := cfg.Sources["github"]; s["enabled"] == "true" && s["token"] != "" {
		releases := []Release{}
		for _, repo := range own.Ranked(20) {
			if own[repo].Kind == "repo" {
				releases = append(releases, githubReleases(ctx, s["token"], repo)...)
			}
		}
		shipped := unannounced(releases,
			func(repo string) []string { return roomsForRepo(own, repo) },
			func(room string) string { return roomSaid(sig, room) }, now)
		for i := range shipped {
			shipped[i].Source = "github"
			shipped[i].Key = keyFor("github", shipped[i])
		}
		sig.Merge(shipped, now)
	}

	if err := store.SaveSignals(sig); err != nil {
		return nil, nil, fmt.Errorf("could not keep what was gathered: %w", err)
	}
	if err := store.SaveOwnership(own); err != nil {
		return nil, nil, fmt.Errorf("could not keep the ownership map: %w", err)
	}
	return sig, own, nil
}

// windowFor decides how far back this morning reads.
//
// A fixed window makes every brief re-read the days before it. Yesterday's
// brief already covered yesterday, so today opens on the same messages, and
// the model spends its effort not repeating itself rather than telling you
// anything: on the second morning that left one to-do out of eight signals.
//
// So the window starts where the last brief stopped. Nothing is covered twice
// and nothing falls down the gap between two mornings.
func windowFor(past []*Brief, cfg *Config, now time.Time) Window {
	// The configured lookback becomes the furthest back it will ever reach, so
	// coming back after a fortnight away does not pull a fortnight of Slack.
	reach := now.Add(-time.Duration(cfg.Lookback) * time.Hour)

	for _, b := range past {
		// Today's own brief is not an anchor: rewriting this morning should
		// cover the same ground as the first attempt, not a sliver of it.
		if b.ID == DayKey(now) || b.CreatedAt.IsZero() {
			continue
		}
		if b.CreatedAt.After(reach) {
			return Window{Now: now, Since: b.CreatedAt}
		}
		break // briefs are newest first, so an older one reaches no further
	}
	return Window{Now: now, Since: reach}
}

// decorate stitches in the things the server knows and the model shouldn't
// guess: the timestamp, the plate, and which sources actually answered.
func decorate(raw json.RawMessage, now time.Time, results []*SourceResult, painting *Painting) (json.RawMessage, []string) {
	var data map[string]any
	if json.Unmarshal(raw, &data) != nil {
		return raw, nil
	}

	// Stamp each entry with the place it came from. The model is never asked
	// for it: it is looked up from the url it copied, so the page can offer to
	// bury a room and the ledger can tell which room was ignored.
	where := map[string]string{}
	for _, r := range results {
		for _, it := range r.Items {
			if it.URL != "" && it.Origin != "" {
				where[it.URL] = it.Origin
			}
		}
	}
	for _, section := range []string{"top_todos", "new_updates"} {
		list, _ := data[section].([]any)
		for _, entry := range list {
			row, _ := entry.(map[string]any)
			if row == nil {
				continue
			}
			link, _ := row["source_url"].(string)
			origin := where[link]
			if origin == "" {
				origin = originFromURL(link)
			}
			// An update names its room in "where" even when its url was to a
			// document rather than a message; that room is still the origin.
			if origin == "" {
				if room, _ := row["where"].(string); strings.HasPrefix(room, "#") {
					origin = room
				}
			}
			if origin != "" {
				row["origin"] = origin
			}
		}
	}

	header, _ := data["header"].(map[string]any)
	if header == nil {
		header = map[string]any{}
	}
	header["date_time"] = now.Format(time.RFC3339)
	data["header"] = header

	if painting != nil {
		data["painting"] = map[string]any{"caption": painting.Caption}
	}

	sources := []any{}
	for _, r := range results {
		if r.OK && len(r.Items) > 0 {
			sources = append(sources, map[string]any{
				"name": r.Name, "url": sourceHome(r.ID), "source_icon_key": r.IconKey,
			})
		}
	}
	data["footer"] = map[string]any{"sources": sources}

	remember := []string{}
	if list, isList := data["remember"].([]any); isList {
		for _, entry := range list {
			if text, isText := entry.(string); isText {
				remember = append(remember, text)
			}
		}
	}

	out, err := json.Marshal(data)
	if err != nil {
		return raw, remember
	}
	return out, remember
}

func reports(results []*SourceResult) []SourceReport {
	out := []SourceReport{}
	for _, r := range results {
		out = append(out, SourceReport{ID: r.ID, Name: r.Name, OK: r.OK, Count: len(r.Items), Error: r.Err})
	}
	return out
}

var homes = map[string]string{
	"slack":    "https://app.slack.com/client",
	"github":   "https://github.com/notifications",
	"linear":   "https://linear.app",
	"calendar": "https://calendar.google.com",
}

func sourceHome(id string) string { return homes[id] }

func plural2(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// history is the last few mornings, with what got ticked off, so the brief can
// tell what you cleared from what you have been carrying all week.
func history(store *UserStore, days int) []DayHistory {
	briefs, err := store.Briefs()
	if err != nil {
		return nil
	}
	return historyFrom(briefs, days, DayKey(store.Config().Now()))
}

// historyFrom reads the last few mornings. Today's own brief is not one of
// them: rewriting this morning should stand on yesterday, not on the attempt
// it is replacing, or the brief starts quoting itself back.
func historyFrom(briefs []*Brief, days int, today string) []DayHistory {
	out := []DayHistory{}
	for _, b := range briefs {
		if len(out) >= days {
			break
		}
		if b.ID == today {
			continue
		}
		done := map[int]bool{}
		for _, index := range b.Done {
			done[index] = true
		}

		var parsed struct {
			PushForward struct {
				Title string `json:"title"`
			} `json:"push_forward"`
			TopTodos []struct {
				Title     string `json:"title"`
				Body      string `json:"body"`
				SourceURL string `json:"source_url"`
				Due       string `json:"due"`
			} `json:"top_todos"`
		}
		if json.Unmarshal(b.Data, &parsed) != nil {
			continue
		}

		day := DayHistory{ID: b.ID, Pushed: parsed.PushForward.Title}
		for index, todo := range parsed.TopTodos {
			day.Todos = append(day.Todos, PastTodo{
				Title: todo.Title, Body: todo.Body,
				Source: todo.SourceURL, Due: todo.Due, Done: done[index],
				Raised: b.CreatedAt, Shown: 1,
			})
		}
		out = append(out, day)
	}
	mergeCarried(out)
	return out
}

// mergeCarried folds a to-do that several mornings repeated into one, under
// the morning that first raised it. Without this every brief re-raised the
// Team Syncs deck with a fresh date, so it never aged and was never let go:
// the third morning's copy was one morning old. The oldest copy keeps the
// day it was asked on; the newest copy's words, date and tick win, because
// they are the latest the reader saw.
func mergeCarried(days []DayHistory) {
	keyOf := func(t PastTodo) string {
		if src := strings.TrimRight(strings.ToLower(t.Source), "/"); src != "" {
			return src
		}
		return "title:" + strings.ToLower(strings.TrimSpace(t.Title))
	}
	first := map[string]*PastTodo{}
	for i := len(days) - 1; i >= 0; i-- { // oldest morning first
		kept := days[i].Todos[:0]
		for _, todo := range days[i].Todos {
			key := keyOf(todo)
			if older := first[key]; older != nil {
				older.Title, older.Body = todo.Title, todo.Body
				if todo.Due != "" {
					older.Due = todo.Due
				}
				older.Done = older.Done || todo.Done
				older.Shown++
				continue
			}
			kept = append(kept, todo)
		}
		days[i].Todos = kept
		for j := range days[i].Todos {
			first[keyOf(days[i].Todos[j])] = &days[i].Todos[j]
		}
	}
}

// ── After the write ─────────────────────────────────────

// dedupePush blanks the push when it is a to-do restated. The prompt says not
// to; the model does it anyway about one morning in three, and a brief whose
// headline is its own first bullet has not read itself.
func dedupePush(raw json.RawMessage) json.RawMessage {
	var data map[string]any
	if json.Unmarshal(raw, &data) != nil {
		return raw
	}
	push, _ := data["push_forward"].(map[string]any)
	todos, _ := data["top_todos"].([]any)
	if push == nil || len(todos) == 0 {
		return raw
	}
	title, _ := push["title"].(string)
	link, _ := push["source_url"].(string)
	if strings.TrimSpace(title) == "" {
		return raw
	}

	for _, entry := range todos {
		todo, _ := entry.(map[string]any)
		if todo == nil {
			continue
		}
		todoLink, _ := todo["source_url"].(string)
		todoTitle, _ := todo["title"].(string)
		sameLink := link != "" && todoLink != "" && strings.TrimRight(link, "/") == strings.TrimRight(todoLink, "/")
		if sameLink || similarTitles(title, todoTitle) {
			data["push_forward"] = map[string]any{"title": "", "body": "", "cta_prompt": "", "source_url": "", "draft": ""}
			break
		}
	}
	out, err := json.Marshal(data)
	if err != nil {
		return raw
	}
	return out
}

// similarTitles says whether two titles are the same ask in different words:
// enough shared words, once the small ones are set aside.
func similarTitles(a, b string) bool {
	wa, wb := titleWords(a), titleWords(b)
	if len(wa) == 0 || len(wb) == 0 {
		return false
	}
	shared := 0
	for w := range wa {
		if wb[w] {
			shared++
		}
	}
	union := len(wa) + len(wb) - shared
	return float64(shared)/float64(union) >= 0.6
}

var smallWords = map[string]bool{
	"the": true, "a": true, "an": true, "to": true, "of": true, "in": true, "on": true, "for": true,
	"and": true, "or": true, "your": true, "you": true, "it": true, "its": true, "with": true, "at": true,
	"by": true, "is": true, "are": true, "be": true, "this": true, "that": true, "up": true, "out": true,
}

func titleWords(title string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(title), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '#' || r == '/' || r == '.' || r == '-')
	}) {
		if len(w) > 2 && !smallWords[w] {
			out[w] = true
		}
	}
	return out
}

// backfillDue gives a to-do the due date the store knows for its source, when
// the model left it blank. The Tuesday deck was still listed on Thursday
// because the writer forgot to set due; the store had the date all along.
func backfillDue(raw json.RawMessage, sig *SignalStore, carried ...*PastTodo) json.RawMessage {
	var data map[string]any
	if json.Unmarshal(raw, &data) != nil {
		return raw
	}
	todos, _ := data["top_todos"].([]any)
	byURL := map[string]string{}
	if sig != nil {
		for _, it := range sig.Items {
			if it.Triage != nil && it.Triage.Due != "" && it.URL != "" {
				byURL[strings.TrimRight(strings.ToLower(it.URL), "/")] = it.Triage.Due
			}
		}
	}
	for _, todo := range carried {
		if todo == nil {
			continue
		}
		key := strings.TrimRight(strings.ToLower(todo.Source), "/")
		if todo.Due != "" && key != "" {
			byURL[key] = todo.Due
		}
	}
	changed := false
	for _, entry := range todos {
		todo, _ := entry.(map[string]any)
		if todo == nil {
			continue
		}
		link, _ := todo["source_url"].(string)
		key := strings.TrimRight(strings.ToLower(link), "/")
		if due, _ := todo["due"].(string); due != "" {
			continue
		}
		if due := byURL[key]; due != "" {
			todo["due"] = due
			changed = true
		}
	}
	if !changed {
		return raw
	}
	out, err := json.Marshal(data)
	if err != nil {
		return raw
	}
	return out
}
