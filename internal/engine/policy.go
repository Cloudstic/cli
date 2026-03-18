package engine

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cloudstic/cli/internal/core"
)

// ForgetPolicy describes which snapshots to keep.
type ForgetPolicy struct {
	KeepLast    int
	KeepHourly  int
	KeepDaily   int
	KeepWeekly  int
	KeepMonthly int
	KeepYearly  int
}

func (p ForgetPolicy) IsEmpty() bool {
	return p == ForgetPolicy{}
}

func (p ForgetPolicy) String() string {
	var parts []string
	if p.KeepLast > 0 {
		parts = append(parts, fmt.Sprintf("keep %d last", p.KeepLast))
	}
	if p.KeepHourly > 0 {
		parts = append(parts, fmt.Sprintf("keep %d hourly", p.KeepHourly))
	}
	if p.KeepDaily > 0 {
		parts = append(parts, fmt.Sprintf("keep %d daily", p.KeepDaily))
	}
	if p.KeepWeekly > 0 {
		parts = append(parts, fmt.Sprintf("keep %d weekly", p.KeepWeekly))
	}
	if p.KeepMonthly > 0 {
		parts = append(parts, fmt.Sprintf("keep %d monthly", p.KeepMonthly))
	}
	if p.KeepYearly > 0 {
		parts = append(parts, fmt.Sprintf("keep %d yearly", p.KeepYearly))
	}
	return strings.Join(parts, ", ")
}

// ---------------------------------------------------------------------------
// Snapshot entries
// ---------------------------------------------------------------------------

// SnapshotEntry is a snapshot loaded for policy evaluation.
type SnapshotEntry struct {
	Ref     string
	Snap    core.Snapshot
	Created time.Time
}

// KeepReason pairs a snapshot with the reasons it was kept.
type KeepReason struct {
	Entry   SnapshotEntry
	Reasons []string
}

// ---------------------------------------------------------------------------
// Grouping
// ---------------------------------------------------------------------------

// GroupKey identifies a group of snapshots for policy application.
type GroupKey struct {
	Source  string
	Account string
	Path    string
	Tags    string // sorted, comma-joined
}

func (k GroupKey) String() string {
	var parts []string
	if k.Source != "" {
		parts = append(parts, "source:"+k.Source)
	}
	if k.Account != "" {
		parts = append(parts, "account:"+k.Account)
	}
	if k.Path != "" {
		parts = append(parts, "path:"+k.Path)
	}
	if k.Tags != "" {
		parts = append(parts, "tags:"+k.Tags)
	}
	if len(parts) == 0 {
		return "(all)"
	}
	return strings.Join(parts, ", ")
}

type groupFields struct {
	source  bool
	account bool
	path    bool
	tags    bool
}

func defaultGroupFields() groupFields {
	return groupFields{source: true, account: true, path: true}
}

func parseGroupBy(s string) groupFields {
	if s == "" {
		return groupFields{}
	}
	g := groupFields{}
	for _, f := range strings.Split(s, ",") {
		switch strings.TrimSpace(f) {
		case "source":
			g.source = true
		case "account":
			g.account = true
		case "path":
			g.path = true
		case "tags":
			g.tags = true
		}
	}
	return g
}

func makeGroupKey(snap *core.Snapshot, gf groupFields) GroupKey {
	var k GroupKey
	if snap.Source != nil {
		if gf.source {
			k.Source = snap.Source.Type
		}
		// Prefer new identity fields, then legacy volume UUID, then account/path.
		if gf.account {
			switch {
			case snap.Source.Identity != "":
				k.Account = snap.Source.Identity
			default:
				k.Account = snap.Source.Account
			}
		}
		if gf.path {
			if snap.Source.PathID != "" {
				k.Path = snap.Source.PathID
			} else {
				k.Path = snap.Source.Path
			}
		}
	}
	if gf.tags && len(snap.Tags) > 0 {
		tags := make([]string, len(snap.Tags))
		copy(tags, snap.Tags)
		sort.Strings(tags)
		k.Tags = strings.Join(tags, ",")
	}
	return k
}

