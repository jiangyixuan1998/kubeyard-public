package api

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 并发闸验证：20 个并发申请，同时持有槽位的不超过 2
func TestRiskEvalGateBounded(t *testing.T) {
	var concurrent, maxConcurrent int64
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if !acquireRiskSlot(ctx) {
				return
			}
			n := atomic.AddInt64(&concurrent, 1)
			for {
				old := atomic.LoadInt64(&maxConcurrent)
				if n <= old || atomic.CompareAndSwapInt64(&maxConcurrent, old, n) {
					break
				}
			}
			time.Sleep(50 * time.Millisecond) // 模拟模型调用
			atomic.AddInt64(&concurrent, -1)
		}()
	}
	wg.Wait()
	if maxConcurrent > 2 {
		t.Errorf("并发上限失效: 峰值 %d > 2", maxConcurrent)
	}
}
