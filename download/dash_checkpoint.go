package download

// A checkpoint identifies the complete plan and each fully synchronized
// segment, never its URL or authorization. Offsets are local to one track.
type dashTrackCheckpoint struct {
	Playlist  string           `json:"playlist,omitempty"`
	Completed []hlsCheckpoint  `json:"completed,omitempty"`
	Splice    *dashSpliceState `json:"splice,omitempty"`
}

// Period files have independent plans and offsets; neither URLs nor credentials
// are stored in their journals.
type dashPeriodCheckpoint struct {
	Video dashTrackCheckpoint `json:"video,omitempty"`
	Audio dashTrackCheckpoint `json:"audio,omitempty"`
}

// dashSpliceState 是多周期缝合状态的检查点投影。
type dashSpliceState struct {
	FingerprintVersion int    `json:"fingerprint_version,omitempty"`
	Period             int    `json:"period"`
	Shift              uint64 `json:"shift,omitempty"`
	ShiftActive        bool   `json:"shift_active,omitempty"`
	PendingShift       bool   `json:"pending_shift,omitempty"`
	LastEnd            uint64 `json:"last_end,omitempty"`
	InitDigest         string `json:"init_digest,omitempty"`
	InitStructure      string `json:"init_structure,omitempty"`
}

// restoreSpliceState 清洗缝合状态；非法即弃用整块。
func restoreSpliceState(splice *dashSpliceState) *dashSpliceState {
	if splice == nil || splice.Period < 0 || splice.Period >= maxMediaPeriods ||
		(splice.InitDigest != "" && !validFingerprint(splice.InitDigest)) ||
		(splice.InitStructure != "" && !validFingerprint(splice.InitStructure)) {
		return nil
	}
	return &dashSpliceState{
		FingerprintVersion: splice.FingerprintVersion,
		Period:             splice.Period,
		Shift:              splice.Shift,
		ShiftActive:        splice.ShiftActive,
		PendingShift:       splice.PendingShift,
		LastEnd:            splice.LastEnd,
		InitDigest:         splice.InitDigest,
		InitStructure:      splice.InitStructure,
	}
}

func cloneDASHCheckpoint(value dashTrackCheckpoint) dashTrackCheckpoint {
	result := dashTrackCheckpoint{Playlist: value.Playlist, Splice: value.Splice}
	for _, item := range value.Completed {
		item.URL = ""
		item.LiveKey = ""
		result.Completed = append(result.Completed, item)
	}
	return result
}

// Treat malformed or partial journal data as unusable rather than guessing
// which bytes belong to a representation. A valid prefix may still be reused.
func restoreDASHCheckpoint(value dashTrackCheckpoint) dashTrackCheckpoint {
	if !validFingerprint(value.Playlist) {
		return dashTrackCheckpoint{}
	}
	result := dashTrackCheckpoint{Playlist: value.Playlist}
	result.Splice = restoreSpliceState(value.Splice)
	var end int64
	for index, item := range value.Completed {
		if index >= maxHLSSegments || item.End < end || (item.End == end && (!item.SkippedInit || index == 0)) || (item.SkippedInit && item.End != end) || !validFingerprint(item.SHA256) || !validFingerprint(item.Resource) || item.ResourceTotal < 0 || len(item.Validator) > 8192 {
			break
		}
		item.URL = ""
		result.Completed = append(result.Completed, item)
		end = item.End
	}
	return result
}
