package mooc

import "academic/backend/internal/contract"

var progressKeys = []string{"submitStatus", "userSubmitStatus", "userEffectStatus", "finishStatus", "studentScore", "userScore", "finalMark", "usedTryCount", "finished", "learned", "learnStatus", "progress", "completePercent", "learnTime", "learnCount", "totalLearnedTime", "replyCount", "voteCount", "viewStatus", "lessonLearned", "lessonsCount"}

// nativeProgress records field presence, including explicit zero/false, without
// treating absence, last access, or a score as proof of completion.
func nativeProgress(f map[string]any) map[string]any {
	out := map[string]any{}
	for _, k := range progressKeys {
		if value, exists := f[k]; exists && (scalar(value) || value == nil && (k == "viewStatus" || k == "replyCount")) {
			out[k] = value
		}
	}
	return out
}
func progressCoverage(f map[string]any) {
	native := nativeProgress(f)
	f["native_progress_fields"] = native
	f["progress_status"] = "source_fields_observed"
	if len(native) == 0 {
		f["progress_status"] = "not_provided"
	}
	f["progress_coverage"] = "partial: visible catalog source scalars; no inferred completion or aggregate percentage"
	f["progress_value_semantics"] = "raw source values; completePercent scale and learnTime unit are not normalized"
}

// assessmentTimes keeps each source layer and field independently. releaseTime
// is publication/availability, never an invented exam start; deadline and peer
// review end remain distinct even when the official UI shows one fallback label.
func assessmentTimes(f, node, test map[string]any) {
	out := map[string]any{}
	for label, m := range map[string]map[string]any{"node": node, "test": test} {
		m = metadata(m, nil)
		fields := map[string]any{}
		for _, k := range []string{"releaseTime", "startTime", "examStartTime", "endTime", "deadline", "submitEndTime", "examEndTime", "testEndTime", "homeworkEndTime", "jobDeadline", "closeTime", "evaluateStart", "evaluateEnd", "evaluationEndTime", "evaluateScoreReleaseTime", "scoreReleaseTime"} {
			if scalar(m[k]) {
				fields[k] = m[k]
			}
		}
		out[label] = fields
	}
	f["assessment_time_fields"] = out
	f["assessment_time_semantics"] = "releaseTime=publication/availability; deadline=submission cutoff; evaluateStart/evaluateEnd=peer review window; evaluateScoreReleaseTime/scoreReleaseTime=score publication; startTime/endTime retain source meaning without fallback"
	f["assessment_time_coverage"] = "partial: fields supplied by visible catalog only; absent start/end are unknown; original units and sentinels retained"
}

func progressNode(kind contract.Resource, collection string) bool {
	return kind == contract.Assignment || kind == contract.Exam || collection == "units" || collection == "chapters" || collection == "lessons"
}
