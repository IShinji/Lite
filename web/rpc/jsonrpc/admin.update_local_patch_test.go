package jsonrpc

import (
	"context"
	"testing"

	"github.com/nuomiiiii/lite/pkg/rpc"
)

func TestSelfUpdateDisabledForLocalPatch(t *testing.T) {
	capability := resolveSelfUpdateCapability()
	if capability.Supported || capability.Reason != "local_patch" {
		t.Fatalf("capability = %+v, want unsupported/local_patch", capability)
	}

	req := &rpc.JsonRpcRequest{Params: map[string]any{"version": "1.2.3", "version_hash": "abc1234"}}
	if _, rpcErr := adminStartSelfUpdate(context.Background(), req); rpcErr == nil {
		t.Fatal("adminStartSelfUpdate 应被拒绝")
	}
}
