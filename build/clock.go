package build

import "github.com/raulk/clock"

// Clock is the global clock for the system. In standard builds,
// we use a real-time clock, which maps to the `time` package.
//
// Tests that need control of time can replace this variable with
// clock.NewMock(). Always use real time for socket/stream deadlines.
// 时钟是系统的全局时钟。在标准版本中，我们使用一个实时时钟，它映射到 `time` 包。
// 需要控制时间的测试可以用时钟.NewMock()。始终使用实时套接字/流截止日期。
var Clock = clock.New()
