package contract

import (
	"github.com/PlakarKorp/integration-sdk"
	"github.com/google/uuid"
)

type Request struct {
	Uuid    uuid.UUID
	Type    string
	Payload interface{}
}

type ReqCreate struct {
	Repository    string
	Configuration []byte
}

type ResCreate struct {
	Err string
}

type ReqOpen struct {
	Repository string
}

type ResOpen struct {
	Configuration []byte
	Err           string
}

// states
type ReqGetStates struct {
}

type ResGetStates struct {
	MACs []integration.MAC
	Err  string
}

type ReqPutState struct {
	MAC  integration.MAC
	Data []byte
}

type ResPutState struct {
	Err string
}

type ReqGetState struct {
	MAC integration.MAC
}

type ResGetState struct {
	Data []byte
	Err  string
}

type ReqDeleteState struct {
	MAC integration.MAC
}
type ResDeleteState struct {
	Err string
}

// packfiles
type ReqGetPackfiles struct {
}

type ResGetPackfiles struct {
	MACs []integration.MAC
	Err  string
}

type ReqPutPackfile struct {
	MAC  integration.MAC
	Data []byte
}

type ResPutPackfile struct {
	Err string
}

type ReqGetPackfile struct {
	MAC integration.MAC
}

type ResGetPackfile struct {
	Data []byte
	Err  string
}

type ReqGetPackfileBlob struct {
	MAC    integration.MAC
	Offset uint64
	Length uint32
}

type ResGetPackfileBlob struct {
	Data []byte
	Err  string
}

type ReqDeletePackfile struct {
	MAC integration.MAC
}
type ResDeletePackfile struct {
	Err string
}

// Locks
type ReqGetLocks struct{}
type ResGetLocks struct {
	Locks []integration.MAC
	Err   string
}

type ReqPutLock struct {
	Mac  integration.MAC
	Data []byte
}
type ResPutLock struct {
	Err string
}

type ReqGetLock struct {
	Mac integration.MAC
}
type ResGetLock struct {
	Data []byte
	Err  string
}

type ReqDeleteLock struct {
	Mac integration.MAC
}
type ResDeleteLock struct {
	Err string
}
