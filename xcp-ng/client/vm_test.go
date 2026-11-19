package client_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/PlakarKorp/integration-xcp-ng/xcp-ng/client"
	"github.com/PlakarKorp/integration-xcp-ng/xcp-ng/xapi"
	"github.com/stretchr/testify/require"
)

// fakeRPC records the last call and answers it with a canned JSON result.
type fakeRPC struct {
	method string
	params []any
	result string
	err    error
}

func (f *fakeRPC) Call(_ context.Context, method string, out any, params ...any) error {
	f.method, f.params = method, params
	if f.err != nil {
		return f.err
	}
	return json.Unmarshal([]byte(f.result), out)
}

// Trimmed from a real VM.get_record answer.
const vmRecordJSON = `{
  "uuid": "6c3f1c1e-2f2a-4b3e-9c1d-0a1b2c3d4e5f",
  "name_label": "web-01",
  "name_description": "front",
  "power_state": "Running",
  "is_a_template": false,
  "is_a_snapshot": false,
  "is_control_domain": false,
  "allowed_operations": ["snapshot", "clean_shutdown", "hard_shutdown"],
  "tags": ["prod", "web"],
  "other_config": {"base_template_name": "Debian Bookworm 12"},
  "VBDs": ["OpaqueRef:vbd-1", "OpaqueRef:vbd-2"],
  "VIFs": ["OpaqueRef:vif-1"],
  "guest_metrics": "OpaqueRef:gm-1",
  "VCPUs_max": 4,
  "VCPUs_at_startup": 2,
  "memory_static_min": 536870912,
  "memory_static_max": 4294967296,
  "memory_dynamic_min": 4294967296,
  "memory_dynamic_max": 4294967296,
  "HVM_boot_params": {"firmware": "uefi", "order": "cdn"},
  "platform": {"secureboot": "false", "viridian": "true", "cores-per-socket": "2"},
  "NVRAM": {"EFI-variables": "base64=="},
  "bios_strings": {"bios-vendor": "Xen", "system-manufacturer": "Xen"},
  "unknown_field": "ignored"
}`

func TestVMGetRecord(t *testing.T) {
	rpc := &fakeRPC{result: vmRecordJSON}
	c := client.New(rpc)

	vm, err := c.VM.GetRecord(context.Background(), "OpaqueRef:vm-1")
	require.NoError(t, err)

	require.Equal(t, "VM.get_record", rpc.method)
	require.Equal(t, []any{client.VMRef("OpaqueRef:vm-1")}, rpc.params)

	require.Equal(t, "6c3f1c1e-2f2a-4b3e-9c1d-0a1b2c3d4e5f", vm.UUID)
	require.Equal(t, "web-01", vm.NameLabel)
	require.Equal(t, "front", vm.NameDescription)
	require.Equal(t, client.PowerStateRunning, vm.PowerState)
	require.False(t, vm.IsATemplate)
	require.False(t, vm.IsASnapshot)
	require.False(t, vm.IsControlDomain)
	require.Equal(t, []string{"snapshot", "clean_shutdown", "hard_shutdown"}, vm.AllowedOperations)
	require.Equal(t, []string{"prod", "web"}, vm.Tags)
	require.Equal(t, map[string]string{"base_template_name": "Debian Bookworm 12"}, vm.OtherConfig)
	require.Equal(t, []client.VBDRef{"OpaqueRef:vbd-1", "OpaqueRef:vbd-2"}, vm.VBDs)
	require.Equal(t, []client.VIFRef{"OpaqueRef:vif-1"}, vm.VIFs)
	require.Equal(t, client.GuestMetricsRef("OpaqueRef:gm-1"), vm.GuestMetrics)
	require.Equal(t, xapi.Int64(4), vm.VCPUsMax)
	require.Equal(t, xapi.Int64(2), vm.VCPUsAtStartup)
	require.Equal(t, xapi.Int64(536870912), vm.MemoryStaticMin)
	require.Equal(t, xapi.Int64(4294967296), vm.MemoryStaticMax)
	require.Equal(t, xapi.Int64(4294967296), vm.MemoryDynamicMin)
	require.Equal(t, xapi.Int64(4294967296), vm.MemoryDynamicMax)
	require.Equal(t, "uefi", vm.HVMBootParams["firmware"])
	require.Equal(t, "2", vm.Platform["cores-per-socket"])
	require.Equal(t, "base64==", vm.NVRAM["EFI-variables"])
	require.Equal(t, "Xen", vm.BIOSStrings["bios-vendor"])
}

func TestVMGetRecordPassesErrorThrough(t *testing.T) {
	rpc := &fakeRPC{err: &xapi.Error{Method: "VM.get_record", Code: xapi.CodeHandleInvalid, Params: []string{"VM", "OpaqueRef:gone"}}}

	_, err := client.New(rpc).VM.GetRecord(context.Background(), "OpaqueRef:gone")

	require.True(t, xapi.IsCode(err, xapi.CodeHandleInvalid), err)
}

func TestVMRecordHelpers(t *testing.T) {
	regular := client.VMRecord{AllowedOperations: []string{"snapshot"}}
	require.True(t, regular.IsRegularVM())
	require.True(t, regular.CanSnapshot())

	busy := client.VMRecord{AllowedOperations: []string{"clean_shutdown"}}
	require.True(t, busy.IsRegularVM())
	require.False(t, busy.CanSnapshot())

	for name, vm := range map[string]client.VMRecord{
		"template": {IsATemplate: true, AllowedOperations: []string{"snapshot"}},
		"snapshot": {IsASnapshot: true, AllowedOperations: []string{"snapshot"}},
		"dom0":     {IsControlDomain: true, AllowedOperations: []string{"snapshot"}},
	} {
		t.Run(name, func(t *testing.T) {
			require.False(t, vm.IsRegularVM())
			require.False(t, vm.CanSnapshot())
		})
	}
}

func TestRefIsNull(t *testing.T) {
	require.True(t, client.VMRef("").IsNull())
	require.True(t, client.VMRef(client.NullRef).IsNull())
	require.False(t, client.VMRef("OpaqueRef:vm-1").IsNull())
}
