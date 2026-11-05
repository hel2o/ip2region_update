package adaptive

import (
	"testing"
	"time"
)

func init() { upInterval = time.Millisecond }

// 健康流量下应爬升到上限。
func TestRampUp(t *testing.T) {
	c := New(60)
	for i := 0; i < 200; i++ {
		c.Record(150*time.Millisecond, false)
		c.Adjust()
		time.Sleep(time.Millisecond) // 满足爬升间隔不至于全跳过
	}
	if got := c.Current(); got < 59 || got > 60.001 {
		t.Fatalf("健康流量应爬到上限 60，实际 %.1f", got)
	}
}

// 出错必须乘性退避，且不会跌破下限。
func TestBackoffOnError(t *testing.T) {
	c := New(60)
	for i := 0; i < 60; i++ { // 先拉满
		c.Record(150*time.Millisecond, false)
		c.Adjust()
	}
	for i := 0; i < 30; i++ { // 连续错误
		c.Record(150*time.Millisecond, true)
		c.Adjust()
	}
	if got := c.Current(); got > c.min*1.01 {
		t.Fatalf("连续 30 个错误应退到下限附近，实际 %.1f", got)
	}
}

// 延迟梯度（RTT 翻倍以上）应触发退避。
func TestBackoffOnLatency(t *testing.T) {
	c := New(60)
	// 建立低基线
	for i := 0; i < 30; i++ {
		c.Record(150*time.Millisecond, false)
		c.Adjust()
	}
	before := c.Current()
	// 持续高延迟
	for i := 0; i < 40; i++ {
		c.Record(600*time.Millisecond, false)
		c.Adjust()
	}
	after := c.Current()
	if after >= before {
		t.Fatalf("延迟梯度超阈应退避：before=%.1f after=%.1f", before, after)
	}
}

// 错误之后恢复健康，应重新爬升。
func TestRecover(t *testing.T) {
	c := New(60)
	for i := 0; i < 20; i++ {
		c.Record(100*time.Millisecond, true)
		c.Adjust()
	}
	low := c.Current()
	for i := 0; i < 100; i++ {
		c.Record(100*time.Millisecond, false)
		c.Adjust()
		time.Sleep(time.Millisecond)
	}
	if got := c.Current(); got <= low {
		t.Fatalf("恢复健康后应爬升：low=%.1f now=%.1f", low, got)
	}
}

// 空窗口不动。
func TestIdleNoChange(t *testing.T) {
	c := New(60)
	if c.Adjust() != "" {
		t.Fatal("无流量时不应有动作")
	}
}
