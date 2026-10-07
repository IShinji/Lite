package selfupdate

import (
	"context"
	"strings"
	"testing"
)

func TestDetectCapabilityRejectsLocalPatch(t *testing.T) {
	capability := DetectCapability()
	if capability.Supported {
		t.Fatal("本地补丁版不应支持自更新")
	}
	if capability.Reason != "local_patch" {
		t.Fatalf("Reason = %q, want local_patch", capability.Reason)
	}
}

func TestPrepareAndLaunchRejectedByLocalPatch(t *testing.T) {
	_, err := PrepareAndLaunch(context.Background(), "1.2.3", "abc1234")
	if err == nil || !strings.Contains(err.Error(), "local_patch") {
		t.Fatalf("PrepareAndLaunch error = %v, want local_patch rejection", err)
	}
}
