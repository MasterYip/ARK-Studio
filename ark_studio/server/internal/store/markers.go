package store

import (
	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/store/markers"
)

// Node marker rules (Go only, internal/store/markers): every unit write applies
// the project's exclusive marker groups to the unit's tags, so a node holds
// at most one star, one flag, one priority, ... and the marker written last
// wins.  An update that leaves the tags alone still runs the rule on the
// stored tags, which repairs units that got several markers of one group
// before the rule existed (they keep the one that sorts last).

// MarkerGroups are the project's marker groups (the setting, or the
// defaults).
func (s *Store) MarkerGroups() []markers.Group {
	if s.Manifest == nil {
		return markers.Defaults()
	}
	settings, _ := s.Manifest.Get("settings").(*pyjson.Object)
	return markers.Effective(settings.Value("markers"))
}

func (s *Store) resolveMarkers(tags []any) []any {
	return markers.Resolve(tags, func(raw any) string {
		ns, value, err := splitTag(raw)
		if err != nil {
			return ""
		}
		return ns + ":" + value
	}, s.MarkerGroups())
}
