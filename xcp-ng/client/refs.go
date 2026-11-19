package client

// Ref is an OpaqueRef to a XAPI object of class R. Each class gets its own
// ref type so a VDI ref can't be passed to a VM call. Refs are only valid
// within a session; UUIDs are what gets stored.
type Ref[R any] string

const NullRef = "OpaqueRef:NULL"

func (r Ref[R]) IsNull() bool {
	return r == "" || r == NullRef
}

type (
	VMRef           = Ref[VMRecord]
	VBDRef          = Ref[VBDRecord]
	VIFRef          = Ref[VIFRecord]
	GuestMetricsRef = Ref[GuestMetricsRecord]
)

// Fields land with their service.
type (
	VBDRecord          struct{}
	VIFRecord          struct{}
	GuestMetricsRecord struct{}
)
