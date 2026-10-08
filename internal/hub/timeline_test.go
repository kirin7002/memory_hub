package hub

import (
	"testing"
	"time"
)

// TestCausalOrderSurvivesClockConflict 验证设备时间与因果关系冲突时，优先保留因果顺序并标记异常。
// 直接测试排序函数；该规则用于 GET /v1/conversations/{id}/timeline，没有实际 HTTP 请求。
func TestCausalOrderSurvivesClockConflict(t *testing.T) {
	base := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	a := base.Add(time.Minute)
	b := base
	ev := []Event{{ID: "a", TimeQuality: "bounded", Estimated: &a, Earliest: &a, Latest: &a}, {ID: "b", TimeQuality: "bounded", Estimated: &b, Earliest: &b, Latest: &b, Timing: Timing{Parents: []string{"a"}}}}
	out, review, err := orderTimeline(ev)
	if err != nil {
		t.Fatal(err)
	}
	if !review || out[0].ID != "a" || out[1].ID != "b" || out[0].TimeQuality != "inconsistent" {
		t.Fatalf("clock conflict overrode causality: %+v", out)
	}
}

// TestStableIndependentOrderAndMissingParents 验证独立事件排序可重复且不冒充确定时间先后。
// 对应 timeline 的排序规则；缺失必要因果前驱应报错，不能静默省略依赖。
func TestStableIndependentOrderAndMissingParents(t *testing.T) {
	when := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	ev := []Event{{ID: "b", Estimated: &when, TimeQuality: "estimated", Timing: Timing{Origin: "b", Stream: 1}}, {ID: "a", Estimated: &when, TimeQuality: "estimated", Timing: Timing{Origin: "a", Stream: 1}}}
	out, _, err := orderTimeline(ev)
	if err != nil || out[0].ID != "a" || out[0].OrderingBasis != "estimated_or_fallback_unproven" {
		t.Fatalf("unstable/unqualified independent order: %+v %v", out, err)
	}
	ev[0].Timing.Parents = []string{"missing"}
	if _, _, err = orderTimeline(ev); err == nil {
		t.Fatal("silently omitted causal dependency")
	}
}
