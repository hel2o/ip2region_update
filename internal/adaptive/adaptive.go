// Package adaptive 实现拥塞控制式的动态限速器。
//
// 替代固定 QPS：健康时缓慢爬升，出现延迟劣化或错误时乘性退避，
// 恢复后继续爬升。信号来自每次请求的耗时与结果，类似 TCP 拥塞避免
// （AIMD：加性增、乘性减），但以延迟梯度为主信号——不需要服务端
// 显式返回 429，软限流（延迟飙升、200+空 body）同样能触发退避。
//
// 延迟基线用「快慢双 EWMA」（Netflix gradient 同款）：
//
//	gradient = rttFast(α=0.25) / rttSlow(α=0.02)
//
// 平稳时两者收敛、梯度≈1；服务端开始拥塞时 fast 先抬头、梯度变大。
// 不用 min-RTT 做基线：网络抖动下历史最小值会被个别快请求压得极低，
// 梯度永远超阈，速率会被锁死在下限（实测踩过）。
package adaptive

import (
	"context"
	"log"
	"math"
	"sync"
	"time"
)

// Controller 动态 QPS 控制器。
//
// 用法：New 后 Start 令牌发放循环，请求方从 Tokens() 拿令牌，
// 完成后调 Record 反馈耗时与结果；外部定时器每秒调一次 Adjust。
type Controller struct {
	mu      sync.Mutex
	qps     float64       // 当前目标速率
	max     float64       // 上限（来自 -qps）
	min     float64       // 下限（退避的兜底）
	rttFast float64       // 近期 RTT 指数滑动平均（秒），对变化敏感
	rttSlow float64       // 长期 RTT 指数滑动平均（秒），慢速基线
	lastUp  time.Time     // 上次爬升时刻（限制爬升频率）
	tokens  chan struct{} // 令牌桶
	// 最近一个调整窗口的观测聚合
	winN     int
	winErr   int
	winRTT   float64
	winMaxRT time.Duration

	n       int64 // 调整次数（调试用）
	started bool
}

const (
	alphaFast     = 0.25 // 快 EWMA：几次请求就能反映延迟变化
	alphaSlow     = 0.02 // 慢 EWMA：约 50 个请求的长期基线
	upRatio       = 0.04 // 每次爬升 = max 的 4%
	gradDownRatio = 0.85 // 延迟梯度超阈 → 乘 0.85
	errDownRatio  = 0.55 // 出错 → 乘 0.55
	gradHigh      = 1.35 // 梯度超过此值视为劣化
	gradLow       = 1.12 // 低于此值视为健康
)

// upInterval 两次爬升的最小间隔（var 是为了测试注入，生产勿改）。
var upInterval = 800 * time.Millisecond

// New 创建控制器。cap 是 QPS 上限；启动速率为上限的 40%。
func New(cap int) *Controller {
	return &Controller{
		max:    float64(cap),
		min:    math.Max(5, float64(cap)/10),
		qps:    math.Min(float64(cap), math.Max(math.Max(5, float64(cap)/10), float64(cap)*0.40)),
		tokens: make(chan struct{}, 8),
	}
}

// Tokens 返回令牌通道（消费方从这里拿令牌）。
func (c *Controller) Tokens() chan struct{} { return c.tokens }

// Start 启动令牌发放循环，随 ctx 退出。
func (c *Controller) Start(ctx context.Context) {
	c.mu.Lock()
	if c.started {
		c.mu.Unlock()
		return
	}
	c.started = true
	c.mu.Unlock()

	go func() {
		timer := time.NewTimer(0)
		defer timer.Stop()
		for {
			iv := time.Duration(float64(time.Second) / c.Current())
			timer.Reset(iv)
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				select {
				case c.tokens <- struct{}{}:
				default:
				}
			}
		}
	}()
}

// Current 取当前目标 QPS。
func (c *Controller) Current() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.qps
}

// SentCount 取已发放令牌数（测试与调试用）。
func (c *Controller) SentCount() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

// gradient 计算延迟梯度，无样本时返回 1（中性）。
func (c *Controller) gradient() float64 {
	if c.rttFast <= 0 || c.rttSlow <= 0 {
		return 1
	}
	return c.rttFast / c.rttSlow
}

// Record 反馈一次请求的结果。
//
//   - 成功：更新快慢 EWMA
//   - 失败：只计数，退避在 Adjust 窗口决策
func (c *Controller) Record(rtt time.Duration, failed bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.winN++
	if failed {
		c.winErr++
		return
	}
	s := rtt.Seconds()
	if c.rttFast == 0 {
		c.rttFast, c.rttSlow = s, s
	} else {
		c.rttFast += alphaFast * (s - c.rttFast)
		c.rttSlow += alphaSlow * (s - c.rttSlow)
	}
	if rtt > c.winMaxRT {
		c.winMaxRT = rtt
	}
	c.winRTT += s
}

// Adjust 做一次窗口决策，返回本次动作描述（"up" / "down:原因" / ""）。
//
//   - 窗口内出现错误 → 乘性退避（错误是最硬的信号）
//   - 延迟梯度超阈 → 乘性退避（软限流的先兆）
//   - 健康且距上次爬升够久 → 加性爬升
func (c *Controller) Adjust() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.winN == 0 {
		return "" // 没流量，不动
	}
	n, errN, maxRT, avgRT := c.winN, c.winErr, c.winMaxRT, time.Duration(c.winRTT/float64(c.winN)*float64(time.Second))
	c.winN, c.winErr, c.winRTT, c.winMaxRT = 0, 0, 0, 0

	action := ""
	g := c.gradient()
	switch {
	case errN > 0:
		// 连续错误窗口加倍退避：乘 (0.55)^k
		c.qps *= math.Pow(errDownRatio, float64(mini(errN, 3)))
		action = "down:错误"
	case g > gradHigh:
		c.qps *= gradDownRatio
		action = "down:延迟梯度"
	}
	c.qps = clamp(c.qps, c.min, c.max)

	// 健康才允许爬升：无错误、梯度未超阈、间隔够久
	if action == "" && errN == 0 && time.Since(c.lastUp) >= upInterval && c.gradient() <= gradLow {
		c.qps = math.Min(c.max, c.qps+c.max*upRatio)
		c.lastUp = time.Now()
		action = "up"
	}
	c.n++
	if action != "" {
		log.Printf("[adaptive] 窗口 n=%d err=%d avg=%s max=%s grad=%.2f → %s qps=%.0f",
			n, errN, avgRT, maxRT, g, action, c.qps)
	}
	return action
}

func clamp(v, lo, hi float64) float64 { return math.Min(hi, math.Max(lo, v)) }
func mini(a, b int) int {
	if a < b {
		return a
	}
	return b
}
