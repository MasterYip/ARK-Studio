package store

// Go only: an agent that hands work to review must say what to check
// (agent rules C5, docs/agents/working-mode.md "Review requests").
//
// A write by an agent ("agent:..." actor: agent API, personal and session
// tokens) that puts a unit into review, or submits / resubmits a review
// group, needs a review_note of at least MinReviewNote characters; for a
// unit whose body has headings (or {#id} blocks) the note needs at least
// one anchor.  Otherwise the write fails with ReviewNoteError (HTTP 422
// REVIEW_NOTE_REQUIRED) and nothing is written.  People ("human:..."), the
// in-app assistant and seeds are not checked.

import (
	"fmt"
	"strconv"
	"strings"

	"arkstudio/go/internal/compat/pytext"
)

// MinReviewNote is the shortest review_note an agent may hand off with.
const MinReviewNote = 20

// ReviewNoteErrorCode is the API error code of ReviewNoteError.
const ReviewNoteErrorCode = "REVIEW_NOTE_REQUIRED"

// ReviewNoteError: an agent handed work to review without a usable note.
type ReviewNoteError struct{ Message string }

func (e *ReviewNoteError) Error() string { return e.Message }

// IsAgentActor: a write made by an agent (agent API, personal or session token).
func IsAgentActor(actor string) bool { return strings.HasPrefix(actor, "agent:") }

const reviewNoteHowTo = "Write one to three sentences (%d-%d characters) saying what the person should check and what changed, " +
	"with at least one anchor as a Markdown link: [text](#heading-slug) for a heading of the unit body, " +
	"[clip](media:421@0:08) for an attachment, [T-0042](T-0042#slug) for another unit."

const reviewResubmitHowTo = " This is a resubmission after a send-back: say what you fixed for each comment."

// reviewNoteProblem says what is wrong with an agent's note ("" = fine).
// needAnchor: the note must carry at least one anchor.
func reviewNoteProblem(note string, needAnchor bool, anchorWhy string) string {
	n := pytext.Len(strings.TrimSpace(note))
	switch {
	case n == 0:
		return "the review_note is missing or empty"
	case n < MinReviewNote:
		return "the review_note has " + strconv.Itoa(n) + " characters, write at least " + strconv.Itoa(MinReviewNote)
	case needAnchor && len(ParseAnchors(note)) == 0:
		return "the review_note has no anchor, and " + anchorWhy
	}
	return ""
}

func howTo() string {
	return fmt.Sprintf(reviewNoteHowTo, MinReviewNote, MaxReviewNote)
}

// checkAgentUnitReviewNote is the check for a unit an agent puts into
// review (label: its uid or title; resubmit: it was sent back before).
func checkAgentUnitReviewNote(label, note, body string, resubmit bool) error {
	headings := BodyAnchors(body)
	problem := reviewNoteProblem(note, len(headings) > 0, "this unit's body has headings")
	if problem == "" {
		return nil
	}
	example := "Check [Results](#results): the timeout is now 10 s and the error table is new."
	if len(headings) > 0 {
		example = "Check [" + headings[0] + "](#" + headings[0] + "): <what changed and what to look at>."
	}
	var b strings.Builder
	b.WriteString(ReviewNoteErrorCode + ": " + label + " cannot go to review without a review note: " + problem + ". ")
	b.WriteString(`Send it in the same request as the status, e.g. {"status": "review", "review_note": "` + example + `"}. `)
	b.WriteString(howTo())
	if len(headings) > 0 {
		list := headings
		if len(list) > 8 {
			list = list[:8]
		}
		b.WriteString(" Headings in this body: #" + strings.Join(list, ", #") + ".")
	}
	if resubmit {
		b.WriteString(reviewResubmitHowTo)
	}
	return &ReviewNoteError{Message: b.String()}
}

// checkAgentGroupReviewNote is the check for a review group an agent
// submits or resubmits.  An anchor in a member note counts too.
func checkAgentGroupReviewNote(note string, memberNotes []string, resubmit bool, previous string) error {
	problem := reviewNoteProblem(note, false, "")
	if problem == "" {
		anchored := len(ParseAnchors(note)) > 0
		for _, m := range memberNotes {
			anchored = anchored || len(ParseAnchors(m)) > 0
		}
		if !anchored {
			problem = "neither the review_note nor a member note has an anchor"
		} else if resubmit && strings.TrimSpace(note) == strings.TrimSpace(previous) {
			problem = "the review_note repeats the one of the previous round"
		}
	}
	if problem == "" {
		return nil
	}
	var b strings.Builder
	b.WriteString(ReviewNoteErrorCode + ": a review group cannot be ")
	if resubmit {
		b.WriteString("resubmitted")
	} else {
		b.WriteString("submitted")
	}
	b.WriteString(" without a review note: " + problem + ". ")
	b.WriteString(`Send it with the group, e.g. {"review_note": "Fixed both comments: the legend is in ` +
		`[T-0042](T-0042#legend), the clip is trimmed in [clip](media:421@0:08)."}. `)
	b.WriteString(howTo())
	if resubmit {
		b.WriteString(reviewResubmitHowTo)
	}
	return &ReviewNoteError{Message: b.String()}
}
