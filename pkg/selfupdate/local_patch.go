package selfupdate

// localPatchMarker 非空表示当前二进制带有本地自定义补丁。
// 官方自更新会从上游 Release 下载二进制并覆盖补丁，因此补丁版一律禁用自更新。
// 可通过 -ldflags "-X github.com/nuomiiiii/lite/pkg/selfupdate.localPatchMarker=..." 覆盖。
var localPatchMarker = "browser-tz"

const reasonLocalPatch = "local_patch"