func groupSnapshots(entries []SnapshotEntry, gf groupFields) map[GroupKey][]SnapshotEntry {
	groups := make(map[GroupKey][]SnapshotEntry)
	for _, e := range entries {
		key := makeGroupKey(&e.Snap, gf)
		groups[key] = append(groups[key], e)
	}
	return groups
}

// ---------------------------------------------------------------------------
// Filtering
// ---------------------------------------------------------------------------

type snapshotFilter struct {
	tags    []string
	source  string
	account string
	path    string
}

func (f snapshotFilter) IsEmpty() bool {
	return f.source == "" && f.account == "" && f.path == "" && len(f.tags) == 0
}

func matchesFilter(snap *core.Snapshot, f snapshotFilter) bool {
	if f.source != "" && (snap.Source == nil || snap.Source.Type != f.source) {
		return false
	}
	if f.account != "" {
		if snap.Source == nil {
			return false
		}
		// Accept display account and identity fields for compatibility.
		if snap.Source.Account != f.account &&
			snap.Source.Identity != f.account {
			return false
		}
	}
	if f.path != "" {
		if snap.Source == nil {
			return false
		}
		if snap.Source.PathID != "" {
			if snap.Source.PathID != f.path && snap.Source.Path != f.path {
				return false
			}
		} else if snap.Source.Path != f.path {
			return false
		}
	}
	if len(f.tags) > 0 {
		tagSet := make(map[string]bool, len(snap.Tags))
		for _, t := range snap.Tags {
			tagSet[t] = true
		}
		for _, ft := range f.tags {
			if !tagSet[ft] {
				return false
			}
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Policy evaluation
// ---------------------------------------------------------------------------

// applyPolicy evaluates the policy against a list of snapshots and returns
// which to keep (with reasons) and which to remove. Snapshots are ORed across
// all keep-* rules: matching any single rule is enough to be kept.
func applyPolicy(entries []SnapshotEntry, policy ForgetPolicy) (keep []KeepReason, remove []SnapshotEntry) {
	if len(entries) == 0 {
		return nil, nil
	}

	// Sort newest first.
	sorted := make([]SnapshotEntry, len(entries))
	copy(sorted, entries)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Created.After(sorted[j].Created)
	})

	kept := make(map[string][]string) // ref -> reasons

	// keep-last
	if policy.KeepLast > 0 {
		for i, e := range sorted {
			if i >= policy.KeepLast {
				break
			}
			kept[e.Ref] = append(kept[e.Ref], "last")
		}
	}

	// Time-bucket based policies: for each rule, walk snapshots newest-first,
	// track unique buckets, keep the first snapshot in each bucket up to N.
	type bucketRule struct {
		name  string
		count int
		key   func(time.Time) string
	}

	rules := []bucketRule{
		{"hourly snapshot", policy.KeepHourly, func(t time.Time) string {
			return t.Format("2006-01-02 15")
		}},
		{"daily snapshot", policy.KeepDaily, func(t time.Time) string {
			return t.Format("2006-01-02")
		}},
		{"weekly snapshot", policy.KeepWeekly, func(t time.Time) string {
			y, w := t.ISOWeek()
			return fmt.Sprintf("%04d-W%02d", y, w)
		}},
		{"monthly snapshot", policy.KeepMonthly, func(t time.Time) string {
			return t.Format("2006-01")
		}},
		{"yearly snapshot", policy.KeepYearly, func(t time.Time) string {
			return t.Format("2006")
		}},
	}

	for _, rule := range rules {
		if rule.count <= 0 {
			continue
		}
		seen := 0
		lastBucket := ""
		for _, e := range sorted {
			bucket := rule.key(e.Created)
			if bucket == lastBucket {
				continue
			}
			lastBucket = bucket
			seen++
			if seen > rule.count {
				break
			}
			kept[e.Ref] = append(kept[e.Ref], rule.name)
		}
	}

	// Partition into keep/remove preserving newest-first order.
	for _, e := range sorted {
		if reasons, ok := kept[e.Ref]; ok {
			keep = append(keep, KeepReason{Entry: e, Reasons: reasons})
		} else {
			remove = append(remove, e)
		}
	}

	return keep, remove
}
