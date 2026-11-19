package client

import (
	"slices"

	"github.com/PlakarKorp/integration-xcp-ng/xcp-ng/xapi"
)

type VMService struct {
	objects[VMRecord]
}

// VM.power_state values.
const (
	PowerStateHalted    = "Halted"
	PowerStatePaused    = "Paused"
	PowerStateRunning   = "Running"
	PowerStateSuspended = "Suspended"
)

// VMRecord holds the VM.get_record fields we use.
type VMRecord struct {
	UUID            string `json:"uuid"`
	NameLabel       string `json:"name_label"`
	NameDescription string `json:"name_description"`
	PowerState      string `json:"power_state"`

	IsATemplate     bool `json:"is_a_template"`
	IsASnapshot     bool `json:"is_a_snapshot"`
	IsControlDomain bool `json:"is_control_domain"`

	AllowedOperations []string          `json:"allowed_operations"`
	Tags              []string          `json:"tags"`
	OtherConfig       map[string]string `json:"other_config"`

	VBDs         []VBDRef        `json:"VBDs"`
	VIFs         []VIFRef        `json:"VIFs"`
	GuestMetrics GuestMetricsRef `json:"guest_metrics"`

	VCPUsMax         xapi.Int64 `json:"VCPUs_max"`
	VCPUsAtStartup   xapi.Int64 `json:"VCPUs_at_startup"`
	MemoryStaticMin  xapi.Int64 `json:"memory_static_min"`
	MemoryStaticMax  xapi.Int64 `json:"memory_static_max"`
	MemoryDynamicMin xapi.Int64 `json:"memory_dynamic_min"`
	MemoryDynamicMax xapi.Int64 `json:"memory_dynamic_max"`

	HVMBootParams map[string]string `json:"HVM_boot_params"`
	Platform      map[string]string `json:"platform"`
	NVRAM         map[string]string `json:"NVRAM"`
	BIOSStrings   map[string]string `json:"bios_strings"`
}

// IsRegularVM reports whether the record is a VM proper, not a template,
// a snapshot or dom0.
func (vm VMRecord) IsRegularVM() bool {
	return !vm.IsATemplate && !vm.IsASnapshot && !vm.IsControlDomain
}

// CanSnapshot reports whether the VM may be snapshotted right now.
func (vm VMRecord) CanSnapshot() bool {
	return vm.IsRegularVM() && slices.Contains(vm.AllowedOperations, "snapshot")
}
