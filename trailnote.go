package main

// trailnote.go — trail status lines are not in the transcript, so a re-render
// (which wipes the screen and reprints the transcript) would lose them. Each
// printed line is remembered with the transcript byte offset it was printed
// at, and rerender puts it back after the line ending at or past that offset.
// Offsets, not timestamps: they are exact, and a note only belongs to the file
// it was printed against, so a lineage rollover never replays the old file's
// notes into the new one.

type trailNoteRec struct {
	Path   string
	Offset int64
	Text   string
	Sev    trailSev
}

// notesUpTo returns the notes for path at or before off, scanning from index
// from, and the index to continue from. notes are in print order, so offsets
// for one path only grow.
func notesUpTo(notes []trailNoteRec, path string, off int64, from int) (due []trailNoteRec, next int) {
	i := from
	for ; i < len(notes); i++ {
		n := notes[i]
		if n.Path != path {
			continue
		}
		if n.Offset > off {
			break
		}
		due = append(due, n)
	}
	return due, i
}
