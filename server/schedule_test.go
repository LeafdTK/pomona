package main

import (
	"testing"
	"time"
)

// A failed morning used to retry every minute for the rest of the day.
func TestBackoffDoublesAndCaps(t *testing.T) {
	cases := map[int]time.Duration{
		0: 0, 1: 5 * time.Minute, 2: 10 * time.Minute, 3: 20 * time.Minute,
		4: 40 * time.Minute, 5: time.Hour, 9: time.Hour, 40: time.Hour,
	}
	for fails, want := range cases {
		if got := backoff(fails); got != want {
			t.Errorf("backoff(%d) = %v, want %v", fails, got, want)
		}
	}
}

// 07:30 for someone in Tokyo is 22:30 UTC the day before. The scheduler used
// to read the process clock, so a server in one place wrote for a person in
// another at the wrong hour and under the wrong date.
func TestDueNowHonoursTheReadersTimezone(t *testing.T) {
	cfg := defaultConfig()
	cfg.Profile.Timezone = "Asia/Tokyo"
	cfg.Schedule.Time = "07:30"

	// 22:00 UTC on the 9th is 07:00 Tokyo on the 10th: not yet.
	early := time.Date(2026, 9, 9, 22, 0, 0, 0, time.UTC)
	if fire, today := dueNow(cfg, early, ""); fire || today != "2026-09-10" {
		t.Errorf("at 07:00 Tokyo: fire=%v today=%s, want false / 2026-09-10", fire, today)
	}
	// 22:30 UTC is 07:30 Tokyo: now.
	due := time.Date(2026, 9, 9, 22, 30, 0, 0, time.UTC)
	if fire, today := dueNow(cfg, due, ""); !fire || today != "2026-09-10" {
		t.Errorf("at 07:30 Tokyo: fire=%v today=%s, want true / 2026-09-10", fire, today)
	}
	// Already written today: never twice.
	if fire, _ := dueNow(cfg, due, "2026-09-10"); fire {
		t.Error("fired twice on the same day")
	}
}

func TestDueNowRespectsTheSwitches(t *testing.T) {
	cfg := defaultConfig()
	cfg.Profile.Timezone = "UTC"
	saturday := time.Date(2026, 9, 12, 9, 0, 0, 0, time.UTC)

	cfg.Schedule.WeekdaysOnly = true
	if fire, _ := dueNow(cfg, saturday, ""); fire {
		t.Error("fired on a Saturday with weekdays only")
	}
	cfg.Schedule.WeekdaysOnly = false
	if fire, _ := dueNow(cfg, saturday, ""); !fire {
		t.Error("did not fire on a Saturday without the switch")
	}
	cfg.Schedule.Enabled = false
	if fire, _ := dueNow(cfg, saturday, ""); fire {
		t.Error("fired while disabled")
	}
	cfg.Schedule.Enabled = true
	cfg.Schedule.Time = "not a time"
	if fire, _ := dueNow(cfg, saturday, ""); fire {
		t.Error("fired on an unparseable time")
	}
}

func TestLocationFallsBackToLocal(t *testing.T) {
	cfg := defaultConfig()
	if cfg.Location() != time.Local {
		t.Error("blank timezone should be the process's")
	}
	cfg.Profile.Timezone = "Not/AZone"
	if cfg.Location() != time.Local {
		t.Error("a bad timezone should fall back rather than fail")
	}
	cfg.Profile.Timezone = "America/Mexico_City"
	if cfg.Location().String() != "America/Mexico_City" {
		t.Errorf("got %s", cfg.Location())
	}
}
