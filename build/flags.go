package build

// DisableBuiltinAssets disables the resolution of go.rice boxes that store
// built-in assets, such as proof parameters, bootstrap peers, genesis blocks,
// etc.
//
// When this value is set to true, it is expected that the user will
// provide any such configurations through the Lotus API itself.
//
// This is useful when you're using Lotus as a library, such as to orchestrate
// test scenarios, or for other purposes where you don't need to use the
// defaults shipped with the binary.
//
// For this flag to be effective, it must be enabled _before_ instantiating Lotus.
// DisableBuiltinAssets 禁用存储内置资产的 go.rice 盒的解析，例如证明参数、引导节点、创世块等。
// 当此值设置为 true 时，预计用户将通过 Lotus API 本身提供任何此类配置。
// 当您将 Lotus 用作库时，这很有用，例如编排测试场景，或用于不需要使用二进制文件附带的默认值的其他目的。
// 要使此标志生效，必须在实例化 Lotus 之前_before_启用它。
var DisableBuiltinAssets = false
