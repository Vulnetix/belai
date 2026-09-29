package tui

import (
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/tui/components"
)

// rmMeta is the presentation metadata of a live activity: who decided, the
// category, the icon and the cause. Every field is derived by the
// rolemanager package from the event and the model identity.
func rmMeta(act rolemanager.Activity) components.RMMeta { return components.RMMetaOf(act) }

// rmMetaFromEntry restores the presentation metadata of a persisted row. An
// entry written before schema 2 has none of the keys; it gets the icon and
// category its event maps to and no decider tag.
func rmMetaFromEntry(meta map[string]any, tone rolemanager.Tone) components.RMMeta {
	ev := rolemanager.Event(metaString(meta, "activity"))
	m := components.RMMeta{
		ActorKind: metaString(meta, "actor_kind"),
		Actor:     metaString(meta, "actor"),
		Category:  metaString(meta, "category"),
		Icon:      metaString(meta, "icon"),
		Cause:     metaString(meta, "cause"),
		Outcome:   string(tone.Kind()),
	}
	if m.Category == "" && ev != "" {
		m.Category = string(rolemanager.CategoryOf(ev))
	}
	if m.Icon == "" && ev != "" {
		m.Icon = rolemanager.IconOf(ev)
	}
	if s, ok := metaInt(meta, "score_pct"); ok {
		m.Score, m.HasScore = s, true
	}
	return m
}
