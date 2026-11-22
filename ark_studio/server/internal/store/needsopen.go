package store

import "strings"

// The one definition of what waits in Needs you. Every reader of the list
// (needsYou, NeedsCards and the automatic groups built from its rows, the
// needs:* tag backfill and needsPending) goes through these helpers, and the
// web client mirrors them in web/js/needs-model.js (isOpenDecision).

// ClosedStatuses are the statuses of KnownStatuses that end a unit: done,
// abandoned (dropped) and archived. Every other status is open.
var ClosedStatuses = []string{"done", "abandoned", "archived"}

// IsClosedStatus reports whether status is one of ClosedStatuses.
func IsClosedStatus(status string) bool { return containsString(ClosedStatuses, status) }

// IsOpenDecision reports whether a unit of this type and status is a decision
// still waiting for an answer.
func IsOpenDecision(typeName, status string) bool {
	return typeName == "decision" && !IsClosedStatus(status)
}

// closedStatusSQL is ClosedStatuses as an SQL list: 'done','abandoned','archived'.
func closedStatusSQL() string {
	quoted := make([]string, len(ClosedStatuses))
	for i, s := range ClosedStatuses {
		quoted[i] = "'" + s + "'"
	}
	return strings.Join(quoted, ",")
}

// needsYouCond is the SQL form of needsPending over the units table (optional
// alias prefix such as "u."): not in the recycle bin, and in review or an open
// decision.
func needsYouCond(prefix string) string {
	return prefix + "archived=0 AND (" + prefix + "status='review' OR (" + prefix + "type='decision' AND " +
		prefix + "status NOT IN (" + closedStatusSQL() + ")))"
}
